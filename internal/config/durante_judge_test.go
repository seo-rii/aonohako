package config

import (
	"encoding/json"
	"testing"

	"aonohako/internal/durante"
)

// configureDuranteProfiledTimeLoadTest binds the synthetic time policy to the
// same virtual processor as the configuration, as judge mode requires.
func configureDuranteProfiledTimeLoadTest(t *testing.T, profile string) {
	t.Helper()
	configureDuranteTimeLoadTest(t)
	policy := durante.TimePolicy{
		ModelID: "configuration-test", ModelVersion: 1, ReferenceID: "synthetic-test-only",
		RuntimeID: "CPP20@" + duranteTimeImageForLoad, Family: "cpp",
		EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault,
		CPUIDProfile:           profile,
		InstructionNSNumerator: 2, REPByteNSNumerator: 3, NSDenominator: 4,
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AONOHAKO_DURANTE_TIME_POLICY", string(raw))
	t.Setenv("AONOHAKO_DURANTE_CPUID_PROFILE", profile)
}

func TestLoadDuranteCPUIDProfile(t *testing.T) {
	configureDuranteProfiledTimeLoadTest(t, "x86-64-v2")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	measurement := cfg.Execution.WorkMeasurement
	policy, _, err := measurement.TimePolicyFor("CPP20")
	if err != nil || measurement.CPUIDProfile != "x86-64-v2" || policy.CPUIDProfile != "x86-64-v2" {
		t.Fatalf("CPUID profile was not preserved: %+v %+v %v", measurement, policy, err)
	}
	// A policy qualified with host CPUID cannot apply to profiled counts.
	configureDuranteTimeLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_CPUID_PROFILE", "x86-64-v2")
	if _, err := Load(); err == nil {
		t.Fatal("accepted a host-CPUID time policy for a profiled configuration")
	}
	t.Setenv("AONOHAKO_DURANTE_CPUID_PROFILE", "x86-64-v3")
	if _, err := Load(); err == nil {
		t.Fatal("accepted an unknown CPUID profile")
	}
	configureDuranteResourceLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_RESOURCE_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", "")
	t.Setenv("AONOHAKO_DURANTE_TIME_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_CPUID_PROFILE", "x86-64-v1")
	if _, err := Load(); err == nil {
		t.Fatal("accepted a CPUID profile without an enabled measurement family")
	}
}

func TestLoadDuranteJudgeMode(t *testing.T) {
	configureDuranteProfiledTimeLoadTest(t, "x86-64-v2")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "on")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "cpp")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_INTEGRITY", "plausibility")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_INTEGRITY_RATIO", "60")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	judge := cfg.Execution.WorkMeasurement.Judge
	if !judge.Enabled || !judge.JudgesFamily("cpp") || judge.Integrity != durante.JudgeIntegrityPlausibility || judge.IntegrityRatio != 60 {
		t.Fatalf("judge configuration was not preserved: %+v", judge)
	}
}

func TestLoadDuranteJudgeModeDefaultsOff(t *testing.T) {
	configureDuranteTimeLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_INTEGRITY", "")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_INTEGRITY_RATIO", "")
	cfg, err := Load()
	if err != nil || cfg.Execution.WorkMeasurement.Judge.Enabled {
		t.Fatalf("judge mode must default off: %+v %v", cfg.Execution.WorkMeasurement.Judge, err)
	}
}

func TestLoadDuranteJudgeModeRequiresCPUIDProfile(t *testing.T) {
	configureDuranteTimeLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_CPUID_PROFILE", "")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "on")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "cpp")
	if _, err := Load(); err == nil {
		t.Fatal("expected rejection when judge mode has no CPUID profile")
	}
}

func TestLoadDuranteJudgeModeRejectsUnenabledFamily(t *testing.T) {
	configureDuranteProfiledTimeLoadTest(t, "x86-64-v2")
	// cpython is a registered family but not enabled by AONOHAKO_DURANTE_FAMILIES.
	t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "on")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "cpython")
	if _, err := Load(); err == nil {
		t.Fatal("expected rejection of a judged family that is not an enabled measurement family")
	}
}

func TestLoadDuranteJudgeModeRequiresTimeModel(t *testing.T) {
	configureDuranteResourceLoadTest(t) // resource policy but no time model
	t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "on")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "cpp")
	if _, err := Load(); err == nil {
		t.Fatal("expected rejection when judge mode has no time model")
	}
}

func TestLoadDuranteRuntimeDeterminism(t *testing.T) {
	configureDuranteProfiledTimeLoadTest(t, "x86-64-v2")
	for raw, want := range map[string]string{"": "", "off": "", "v1": durante.RuntimeDeterminismV1} {
		t.Setenv("AONOHAKO_DURANTE_RUNTIME_DETERMINISM", raw)
		cfg, err := Load()
		if err != nil || cfg.Execution.WorkMeasurement.RuntimeDeterminism != want {
			t.Fatalf("%q loaded as %+v: %v", raw, cfg.Execution.WorkMeasurement.RuntimeDeterminism, err)
		}
	}
	t.Setenv("AONOHAKO_DURANTE_RUNTIME_DETERMINISM", "v2")
	if _, err := Load(); err == nil {
		t.Fatal("accepted an unknown runtime determinism")
	}
	// Judging CPython requires the pinned options.
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", "cpp,cpython")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "on")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "cpython")
	t.Setenv("AONOHAKO_DURANTE_RUNTIME_DETERMINISM", "")
	if _, err := Load(); err == nil {
		t.Fatal("judged CPython without runtime determinism")
	}
	t.Setenv("AONOHAKO_DURANTE_RUNTIME_DETERMINISM", "v1")
	if _, err := Load(); err != nil {
		t.Fatalf("pinned CPython judge rejected: %v", err)
	}
	configureDuranteResourceLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_JUDGE_MODE", "")
	t.Setenv("AONOHAKO_DURANTE_JUDGE_FAMILIES", "")
	t.Setenv("AONOHAKO_DURANTE_RESOURCE_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", "")
	t.Setenv("AONOHAKO_DURANTE_TIME_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_CPUID_PROFILE", "")
	t.Setenv("AONOHAKO_DURANTE_RUNTIME_DETERMINISM", "v1")
	if _, err := Load(); err == nil {
		t.Fatal("accepted runtime determinism without an enabled measurement family")
	}
}
