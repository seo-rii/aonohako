package execute

import (
	"context"
	"encoding/base64"
	"math"
	"os"
	"sort"
	"strconv"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// TestDuranteJudgeIntegration runs judge mode through the real sandbox, the
// patched runtime and a fixed CPUID profile. Coefficients are synthetic: they
// select which side of the limit an actual complete count falls on and are
// not a deployment model.
func TestDuranteJudgeIntegration(t *testing.T) {
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
	for _, tc := range []struct {
		name        string
		numerator   uint64
		denominator uint64
		output      string
		integrity   durante.JudgeIntegrity
		ratio       uint64
		want        string
	}{
		// MaxUint64 makes any count round up to a single nanosecond.
		{"accepted below the virtual limit", 1, math.MaxUint64, expected, durante.JudgeIntegrityOff, 0, model.RunStatusAccepted},
		// Over 1 ms per instruction: any complete count exceeds the 1 ms limit.
		{"time limit exceeded on work", 1_000_001, 1, expected, durante.JudgeIntegrityOff, 0, model.RunStatusTLE},
		{"wrong answer", 1, math.MaxUint64, "0", durante.JudgeIntegrityOff, 0, model.RunStatusWA},
		// A 1 ns estimate against real instrumented CPU is implausible at ratio 1.
		{"implausible work is withheld", 1, math.MaxUint64, expected, durante.JudgeIntegrityPlausibility, 1, model.RunStatusRE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := timeMeasurementConfig()
			cfg.Drrun, cfg.Client = os.Getenv("AONOHAKO_DURANTE_DRRUN"), os.Getenv("AONOHAKO_DURANTE_CLIENT")
			if cfg.Drrun == "" {
				cfg.Drrun = durante.DefaultDrrun
			}
			if cfg.Client == "" {
				cfg.Client = durante.DefaultClient
			}
			cfg.RuntimeImage, cfg.TimePolicy.RuntimeID = image, runtimeID
			cfg.TimePolicy.InstructionNSNumerator, cfg.TimePolicy.NSDenominator = tc.numerator, tc.denominator
			cfg.CPUIDProfile = durante.CPUIDProfileX8664V2
			cfg.TimePolicy.CPUIDProfile = durante.CPUIDProfileX8664V2
			cfg.Judge = durante.JudgePolicy{Enabled: true, Families: []string{"cpp"}, Integrity: tc.integrity, IntegrityRatio: tc.ratio}
			req := &model.RunRequest{
				Lang: "CPP20", WorkMeasurement: true, WorkMeasurementTime: true, WorkMeasurementJudge: true,
				Binaries: []model.Binary{{Name: "Main", DataB64: base64.StdEncoding.EncodeToString(data), Mode: "exec"}},
				Stdin:    "4096", ExpectedStdout: tc.output,
				Limits:                model.Limits{TimeMs: 1, WallTimeMs: 1, MemoryMB: 16},
				WorkMeasurementLimits: &durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 10000, MemoryMB: 128},
			}
			svc := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
			resp := svc.Run(context.Background(), req, Hooks{})
			j := resp.WorkMeasurementJudgment
			if resp.Status != tc.want || j == nil || j.Verdict != tc.want || resp.VerdictSource != "durante_judge" {
				t.Fatalf("verdict %q judgment %+v reason %q", resp.Status, j, resp.Reason)
			}
			report := resp.WorkMeasurement
			if report == nil || !report.Complete || report.CPUIDProfile != durante.CPUIDProfileX8664V2 || !report.ValidInstallationIdentity() {
				t.Fatalf("judge observation lacks a complete profiled receipt: %+v", report)
			}
			if (resp.Score != nil) != (tc.want == model.RunStatusAccepted) {
				t.Fatalf("score %v for verdict %q", resp.Score, tc.want)
			}
			if tc.want == model.RunStatusAccepted || tc.want == model.RunStatusTLE {
				if j.EstimatedTimeNs == nil || resp.WorkMeasurementExecution.TimeEstimate.CPUIDProfile != durante.CPUIDProfileX8664V2 {
					t.Fatalf("missing profiled estimate: %+v", j)
				}
			}
			if tc.integrity == durante.JudgeIntegrityPlausibility && !j.IntegrityFlagged {
				t.Fatalf("plausibility case not flagged: %+v", j)
			}
			// The remote control plane recomputes exactly the same verdict.
			if err := validateRemoteWorkMeasurement(req, resp); err != nil {
				t.Fatal(err)
			}
			if err := validateRemoteWorkMeasurementTime(req, resp, cfg); err != nil {
				t.Fatal(err)
			}
			if err := validateRemoteWorkMeasurementJudge(req, resp, cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
}
