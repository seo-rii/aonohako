package execute

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/workmeter"
)

func TestWorkMeasurementRejectsDisabledAndAmbiguousRequests(t *testing.T) {
	cfg, err := workmeter.ParseConfig("cpp", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	enabled := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	for _, tc := range []struct {
		name   string
		svc    *Service
		req    model.RunRequest
		reason string
	}{
		{"disabled", New(), model.RunRequest{Lang: "CPP20", WorkMeasurement: true}, "not enabled"},
		{"ambiguous", enabled, model.RunRequest{Lang: "binary", WorkMeasurement: true}, "source-language"},
		{"interactive", enabled, model.RunRequest{Lang: "CPP20", WorkMeasurement: true, Interactor: &model.InteractorSpec{}}, "ordinary single-program"},
		{"spj", enabled, model.RunRequest{Lang: "CPP20", WorkMeasurement: true, SPJ: &model.SPJSpec{}}, "ordinary single-program"},
		{"pipeline", enabled, model.RunRequest{Lang: "CPP20", WorkMeasurement: true, Pipeline: &model.PipelineV1{}}, "ordinary single-program"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.svc.Run(context.Background(), &tc.req, Hooks{})
			if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, tc.reason) {
				t.Fatalf("%+v", resp)
			}
		})
	}
}

// This is run by docker/workmeter.Dockerfile's workmeter-validation target.
// Missing root/installed instrumentation is a failure when explicitly enabled.
func TestWorkmeterSandboxIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_WORKMETER") != "1" {
		t.Skip("set AONOHAKO_TEST_WORKMETER=1 in the dedicated workmeter validation image")
	}
	if os.Geteuid() != 0 {
		t.Fatal("workmeter sandbox integration requires root")
	}
	forceDirectMode(t)
	cfg, err := workmeter.ParseConfig("cpp", os.Getenv("AONOHAKO_WORKMETER_DRRUN"), os.Getenv("AONOHAKO_WORKMETER_CLIENT"), os.Getenv("AONOHAKO_WORKMETER_DISPATCH_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	probe := os.Getenv("AONOHAKO_WORKMETER_TEST_BINARY")
	if probe == "" {
		t.Fatal("missing precompiled integration fixture")
	}
	data, err := os.ReadFile(probe)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	run := func(input, expected string, enabled bool, limit int) model.RunResponse {
		return svc.Run(context.Background(), &model.RunRequest{Lang: "CPP20", WorkMeasurement: enabled, Binaries: []model.Binary{{Name: "Main", DataB64: base64.StdEncoding.EncodeToString(data), Mode: "exec"}}, Stdin: input, ExpectedStdout: expected, Limits: model.Limits{TimeMs: limit, WallTimeMs: 10000, MemoryMB: 512}}, Hooks{})
	}
	for _, tc := range []struct{ name, input, expected string }{{"ordinary", "normal 10000", "49995000"}, {"fork policy", "fork", "blocked"}, {"network policy", "network", "blocked"}} {
		t.Run(tc.name, func(t *testing.T) {
			resp := run(tc.input, tc.expected, true, 3000)
			if resp.Status != model.RunStatusAccepted {
				t.Fatalf("instrumented run failed: %+v", resp)
			}
			m := resp.WorkMeasurement
			if m == nil || !m.Complete || m.Instructions == nil || *m.Instructions == 0 || m.Processes != 1 || m.Authority != workmeter.Authority {
				t.Fatalf("invalid work report: %+v", m)
			}
			if resp.CPUTimeNs == 0 || resp.MemoryKB == 0 {
				t.Fatalf("ordinary accounting missing: %+v", resp)
			}
		})
	}
	t.Run("unmetered", func(t *testing.T) {
		resp := run("normal 10000", "49995000", false, 3000)
		if resp.Status != model.RunStatusAccepted || resp.WorkMeasurement != nil {
			t.Fatalf("%+v", resp)
		}
	})
	t.Run("limit remains enforced", func(t *testing.T) {
		resp := run("burn", "", true, 100)
		if resp.Status != model.RunStatusTLE {
			t.Fatalf("limit not enforced: %+v", resp)
		}
		if resp.WorkMeasurement == nil || resp.WorkMeasurement.Complete || resp.WorkMeasurement.Instructions != nil {
			t.Fatalf("forced stop produced valid total: %+v", resp.WorkMeasurement)
		}
	})
}
