package execute

import (
	"context"
	"encoding/base64"
	"os"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestDuranteREPBreakdownIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("dedicated durante validation image required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("durante sandbox integration requires root")
	}
	forceDirectMode(t)
	cfg, err := durante.ParseConfig("cpp", os.Getenv("AONOHAKO_DURANTE_DRRUN"), os.Getenv("AONOHAKO_DURANTE_CLIENT"), "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(os.Getenv("AONOHAKO_TEST_DURANTE_REP_BREAKDOWN"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	for _, measured := range []bool{false, true} {
		name := "native"
		if measured {
			name = "measured"
		}
		t.Run(name, func(t *testing.T) {
			req := &model.RunRequest{
				Lang: "CPP20", WorkMeasurement: measured,
				Binaries:       []model.Binary{{Name: "Main", DataB64: base64.StdEncoding.EncodeToString(data), Mode: "exec"}},
				ExpectedStdout: "rep-breakdown-ok",
				Limits:         model.Limits{TimeMs: 3000, WallTimeMs: 10000, MemoryMB: 512},
			}
			response := svc.Run(context.Background(), req, Hooks{})
			if response.Status != model.RunStatusAccepted || response.ExitCode == nil || *response.ExitCode != 0 {
				t.Fatalf("fixed REP fixture failed: %+v", response)
			}
			if !measured {
				if response.WorkMeasurement != nil {
					t.Fatal("unrequested counters returned")
				}
				return
			}
			r := response.WorkMeasurement
			if !validDiagnosticWorkReport(req.Lang, r) || r.REPBreakdown == nil || r.Processes != 1 || r.Images != 1 {
				t.Fatalf("missing complete REP observation: %+v", r)
			}
			want := durante.REPBreakdown{Version: 1, MOVSBytes: 65, STOSBytes: 66, MOVSEntries: 3, STOSEntries: 3}
			if *r.REPBreakdown != want || *r.REPBytes != 131 || *r.REPEntries != 6 || *r.UnsupportedREPEntries != 0 {
				t.Fatalf("incorrect mixed-width or zero-count REP demand: report=%+v breakdown=%+v", r, r.REPBreakdown)
			}
			if err := validateRemoteWorkMeasurement(req, response); err != nil {
				t.Fatal(err)
			}
		})
	}
}
