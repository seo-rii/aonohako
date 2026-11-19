package execute

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// Both peers execute the same precompiled native fixture under the real
// instrumentation. The runtime image need not contain a C compiler.
func TestDuranteInteractiveIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("dedicated durante validation image required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("durante sandbox integration requires root")
	}
	forceDirectMode(t)
	cfg, err := durante.ParseConfig("cpp", os.Getenv("AONOHAKO_DURANTE_DRRUN"), os.Getenv("AONOHAKO_DURANTE_CLIENT"), os.Getenv("AONOHAKO_DURANTE_DISPATCH_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(os.Getenv("AONOHAKO_TEST_DURANTE_INTERACTIVE"))
	if err != nil {
		t.Fatalf("read precompiled interactive fixture: %v", err)
	}
	binary := model.Binary{Name: "Main", DataB64: base64.StdEncoding.EncodeToString(data), Mode: "exec"}
	svc := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	request := func(mode string, enabled bool) *model.RunRequest {
		limits := model.Limits{TimeMs: 3000, WallTimeMs: 10000, MemoryMB: 512}
		return &model.RunRequest{
			Lang: "CPP20", WorkMeasurement: enabled,
			Binaries: []model.Binary{binary}, Stdin: mode, Limits: limits,
			Interactor: &model.InteractorSpec{Lang: "CPP20", Binaries: []model.Binary{binary}, Limits: &limits},
		}
	}
	assertReports := func(t *testing.T, req *model.RunRequest, response model.RunResponse, complete [2]bool) {
		t.Helper()
		if response.WorkMeasurement != nil || response.WorkMeasurementExecution != nil || len(response.WorkMeasurements) != 2 {
			t.Fatalf("missing per-role observations or unexpected aggregate/envelope: %+v", response)
		}
		for i, role := range []string{"contestant", "interactor"} {
			process := response.WorkMeasurements[i]
			if process.Role != role || process.ProgramID != role || process.ParticipantIndex != nil || process.Report == nil {
				t.Fatalf("invalid %s observation identity: %+v", role, process)
			}
			if complete[i] {
				if process.ExecutionStatus != "OK" || process.ExitCode == nil || *process.ExitCode < 0 || !validDiagnosticWorkReport("CPP20", process.Report) || process.Report.REPBreakdown == nil {
					t.Fatalf("invalid completed %s observation: %+v; report=%+v", role, process, process.Report)
				}
			} else if process.Report.Complete || process.Report.Instructions != nil || !process.Report.ValidUnavailableResult() || !process.Report.ValidDiagnosticContract() {
				t.Fatalf("interrupted %s retained usable totals: %+v; report=%+v", role, process, process.Report)
			}
		}
		if err := validateRemoteWorkMeasurement(req, response); err != nil {
			t.Fatalf("local observations do not satisfy the forwarding contract: %v; response=%+v", err, response)
		}
	}
	for _, tc := range []struct {
		mode   string
		status string
		score  float64
	}{
		{"accept", model.RunStatusAccepted, 1},
		{"wrong", model.RunStatusWA, 0},
		{"final_accept", model.RunStatusAccepted, 1},
		{"final_wrong", model.RunStatusWA, 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			req := request(tc.mode, true)
			response := svc.Run(context.Background(), req, Hooks{})
			if response.Status != tc.status || response.Score == nil || *response.Score != tc.score {
				t.Fatalf("instrumentation changed the interactor verdict: %+v", response)
			}
			assertReports(t, req, response, [2]bool{true, true})
			if len(response.Steps) != 2 || response.Steps[0].CPUTimeMs == 0 || response.Steps[1].CPUTimeMs == 0 || response.Steps[0].MemoryKB == 0 || response.Steps[1].MemoryKB == 0 {
				t.Fatalf("ordinary per-role accounting missing: %+v", response.Steps)
			}
		})
	}
	t.Run("unmetered", func(t *testing.T) {
		response := svc.Run(context.Background(), request("accept", false), Hooks{})
		if response.Status != model.RunStatusAccepted || response.WorkMeasurement != nil || len(response.WorkMeasurements) != 0 {
			t.Fatalf("unmetered interactive execution changed: %+v", response)
		}
	})
	for _, variant := range []string{"", "_bound_options", "_latency"} {
		name := "diagnostic_time_budgets" + variant
		t.Run(name, func(t *testing.T) {
			timeCfg := peerTimeIntegrationConfig(t, cfg, variant == "_latency")
			req := request("accept", true)
			addPeerTimeIntegrationBudgets(t, req)
			if variant == "_bound_options" {
				timeCfg = bindIntegrationRuntimeOptions(t, timeCfg, req)
			}
			timeService := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: timeCfg}})
			response := timeService.Run(context.Background(), req, Hooks{})
			if response.Status != model.RunStatusAccepted || response.Score == nil || *response.Score != 1 {
				t.Fatalf("diagnostic time budgets changed the interactor verdict: %+v", response)
			}
			assertReports(t, req, response, [2]bool{true, true})
			assertPeerTimeIntegrationBudgets(t, req, response, timeCfg)
		})
	}
	for _, tc := range []struct {
		mode, status, source string
		limited              string
	}{
		{"accept", model.RunStatusAccepted, "interactor", ""},
		{"wrong", model.RunStatusWA, "interactor:exit_code", ""},
		{"final_accept", model.RunStatusAccepted, "interactor", ""},
		{"final_wrong", model.RunStatusWA, "interactor:exit_code", ""},
		{"burn", model.RunStatusTLE, "contestant:durante_work_clock", "contestant"},
		{"interactor_burn", model.RunStatusInitFail, "interactor:durante_work_clock", "interactor"},
		{"wait", model.RunStatusInitFail, "contestant:durante_peer_clock_error", ""},
		{"accept_cancel", model.RunStatusInitFail, "contestant:durante_peer_cancelled", ""},
		{"wrong_cancel", model.RunStatusWA, "interactor:exit_code", ""},
	} {
		t.Run("online_"+tc.mode, func(t *testing.T) {
			req := request(tc.mode, true)
			if tc.mode == "wait" {
				req.Limits.TimeMs, req.Limits.WallTimeMs = 500, 700
			}
			addPeerTimeIntegrationBudgets(t, req)
			req.WorkMeasurementPeerTime.Version = 2
			for i := range req.WorkMeasurementPeerTime.Budgets {
				req.WorkMeasurementPeerTime.Budgets[i].TimeMs = 5
			}
			measurement := bindIntegrationRuntimeOptions(t, peerTimeIntegrationConfig(t, cfg, false), req)
			measurement.Judge = durante.JudgePolicy{Enabled: true, VerdictsAllowed: true, Integrity: durante.JudgeIntegrityOff, Families: []string{"cpp"}}
			service := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: measurement}})
			response := service.Run(context.Background(), req, Hooks{})
			if response.Status != tc.status || response.VerdictSource != tc.source || (response.Score == nil) != (tc.status == model.RunStatusInitFail) {
				t.Fatalf("unexpected clock verdict: %+v; roles=%+v", response, response.WorkMeasurements)
			}
			if err := validateRemoteWorkMeasurementPeerTime(req, response, measurement); err != nil {
				t.Fatal(err)
			}
			for _, r := range response.WorkMeasurements {
				if r.TimeBudget == nil || r.TimeBudget.Clock == nil {
					t.Fatalf("missing clock for %s", r.Role)
				}
				if r.Role == tc.limited && (r.TimeBudget.Exceeded == nil || !*r.TimeBudget.Exceeded || !r.Report.ValidUnavailableResult()) {
					t.Fatalf("unverified live cutoff for %s: %+v", r.Role, r)
				}
			}
		})
	}
	t.Run("contestant CPU limit", func(t *testing.T) {
		req := request("burn", true)
		req.Limits.TimeMs = 200
		response := svc.Run(context.Background(), req, Hooks{})
		if response.Status != model.RunStatusTLE {
			t.Fatalf("contestant CPU limit was not enforced: %+v", response)
		}
		assertReports(t, req, response, [2]bool{false, false})
	})
	t.Run("contestant wall limit", func(t *testing.T) {
		req := request("wait", true)
		req.Limits.WallTimeMs = 400
		response := svc.Run(context.Background(), req, Hooks{})
		if response.Status != model.RunStatusTLE {
			t.Fatalf("contestant wall limit was not enforced: %+v", response)
		}
		assertReports(t, req, response, [2]bool{false, false})
	})
	t.Run("interactor CPU limit preserves completed contestant", func(t *testing.T) {
		req := request("interactor_burn", true)
		req.Interactor.Limits.TimeMs = 400
		response := svc.Run(context.Background(), req, Hooks{})
		if response.Status != model.RunStatusRE {
			t.Fatalf("interactor resource failure changed verdict: %+v", response)
		}
		assertReports(t, req, response, [2]bool{true, false})
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(time.Second, cancel)
		defer timer.Stop()
		req := request("wait", true)
		response := svc.Run(ctx, req, Hooks{})
		if ctx.Err() == nil || response.Status == model.RunStatusAccepted {
			t.Fatalf("parent cancellation did not stop the interactive peers: %+v", response)
		}
		assertReports(t, req, response, [2]bool{false, false})
	})
}
