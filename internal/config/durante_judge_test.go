package config

import (
	"testing"

	"aonohako/internal/durante"
)

func TestLoadDuranteJudgeMode(t *testing.T) {
	configureDuranteTimeLoadTest(t)
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

func TestLoadDuranteJudgeModeRejectsUnenabledFamily(t *testing.T) {
	configureDuranteTimeLoadTest(t)
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
