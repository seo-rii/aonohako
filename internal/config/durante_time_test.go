package config

import (
	"encoding/json"
	"strings"
	"testing"

	"aonohako/internal/durante"
)

const duranteTimeImageForLoad = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func configureDuranteTimeLoadTest(t *testing.T) {
	t.Helper()
	configureDuranteResourceLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_TIME_MODELS", "")
	// These weights exercise configuration only; they are not calibrated.
	policy := durante.TimePolicy{
		ModelID: "configuration-test", ModelVersion: 1, ReferenceID: "synthetic-test-only",
		RuntimeID: "CPP20@" + duranteTimeImageForLoad, Family: "cpp",
		EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault,
		InstructionNSNumerator: 2, REPByteNSNumerator: 3, NSDenominator: 4,
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AONOHAKO_DURANTE_TIME_POLICY", string(raw))
	t.Setenv("AONOHAKO_DURANTE_RUNTIME_IMAGE", duranteTimeImageForLoad)
}

func TestLoadDuranteTimePolicy(t *testing.T) {
	configureDuranteTimeLoadTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	measurement := cfg.Execution.WorkMeasurement
	policy, runtimeID, err := measurement.TimePolicyFor("CPP20")
	if err != nil || policy.ModelID != "configuration-test" || policy.InstructionNSNumerator != 2 || policy.REPByteNSNumerator != 3 || policy.NSDenominator != 4 || runtimeID != "CPP20@"+duranteTimeImageForLoad || measurement.RuntimeImage != duranteTimeImageForLoad {
		t.Fatalf("time configuration was not preserved: %+v, %q, %v", measurement, runtimeID, err)
	}
}

func TestLoadDuranteTimePolicyIsOptIn(t *testing.T) {
	configureDuranteTimeLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_TIME_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_RUNTIME_IMAGE", "")
	t.Setenv("AONOHAKO_DURANTE_RESOURCE_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", "")
	for _, image := range []string{"", duranteTimeImageForLoad} {
		t.Setenv("AONOHAKO_DURANTE_RUNTIME_IMAGE", image)
		cfg, err := Load()
		if err != nil || cfg.Execution.WorkMeasurement.TimePolicy != nil || cfg.Execution.WorkMeasurement.TimeModels != nil || cfg.Execution.WorkMeasurement.RuntimeImage != image {
			t.Fatalf("disabled time configuration changed: %+v, %v", cfg.Execution.WorkMeasurement, err)
		}
	}
}

func TestLoadDuranteTimePolicyRejectsIncompatibleConfiguration(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"resource policy absent": {"AONOHAKO_DURANTE_RESOURCE_POLICY": ""},
		"C++ family disabled":    {"AONOHAKO_DURANTE_FAMILIES": "cpython"},
		"dispatch mismatch":      {"AONOHAKO_DURANTE_DISPATCH_PROFILE": durante.DispatchGLIBC241},
		"image absent":           {"AONOHAKO_DURANTE_RUNTIME_IMAGE": ""},
		"image mutable":          {"AONOHAKO_DURANTE_RUNTIME_IMAGE": "runner:latest"},
		"image mismatch":         {"AONOHAKO_DURANTE_RUNTIME_IMAGE": "sha256:" + strings.Repeat("f", 64)},
		"invalid JSON":           {"AONOHAKO_DURANTE_TIME_POLICY": "null"},
		"missing fields":         {"AONOHAKO_DURANTE_TIME_POLICY": `{"model_id":"configuration-test"}`},
		"invalid disabled image": {"AONOHAKO_DURANTE_TIME_POLICY": "", "AONOHAKO_DURANTE_RUNTIME_IMAGE": "runner:latest"},
	} {
		t.Run(name, func(t *testing.T) {
			configureDuranteTimeLoadTest(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AONOHAKO_DURANTE_TIME_POLICY") {
				t.Fatalf("incompatible configuration accepted or unrelated error: %v", err)
			}
		})
	}
}
