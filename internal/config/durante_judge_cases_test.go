package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"aonohako/internal/durante"
)

func TestLoadDuranteJudgeCases(t *testing.T) {
	const env = "AONOHAKO_DURANTE_JUDGE_CASES"
	const stdin = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	configureDuranteModelsLoadTest(t)
	t.Setenv(env, "")
	if err := os.Unsetenv(env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "on")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "cpp")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_INTEGRITY", "off")
	t.Setenv("AONOHAKO_DURANTE_CPUID_PROFILE", "x86-64-v2")
	opts := durante.RuntimeOptions{Version: 1, Argv: []string{"/__aonohako_program__"}, Env: []string{"PATH=/usr/bin"}, MemoryMB: 512}
	registry, err := durante.ParseTimeModels(os.Getenv("AONOHAKO_DURANTE_TIME_MODELS"))
	if err != nil {
		t.Fatal(err)
	}
	registry.Schema, registry.Profiles = 2, registry.Profiles[:1]
	registry.Profiles[0].CPUIDProfile = "x86-64-v2"
	registry.Profiles[0].RuntimeOptions = &opts
	setModels := func(t *testing.T, models *durante.TimeModels) {
		t.Helper()
		raw, err := json.Marshal(models)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("AONOHAKO_DURANTE_TIME_MODELS", string(raw))
	}
	setModels(t, registry)
	cfg, err := Load()
	if err != nil || cfg.Execution.WorkMeasurement.JudgeCases != nil {
		t.Fatalf("unset case scope should preserve legacy configuration: %v", err)
	}
	policy, _, err := cfg.Execution.WorkMeasurement.TimePolicyForOptions("CPP20", opts)
	if err != nil {
		t.Fatal(err)
	}
	binding := (durante.WorkClockRequest{TimePolicy: &policy}).ModelBinding()
	raw := `{"schema":1,"cases":[{"problem_id":"contest-1/a","source_language":"CPP20","stdin_sha256":"` + stdin + `","time_ms":50,"model_binding":"` + binding + `"}]}`
	t.Setenv(env, raw)
	for _, mode := range []string{"on", "shadow"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", mode)
			cfg, err := Load()
			if err != nil || !cfg.Execution.WorkMeasurement.JudgeCases.Allows("contest-1/a", "CPP20", stdin, 50, binding) {
				t.Fatalf("case was not admitted with %s: %v", mode, err)
			}
		})
	}
	for name, edit := range map[string]func(*testing.T){
		"empty setting": func(t *testing.T) { t.Setenv(env, "") },
		"judge off":     func(t *testing.T) { t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "off") },
		"cpp judging disabled": func(t *testing.T) {
			t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "cpython")
			t.Setenv("AONOHAKO_DURANTE_RUNTIME_DETERMINISM", durante.RuntimeDeterminismV1)
		},
		"plausibility": func(t *testing.T) {
			t.Setenv("AONOHAKO_DURANTE_JUDGE_INTEGRITY", "plausibility")
			t.Setenv("AONOHAKO_DURANTE_JUDGE_INTEGRITY_RATIO", "60")
		},
		"stale binding":        func(t *testing.T) { t.Setenv(env, strings.Replace(raw, binding, "work-clock-v1:sha256:"+stdin, 1)) },
		"other runtime":        func(t *testing.T) { t.Setenv("AONOHAKO_DURANTE_RUNTIME_IMAGE", "sha256:"+strings.Repeat("b", 64)) },
		"no watchdog headroom": func(t *testing.T) { t.Setenv(env, strings.Replace(raw, ":50", ":4000", 1)) },
		"legacy registry": func(t *testing.T) {
			m := registry.Clone()
			m.Schema, m.Profiles[0].RuntimeOptions = 1, nil
			setModels(t, m)
		},
		"changed runtime options": func(t *testing.T) {
			m := registry.Clone()
			m.Profiles[0].RuntimeOptions.MemoryMB--
			setModels(t, m)
		},
		"terminal only formula": func(t *testing.T) {
			m := registry.Clone()
			m.Models[0].Method = durante.TimeMethodLatencyBoundREP
			m.Models[0].Coefficients = durante.TimeCoefficients{LatencyX16NSNumerator: 2, REPByteNSNumerator: 3, NSDenominator: 4}
			t.Setenv("AONOHAKO_DURANTE_LATENCY_BOUND", "v1")
			setModels(t, m)
			// Bind the *correct* terminal model: its receipt still cannot be
			// used to drive an online deadline.
			measurement := cfg.Execution.WorkMeasurement
			measurement.TimeModels, measurement.LatencyBound = m, true
			policy, _, err := measurement.TimePolicyForOptions("CPP20", opts)
			if err != nil {
				t.Fatal(err)
			}
			terminal := (durante.WorkClockRequest{TimePolicy: &policy}).ModelBinding()
			t.Setenv(env, strings.Replace(raw, binding, terminal, 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			edit(t)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), env) {
				t.Fatalf("expected case-registry validation error, got %v", err)
			}
		})
	}
}
