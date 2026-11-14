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
				if process.ExecutionStatus != "OK" || process.ExitCode == nil || *process.ExitCode < 0 || !validDiagnosticWorkReport("CPP20", process.Report) {
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
