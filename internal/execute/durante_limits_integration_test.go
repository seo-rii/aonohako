package execute

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/timing"
)

func TestDuranteResourceEnvelopeIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("dedicated durante validation image required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("sandbox integration requires root")
	}
	forceDirectMode(t)
	cfg, err := durante.ParseConfig("cpp", os.Getenv("AONOHAKO_DURANTE_DRRUN"), os.Getenv("AONOHAKO_DURANTE_CLIENT"), os.Getenv("AONOHAKO_DURANTE_DISPATCH_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ResourcePolicy, err = durante.ParseResourcePolicy(`{"max_limits":{"cpu_time_ms":5000,"wall_time_ms":20000,"memory_mb":512},"memory_budget_mb":4096,"reserved_memory_mb":512}`)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(os.Getenv("AONOHAKO_DURANTE_TEST_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	svc.cpuNormalizer, err = timing.NewCPUNormalizer("test", 60000000, 120000000)
	if err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, input, expected string, hard durante.ResourceLimits) model.RunResponse {
		req := &model.RunRequest{Lang: "CPP20", WorkMeasurement: true, IgnoreTLE: true, Binaries: []model.Binary{{Name: "Main", DataB64: base64.StdEncoding.EncodeToString(data), Mode: "exec"}}, Stdin: input, ExpectedStdout: expected, Limits: model.Limits{TimeMs: 1, WallTimeMs: 1, MemoryMB: 16}, WorkMeasurementLimits: &hard}
		return svc.Run(ctx, req, Hooks{})
	}
	hard := durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 10000, MemoryMB: 128}
	for _, tc := range []struct{ name, input, expected string }{{"beyond nominal CPU", "normal 10000", "49995000"}, {"beyond nominal RSS", "memory 48", "allocated"}} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(context.Background(), tc.input, tc.expected, hard)
			d := r.WorkMeasurementExecution
			if r.Status != model.RunStatusInitFail || d == nil || d.State != "complete" || d.OutputMatches == nil || !*d.OutputMatches || r.CPUTimeNormalization != nil || r.Score != nil || r.CPUTimeNs <= 1000000 {
				t.Fatalf("diagnostic failed: %+v; metadata=%+v", r, d)
			}
			if tc.input == "memory 48" && r.MemoryKB <= 16*1024 {
				t.Fatalf("RSS not captured: %d", r.MemoryKB)
			}
		})
	}
	for _, tc := range []struct {
		name, input, source string
		limits              durante.ResourceLimits
	}{
		{"CPU", "burn", "cpu", durante.ResourceLimits{CPUTimeMs: 100, WallTimeMs: 10000, MemoryMB: 128}},
		{"wall", "sleep", "wall", durante.ResourceLimits{CPUTimeMs: 400, WallTimeMs: 400, MemoryMB: 128}},
		{"RSS", "memory 128", "memory", durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 10000, MemoryMB: 32}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := run(context.Background(), tc.input, "", tc.limits)
			d := r.WorkMeasurementExecution
			if r.Status != model.RunStatusInitFail || d == nil || d.State != "resource_exhausted" || d.OutputMatches != nil || r.Score != nil || !strings.Contains(d.TerminationSource, tc.source) {
				t.Fatalf("hard stop failed: %+v; metadata=%+v", r, d)
			}
			if r.WorkMeasurement != nil && r.WorkMeasurement.Instructions != nil {
				t.Fatal("forced stop produced usable work total")
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(200*time.Millisecond, cancel)
		defer timer.Stop()
		r := run(ctx, "burn", "", hard)
		if r.Status != model.RunStatusInitFail || r.WorkMeasurementExecution == nil || r.WorkMeasurementExecution.State != "cancelled" || r.Score != nil {
			t.Fatalf("cancel mapped to judgement: %+v", r)
		}
	})
}

// The same interpreter binary, sandbox and envelope protect Python programs;
// no instrumented interpreter build or alternate score protocol is involved.
func TestDurantePythonResourceEnvelopeIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("dedicated durante validation image required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("sandbox integration requires root")
	}
	forceDirectMode(t)
	cfg, err := durante.ParseConfig("cpython,pypy", os.Getenv("AONOHAKO_DURANTE_DRRUN"), os.Getenv("AONOHAKO_DURANTE_CLIENT"), durante.DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ResourcePolicy, err = durante.ParseResourcePolicy(`{"max_limits":{"cpu_time_ms":10000,"wall_time_ms":30000,"memory_mb":1024},"memory_budget_mb":4096,"reserved_memory_mb":512}`)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	languages := []string{"PYTHON3"}
	// CI supplies PyPy explicitly; a missing interpreter is not a passing receipt.
	if os.Getenv("AONOHAKO_TEST_DURANTE_PYPY") == "1" {
		languages = append(languages, "PYPY3")
	}
	for _, language := range languages {
		t.Run(language, func(t *testing.T) {
			run := func(ctx context.Context, source, expected string, hard durante.ResourceLimits) model.RunResponse {
				req := &model.RunRequest{Lang: language, WorkMeasurement: true, IgnoreTLE: true, Binaries: []model.Binary{{Name: "Main.py", DataB64: base64.StdEncoding.EncodeToString([]byte(source)), Mode: "text"}}, ExpectedStdout: expected, Limits: model.Limits{TimeMs: 1, WallTimeMs: 1, MemoryMB: 16}, WorkMeasurementLimits: &hard}
				return svc.Run(ctx, req, Hooks{})
			}
			hard := durante.ResourceLimits{CPUTimeMs: 10000, WallTimeMs: 30000, MemoryMB: 512}
			for _, tc := range []struct{ name, source, expected string }{
				{"startup", "print(42)", "42"},
				{"hashlib", "import hashlib\nprint(hashlib.sha256(b'abc').hexdigest())", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
				{"zlib", "import zlib\nprint(len(zlib.decompress(zlib.compress(b'a'*100000))))", "100000"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := run(context.Background(), tc.source, tc.expected, hard)
					if r.Status != model.RunStatusInitFail || r.WorkMeasurementExecution == nil || r.WorkMeasurementExecution.State != "complete" || r.WorkMeasurementExecution.OutputMatches == nil || !*r.WorkMeasurementExecution.OutputMatches || r.Score != nil || r.CPUTimeNormalization != nil {
						t.Fatalf("interpreter diagnostic: %+v metadata=%+v", r, r.WorkMeasurementExecution)
					}
					if err := validateRemoteWorkMeasurement(&model.RunRequest{Lang: language, Limits: model.Limits{TimeMs: 1, WallTimeMs: 1, MemoryMB: 16}, WorkMeasurementLimits: &hard}, r); err != nil {
						t.Fatal(err)
					}
				})
			}
			for _, tc := range []struct {
				name, source, expectedSource string
				limits                       durante.ResourceLimits
			}{
				{"cpu", "while True: pass", "cpu", durante.ResourceLimits{CPUTimeMs: 1000, WallTimeMs: 10000, MemoryMB: 512}},
				{"wall", "import time\ntime.sleep(30)", "wall", durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 3000, MemoryMB: 512}},
				{"rss", "a=bytearray(512*1024*1024)\nprint(len(a))", "memory", durante.ResourceLimits{CPUTimeMs: 10000, WallTimeMs: 30000, MemoryMB: 96}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := run(context.Background(), tc.source, "", tc.limits)
					d := r.WorkMeasurementExecution
					if r.Status != model.RunStatusInitFail || d == nil || d.State != "resource_exhausted" || d.OutputMatches != nil || r.Score != nil || !strings.Contains(d.TerminationSource, tc.expectedSource) {
						t.Fatalf("interpreter hard stop: %+v metadata=%+v", r, d)
					}
					if r.WorkMeasurement != nil && r.WorkMeasurement.Instructions != nil {
						t.Fatal("forced stop produced usable total")
					}
				})
			}
			t.Run("cancel", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				timer := time.AfterFunc(500*time.Millisecond, cancel)
				defer timer.Stop()
				r := run(ctx, "while True: pass", "", hard)
				if r.Status != model.RunStatusInitFail || r.WorkMeasurementExecution == nil || r.WorkMeasurementExecution.State != "cancelled" || r.Score != nil {
					t.Fatalf("interpreter cancellation: %+v", r)
				}
			})
		})
	}
}
