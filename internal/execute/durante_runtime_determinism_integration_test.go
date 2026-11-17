package execute

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/util"
)

// TestDuranteRuntimeDeterminismIntegration checks, through the real sandbox
// and patched runtime, that measured executions receive exactly the pinned
// options: CPython's string hash equals the same interpreter's hash under
// PYTHONHASHSEED=0 with every other PYTHON* variable absent, and Node runs with
// --predictable. PyPy is unchanged; its hash must not vary between processes.
func TestDuranteRuntimeDeterminismIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("dedicated durante validation image required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("sandbox integration requires root")
	}
	forceDirectMode(t)
	cfg, err := durante.ParseConfig("cpython,pypy,javascript-wasm", os.Getenv("AONOHAKO_DURANTE_DRRUN"), os.Getenv("AONOHAKO_DURANTE_CLIENT"), durante.DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ResourcePolicy, err = durante.ParseResourcePolicy(`{"max_limits":{"cpu_time_ms":20000,"wall_time_ms":60000,"memory_mb":1024},"memory_budget_mb":4096,"reserved_memory_mb":512}`)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeDeterminism = durante.RuntimeDeterminismV1
	svc := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	// Compute the expectation with the interpreter the sandbox resolves.
	host := func(name string, env []string, args ...string) string {
		t.Helper()
		path, err := util.ResolveCommandPath(name, util.BaseEnv())
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(path, args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	pythonProbe := "import os, sys\n" +
		"print(hash('aonohako-durante'), sys.flags.hash_randomization, sys.flags.no_user_site, sys.flags.safe_path,\n" +
		"      sorted(k for k in os.environ if k.startswith('PYTHON')))\n"
	type probe struct {
		lang, name, source, expected, determinism string
	}
	probes := []probe{{
		"PYTHON3", "Main.py", pythonProbe,
		host("python3", []string{"PATH=/usr/bin:/bin", "PYTHONHASHSEED=0"}, "-c",
			"import sys; print(hash('aonohako-durante'), 0, 1, True, ['PYTHONHASHSEED'])"),
		durante.RuntimeDeterminismV1,
	}}
	// CI supplies these interpreters explicitly; a missing one is not a pass.
	if os.Getenv("AONOHAKO_TEST_DURANTE_PYPY") == "1" {
		probes = append(probes, probe{"PYPY3", "Main.py", "print(hash('aonohako-durante'))",
			host("pypy3", []string{"PATH=/usr/bin:/bin"}, "-c", "print(hash('aonohako-durante'))"), ""})
	}
	if os.Getenv("AONOHAKO_TEST_DURANTE_NODE") == "1" {
		probes = append(probes, probe{"JAVASCRIPT", "main.js", "console.log(process.execArgv.includes('--predictable'))", "true", durante.RuntimeDeterminismV1})
	}
	for _, p := range probes {
		t.Run(p.lang, func(t *testing.T) {
			var counts []uint64
			for range 2 {
				hard := durante.ResourceLimits{CPUTimeMs: 20000, WallTimeMs: 60000, MemoryMB: 1024}
				req := &model.RunRequest{Lang: p.lang, WorkMeasurement: true, IgnoreTLE: true,
					Binaries:       []model.Binary{{Name: p.name, DataB64: base64.StdEncoding.EncodeToString([]byte(p.source)), Mode: "text"}},
					ExpectedStdout: p.expected, Limits: model.Limits{TimeMs: 1, WallTimeMs: 1, MemoryMB: 16}, WorkMeasurementLimits: &hard}
				r := svc.Run(context.Background(), req, Hooks{})
				d, report := r.WorkMeasurementExecution, r.WorkMeasurement
				if d == nil || d.State != "complete" || d.OutputMatches == nil || !*d.OutputMatches {
					t.Fatalf("%s did not observe the pinned runtime (want %q): %+v metadata=%+v", p.lang, p.expected, r, d)
				}
				if report == nil || !report.Complete || report.RuntimeDeterminism != p.determinism || report.Instructions == nil {
					t.Fatalf("%s receipt %+v", p.lang, report)
				}
				counts = append(counts, *report.Instructions)
			}
			// ASLR remains enabled in the sandbox, so counts are reported, not asserted.
			t.Logf("%s instructions %v", p.lang, counts)
		})
	}
}
