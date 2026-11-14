package config

import (
	"strings"
	"testing"
)

const durantePolicyForLoad = `{"max_limits":{"cpu_time_ms":4000,"wall_time_ms":8000,"memory_mb":512},"memory_budget_mb":1280,"reserved_memory_mb":128}`

func configureDuranteResourceLoadTest(t *testing.T) {
	t.Helper()
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", "cpp")
	t.Setenv("AONOHAKO_DURANTE_RESOURCE_POLICY", durantePolicyForLoad)
	t.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "2")
	t.Setenv("AONOHAKO_WORK_ROOT_MAX_BYTES", "134217728")
}

func TestLoadDuranteResourcePolicy(t *testing.T) {
	configureDuranteResourceLoadTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.Execution.WorkMeasurement.ResourcePolicy
	if policy == nil || policy.MaxLimits.CPUTimeMs != 4000 || policy.MaxLimits.WallTimeMs != 8000 || policy.MaxLimits.MemoryMB != 512 || policy.MemoryBudgetMB != 1280 || policy.ReservedMemoryMB != 128 {
		t.Fatalf("policy was not preserved: %+v", policy)
	}
	t.Setenv("AONOHAKO_DURANTE_RESOURCE_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", "")
	cfg, err = Load()
	if err != nil || cfg.Execution.WorkMeasurement.ResourcePolicy != nil {
		t.Fatalf("legacy configuration changed: %+v %v", cfg.Execution.WorkMeasurement.ResourcePolicy, err)
	}
}

func TestLoadDuranteResourcePolicyRejectsUnsafeConfiguration(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"family disabled":    {"AONOHAKO_DURANTE_FAMILIES": ""},
		"too many runs":      {"AONOHAKO_MAX_ACTIVE_RUNS": "3"},
		"work root rounding": {"AONOHAKO_WORK_ROOT_MAX_BYTES": "134217729"},
		"invalid JSON":       {"AONOHAKO_DURANTE_RESOURCE_POLICY": "null"},
		"missing reserve":    {"AONOHAKO_DURANTE_RESOURCE_POLICY": strings.Replace(durantePolicyForLoad, `,"reserved_memory_mb":128`, "", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			configureDuranteResourceLoadTest(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AONOHAKO_DURANTE_RESOURCE_POLICY") {
				t.Fatalf("unsafe configuration accepted or unrelated error: %v", err)
			}
		})
	}
}
