package execute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
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
			cfg.Judge = durante.JudgePolicy{Enabled: true, VerdictsAllowed: true, Families: []string{"cpp"}, Integrity: tc.integrity, IntegrityRatio: tc.ratio}
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
	// Shadow mode: the ordinary CPU-time verdict stays authoritative and a
	// separate judge-mode execution of the same input is only compared.
	for _, tc := range []struct {
		name                   string
		numerator, denominator uint64
		agreement, shadow      string
		latency, urlInput      bool
	}{
		{"shadow agrees", 1, math.MaxUint64, model.ShadowAgree, model.RunStatusAccepted, false, false},
		{"shadow disagrees", 1_000_001, 1, model.ShadowDisagree, model.RunStatusTLE, false, false},
		{"shadow latency agrees", 1, math.MaxUint64, model.ShadowAgree, model.RunStatusAccepted, true, false},
		{"shadow latency disagrees", 1_000_001, 1, model.ShadowDisagree, model.RunStatusTLE, true, false},
		{"shadow and sampled primary share binary URL stdin", 1, math.MaxUint64, model.ShadowAgree, model.RunStatusAccepted, true, true},
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
			if tc.latency {
				cfg.LatencyBound = true
				cfg.TimePolicy.Method = durante.TimeMethodLatencyBoundREP
				cfg.TimePolicy.InstructionNSNumerator = 0
				cfg.TimePolicy.LatencyX16NSNumerator = tc.numerator
			}
			cfg.CPUIDProfile, cfg.TimePolicy.CPUIDProfile = durante.CPUIDProfileX8664V2, durante.CPUIDProfileX8664V2
			cfg.Judge = durante.JudgePolicy{Enabled: true, Families: []string{"cpp"}, Integrity: durante.JudgeIntegrityOff}
			req := &model.RunRequest{
				Lang: "CPP20", WorkMeasurementShadow: true,
				Binaries: []model.Binary{{Name: "Main", DataB64: base64.StdEncoding.EncodeToString(data), Mode: "exec"}},
				Stdin:    "4096", ExpectedStdout: expected,
				Limits:                model.Limits{TimeMs: 2000, WallTimeMs: 5000, MemoryMB: 64},
				WorkMeasurementLimits: &durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 10000, MemoryMB: 128},
			}
			svc := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
			var downloads atomic.Int32
			digest := make(chan string, 1)
			if tc.urlInput {
				// Include binary data beyond the inline text limit. The program
				// consumes the first integer; a second GET changes its answer.
				chunk := bytes.Repeat([]byte{0, 0xff}, 16<<10)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if downloads.Add(1) != 1 {
						fmt.Fprint(w, "1")
						return
					}
					hash := sha256.New()
					out := io.MultiWriter(w, hash)
					if _, err := fmt.Fprintln(out, "4096"); err != nil {
						return
					}
					for written := 0; written < runvalidation.MaxTextFieldBytes; written += len(chunk) {
						if _, err := out.Write(chunk); err != nil {
							return
						}
					}
					digest <- fmt.Sprintf("%x", hash.Sum(nil))
				}))
				defer server.Close()
				setStdinURLHTTPClientForTest(t, server.URL)
				req.Stdin, req.StdinURL = "", "http://payload.example/case"
				svc.shortCaseSampling = true
				svc.cpuNormalizer = testCPUNormalizer(t, 1, 1)
			}
			resp := svc.Run(context.Background(), req, Hooks{})
			b := resp.WorkMeasurementShadow
			if resp.Status != model.RunStatusAccepted || resp.WorkMeasurement != nil || resp.WorkMeasurementJudgment != nil || b == nil {
				t.Fatalf("primary verdict was not the ordinary result: %+v", resp)
			}
			if b.Agreement != tc.agreement || b.ShadowStatus != tc.shadow || b.PrimaryStatus != model.RunStatusAccepted || b.Response == nil {
				t.Fatalf("shadow comparison: %+v", b)
			}
			if tc.urlInput {
				if downloads.Load() != 1 || resp.CPUTimeSampling == nil || len(resp.CPUTimeSampling.Samples) != 3 {
					t.Fatalf("URL downloads=%d, primary sampling=%+v", downloads.Load(), resp.CPUTimeSampling)
				}
				if want := <-digest; b.StdinSHA256 != want {
					t.Fatalf("shared stdin SHA-256 = %q, want %q", b.StdinSHA256, want)
				}
			}
			if r := b.Response.WorkMeasurement; r == nil || !r.Complete || r.CPUIDProfile != durante.CPUIDProfileX8664V2 {
				t.Fatalf("shadow lacks a complete profiled observation: %+v", r)
			}
			if tc.latency && (b.Response.WorkMeasurement.LatencyBound == nil || b.Response.WorkMeasurementExecution.TimeEstimate.Method != durante.TimeMethodLatencyBoundREP) {
				t.Fatal("shadow latency estimate lacks the requested counter or method")
			}
			if err := validateRemoteShadow(req, resp, cfg, config.RuntimeTuningConfig{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
