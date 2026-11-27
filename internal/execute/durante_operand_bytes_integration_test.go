//go:build durante_v11 || durante_v12

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

func TestDuranteMemoryOperandBytesIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("dedicated durante validation image required")
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
	req := &model.RunRequest{
		Lang: "CPP20", WorkMeasurement: true,
		Binaries:       []model.Binary{{Name: "Main", DataB64: base64.StdEncoding.EncodeToString(data), Mode: "exec"}},
		ExpectedStdout: "rep-breakdown-ok",
		Limits:         model.Limits{TimeMs: 3000, WallTimeMs: 10000, MemoryMB: 512},
	}
	cfg.LatencyBound = true
	for _, enabled := range []bool{false, true} {
		cfg.MemoryOperandBytes = enabled
		svc := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
		resp := svc.Run(context.Background(), req, Hooks{})
		r := resp.WorkMeasurement
		if resp.Status != model.RunStatusAccepted || !validDiagnosticWorkReport(req.Lang, r) || r.LatencyBound == nil ||
			r.REPBytes == nil || *r.REPBytes != 131 || (r.MemoryOperandBytes != nil) != enabled {
			t.Fatalf("enabled %t: status=%s reason=%s report=%+v", enabled, resp.Status, resp.Reason, r)
		}
		// MOVS reads and writes 1, 4 and 8 bytes. STOS writes 2, 8 and 1.
		// Seven byte comparisons read one byte each. Include zero-count REP sites.
		if enabled && (r.MemoryOperandBytes.Version != 1 || r.MemoryOperandBytes.Bytes != 44) {
			t.Fatalf("unexpected nominal memory demand: %+v", r.MemoryOperandBytes)
		}
		if err := validateRemoteMeasurementCounters(req, resp, cfg); err != nil {
			t.Fatal(err)
		}
		other := cfg
		other.MemoryOperandBytes = !enabled
		if err := validateRemoteMeasurementCounters(req, resp, other); err == nil {
			t.Fatal("accepted a receipt from a different counter configuration")
		}
	}
}
