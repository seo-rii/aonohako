package execute

import (
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// Keep the existing integration's deliberately synthetic coefficients and real
// installation/image metadata, adding only explicit runtime-option bindings.
func bindIntegrationRuntimeOptions(t *testing.T, cfg durante.Config, req *model.RunRequest) durante.Config {
	t.Helper()
	if cfg.TimePolicy != nil {
		policy := cfg.TimePolicy
		cfg.TimeModels = &durante.TimeModels{Models: []durante.TimeModel{{
			ModelID: policy.ModelID, ModelVersion: policy.ModelVersion, ReferenceID: policy.ReferenceID, Method: policy.Method,
			Coefficients: durante.TimeCoefficients{
				InstructionNSNumerator: policy.InstructionNSNumerator, REPByteNSNumerator: policy.REPByteNSNumerator,
				MOVSByteNSNumerator: policy.MOVSByteNSNumerator, STOSByteNSNumerator: policy.STOSByteNSNumerator, NSDenominator: policy.NSDenominator,
			},
		}}}
		cfg.TimePolicy = nil
	} else {
		cfg.TimeModels = cfg.TimeModels.Clone()
	}
	cfg.TimeModels.Schema, cfg.TimeModels.Profiles = 2, nil
	descriptions, err := DescribeWorkMeasurementRuntimeOptions(req, config.ExecutionConfig{WorkMeasurement: cfg})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, description := range descriptions {
		key := description.SourceLanguage + description.RuntimeOptionsID
		if seen[key] {
			continue
		}
		seen[key] = true
		family, _ := durante.FamilyForLanguage(description.SourceLanguage)
		options := description.RuntimeOptions
		cfg.TimeModels.Profiles = append(cfg.TimeModels.Profiles, durante.TimeProfileBinding{
			SourceLanguage: description.SourceLanguage, Family: family, RuntimeImage: cfg.RuntimeImage,
			EngineProfile: durante.EngineProfile, DispatchProfile: cfg.DispatchProfile,
			ModelID: cfg.TimeModels.Models[0].ModelID, ModelVersion: cfg.TimeModels.Models[0].ModelVersion,
			QualificationID: "synthetic-options-integration-only", RuntimeOptions: &options,
		})
	}
	if err := cfg.ValidateTimeConfiguration(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func assertIntegrationRuntimeOptions(t *testing.T, cfg durante.Config, req *model.RunRequest, estimates []durante.TimeEstimate) {
	t.Helper()
	descriptions, err := DescribeWorkMeasurementRuntimeOptions(req, config.ExecutionConfig{WorkMeasurement: cfg})
	if err != nil || len(descriptions) != len(estimates) {
		t.Fatalf("actual estimates do not cover runtime descriptors: %v", err)
	}
	for i, description := range descriptions {
		if estimates[i].RuntimeOptionsID != description.RuntimeOptionsID || estimates[i].RuntimeOptionsID == "" {
			t.Fatalf("actual %s %s estimate does not match preview: %+v", description.Role, description.ProgramID, estimates[i])
		}
	}
}
