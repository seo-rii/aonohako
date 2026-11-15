package execute

import (
	"context"
	"encoding/base64"
	"math"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestDuranteTimeEstimateIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("dedicated durante validation image required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("sandbox integration requires root")
	}
	forceDirectMode(t)
	data, err := os.ReadFile(os.Getenv("AONOHAKO_TEST_DURANTE_REFERENCE"))
	if err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("AONOHAKO_DURANTE_RUNTIME_IMAGE")
	runtimeID, err := durante.TimeRuntimeID("CPP20", image)
	if err != nil {
		t.Fatal(err)
	}
	// Independently calculate the fixture's expected weighted sorted checksum.
	values, value := make([]uint32, 4096), uint32(42)
	for i := range values {
		value = value*1664525 + 1013904223
		values[i] = value
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var checksum uint64
	for i, value := range values {
		checksum += uint64(value) * uint64(i+1)
	}
	expected := strconv.FormatUint(checksum, 10)
	makeRun := func() (durante.Config, *model.RunRequest) {
		cfg := timeMeasurementConfig()
		cfg.Drrun, cfg.Client = os.Getenv("AONOHAKO_DURANTE_DRRUN"), os.Getenv("AONOHAKO_DURANTE_CLIENT")
		if cfg.Drrun == "" {
			cfg.Drrun = durante.DefaultDrrun
		}
		if cfg.Client == "" {
			cfg.Client = durante.DefaultClient
		}
		cfg.RuntimeImage, cfg.TimePolicy.RuntimeID = image, runtimeID
		// The extreme synthetic coefficient exercises a value below the nominal
		// limit while raw instrumented CPU exceeds it; it is not a deployment model.
		cfg.TimePolicy.NSDenominator = math.MaxUint64
		req := &model.RunRequest{
			Lang: "CPP20", WorkMeasurement: true, WorkMeasurementTime: true,
			Binaries: []model.Binary{{Name: "Main", DataB64: base64.StdEncoding.EncodeToString(data), Mode: "exec"}},
			Stdin:    "4096", ExpectedStdout: expected,
			Limits:                model.Limits{TimeMs: 1, WallTimeMs: 1, MemoryMB: 16},
			WorkMeasurementLimits: &durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 10000, MemoryMB: 128},
		}
		return cfg, req
	}
	run := func(ctx context.Context, cfg durante.Config, req *model.RunRequest) model.RunResponse {
		svc := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
		return svc.Run(ctx, req, Hooks{})
	}
	for _, tc := range []struct {
		name             string
		exceeds, matches bool
	}{
		{"within reference limit", false, true},
		{"exceeds reference limit", true, true},
		{"wrong output retains reference estimate", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, req := makeRun()
			if tc.exceeds {
				cfg.TimePolicy.InstructionNSNumerator, cfg.TimePolicy.NSDenominator = 1_000_001, 1
			}
			if !tc.matches {
				req.ExpectedStdout = "wrong"
			}
			response := run(context.Background(), cfg, req)
			d := response.WorkMeasurementExecution
			if response.Status != model.RunStatusInitFail || response.Score != nil || response.CPUTimeNormalization != nil || d == nil || d.State != "complete" || d.TimeEstimate == nil || d.NominalTimeExceeded == nil || d.TimeEstimateUnavailable != "" || d.OutputMatches == nil {
				t.Fatalf("reference observation failed: response=%+v execution=%+v", response, d)
			}
			if *d.NominalTimeExceeded != tc.exceeds || *d.OutputMatches != tc.matches || d.TimeEstimate.RuntimeID != runtimeID || response.CPUTimeNs <= 1_000_000 {
				t.Fatalf("reference/raw limits conflated: response=%+v execution=%+v", response, d)
			}
			if response.WorkMeasurement == nil || response.WorkMeasurement.REPBreakdown == nil || !response.WorkMeasurement.ValidREPTelemetry() {
				t.Fatalf("time observation lost REP detail: %+v", response.WorkMeasurement)
			}
			if err := validateRemoteWorkMeasurement(req, response); err != nil {
				t.Fatal(err)
			}
			if err := validateRemoteWorkMeasurementTime(req, response, cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct{ name, input, state string }{
		{"raw CPU stop", "0", "resource_exhausted"},
		{"runtime error", "65537", "failed"},
		{"cancellation", "0", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, req := makeRun()
			req.Stdin = tc.input
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.state == "resource_exhausted" {
				req.WorkMeasurementLimits.CPUTimeMs = 100
			}
			if tc.state == "cancelled" {
				timer := time.AfterFunc(200*time.Millisecond, cancel)
				defer timer.Stop()
			}
			response := run(ctx, cfg, req)
			d := response.WorkMeasurementExecution
			if response.Status != model.RunStatusInitFail || response.Score != nil || d == nil || d.State != tc.state || d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "execution_"+tc.state {
				t.Fatalf("failed run returned a time: response=%+v execution=%+v", response, d)
			}
		})
	}
	t.Run("time option disabled", func(t *testing.T) {
		cfg, req := makeRun()
		req.WorkMeasurementTime = false
		cfg.TimePolicy = nil
		response := run(context.Background(), cfg, req)
		d := response.WorkMeasurementExecution
		if response.Status != model.RunStatusInitFail || d == nil || d.State != "complete" || d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "" {
			t.Fatalf("legacy envelope changed: response=%+v execution=%+v", response, d)
		}
	})
}
