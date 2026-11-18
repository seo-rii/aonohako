package config

import (
	"encoding/json"
	"strings"
	"testing"

	"aonohako/internal/durante"
)

func configureDuranteModelsLoadTest(t *testing.T) {
	t.Helper()
	configureDuranteTimeLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_TIME_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", "cpp,cpython")
	registry := durante.TimeModels{Schema: 1, Models: []durante.TimeModel{{
		ModelID: "common-config-test", ModelVersion: 1, ReferenceID: "synthetic-only",
		Coefficients: durante.TimeCoefficients{InstructionNSNumerator: 2, REPByteNSNumerator: 3, NSDenominator: 4},
	}}, Profiles: []durante.TimeProfileBinding{}}
	for _, source := range []string{"CPP20", "PYTHON3"} {
		family, _ := durante.FamilyForLanguage(source)
		registry.Profiles = append(registry.Profiles, durante.TimeProfileBinding{
			SourceLanguage: source, Family: family, RuntimeImage: duranteTimeImageForLoad,
			EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault,
			ModelID: "common-config-test", ModelVersion: 1, QualificationID: "synthetic-" + source,
		})
	}
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AONOHAKO_DURANTE_TIME_MODELS", string(raw))
}

func TestLoadDuranteSharedTimeModels(t *testing.T) {
	configureDuranteModelsLoadTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	measurement := cfg.Execution.WorkMeasurement
	if measurement.TimePolicy != nil || measurement.TimeModels == nil || len(measurement.TimeModels.Models) != 1 {
		t.Fatal("shared models were replaced with a single-language policy")
	}
	for _, source := range []string{"CPP20", "PYTHON3"} {
		policy, runtimeID, err := measurement.TimePolicyFor(source)
		if err != nil || policy.ModelID != "common-config-test" || policy.InstructionNSNumerator != 2 || runtimeID != source+"@"+duranteTimeImageForLoad {
			t.Fatalf("shared setting did not resolve %s: %+v %q %v", source, policy, runtimeID, err)
		}
	}
}

func TestLoadDuranteTimeModelsRejectConflictingOrIncompleteSettings(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"legacy also configured": {"AONOHAKO_DURANTE_TIME_POLICY": `{"model_id":"legacy","model_version":1,"reference_id":"test","runtime_id":"CPP20@` + duranteTimeImageForLoad + `","family":"cpp","engine_profile":"` + durante.EngineProfile + `","dispatch_profile":"default","instruction_ns_numerator":1,"rep_byte_ns_numerator":1,"ns_denominator":1}`},
		"no image":               {"AONOHAKO_DURANTE_RUNTIME_IMAGE": ""},
		"no resource policy":     {"AONOHAKO_DURANTE_RESOURCE_POLICY": ""},
		"null registry":          {"AONOHAKO_DURANTE_TIME_MODELS": "null"},
	} {
		t.Run(name, func(t *testing.T) {
			configureDuranteModelsLoadTest(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AONOHAKO_DURANTE_TIME_MODELS") {
				t.Fatalf("incompatible registry setting accepted or unrelated failure: %v", err)
			}
		})
	}
}
