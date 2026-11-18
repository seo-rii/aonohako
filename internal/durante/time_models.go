package durante

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"aonohako/internal/profiles"
)

const (
	maxTimeModelsBytes = 64 << 10
	maxTimeModels      = 32
	maxTimeProfiles    = 128
)

// TimeCoefficients describes the common conversion independently of its approved
// execution profiles. It has the same arithmetic and units as TimePolicy.
type TimeCoefficients struct {
	InstructionNSNumerator uint64 `json:"instruction_ns_numerator"`
	REPByteNSNumerator     uint64 `json:"rep_byte_ns_numerator,omitempty"`
	MOVSByteNSNumerator    uint64 `json:"movs_byte_ns_numerator,omitempty"`
	STOSByteNSNumerator    uint64 `json:"stos_byte_ns_numerator,omitempty"`
	NSDenominator          uint64 `json:"ns_denominator"`
}

type TimeModel struct {
	ModelID      string           `json:"model_id"`
	ModelVersion uint64           `json:"model_version"`
	ReferenceID  string           `json:"reference_id"`
	Method       string           `json:"method,omitempty"`
	Coefficients TimeCoefficients `json:"coefficients"`
}

// TimeProfileBinding is an operator's explicit application of a shared model.
// QualificationID identifies the operator's evidence; its presence is not a
// verification of that evidence. Family registration never creates a binding.
type TimeProfileBinding struct {
	SourceLanguage  string          `json:"source_language"`
	Family          string          `json:"family"`
	RuntimeImage    string          `json:"runtime_image"`
	EngineProfile   string          `json:"engine_profile"`
	DispatchProfile string          `json:"dispatch_profile"`
	ModelID         string          `json:"model_id"`
	ModelVersion    uint64          `json:"model_version"`
	QualificationID string          `json:"qualification_id"`
	RuntimeOptions  *RuntimeOptions `json:"runtime_options,omitempty"`
	// CPUIDProfile is the virtual processor the qualification evidence used.
	// Empty means host CPUID; it matches only an unprofiled configuration.
	CPUIDProfile string `json:"cpuid_profile,omitempty"`
}

// TimeModels may contain bindings for more than one deployment. The executing
// image is supplied independently through Config.RuntimeImage and matched at
// selection time. An empty profile list enables no source-language profile.
type TimeModels struct {
	Schema   uint64               `json:"schema"`
	Models   []TimeModel          `json:"models"`
	Profiles []TimeProfileBinding `json:"profiles"`
}

// The explicit field decoder is shared by the nested registry objects so JSON
// aliases, duplicates, nulls and omitted fields cannot change model selection.
func decodeTimeModelFields(data []byte, kind string, targets map[string]any, optional ...string) error {
	fields, err := manifestObject(data)
	if err != nil {
		return fmt.Errorf("durante %s: %w", kind, err)
	}
	for key := range fields {
		if _, ok := targets[key]; !ok {
			return fmt.Errorf("durante %s has unknown field %q", kind, key)
		}
	}
	for key, target := range targets {
		raw, ok := fields[key]
		if !ok && slices.Contains(optional, key) {
			continue
		}
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("durante %s requires field %q", kind, key)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("durante %s field %q: %w", kind, key, err)
		}
	}
	return nil
}

func (c *TimeCoefficients) UnmarshalJSON(data []byte) error {
	var decoded TimeCoefficients
	if err := decodeTimeModelFields(data, "time coefficients", map[string]any{
		"instruction_ns_numerator": &decoded.InstructionNSNumerator,
		"rep_byte_ns_numerator":    &decoded.REPByteNSNumerator,
		"movs_byte_ns_numerator":   &decoded.MOVSByteNSNumerator,
		"stos_byte_ns_numerator":   &decoded.STOSByteNSNumerator,
		"ns_denominator":           &decoded.NSDenominator,
	}, "rep_byte_ns_numerator", "movs_byte_ns_numerator", "stos_byte_ns_numerator"); err != nil {
		return err
	}
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	_, aggregate := fields["rep_byte_ns_numerator"]
	_, movs := fields["movs_byte_ns_numerator"]
	_, stos := fields["stos_byte_ns_numerator"]
	if (aggregate && (movs || stos)) || (!aggregate && (!movs || !stos)) {
		return fmt.Errorf("durante time coefficients requires exactly one complete REP formula")
	}
	*c = decoded
	return nil
}

func (m *TimeModel) UnmarshalJSON(data []byte) error {
	var decoded TimeModel
	if err := decodeTimeModelFields(data, "time model", map[string]any{
		"model_id": &decoded.ModelID, "model_version": &decoded.ModelVersion,
		"reference_id": &decoded.ReferenceID, "coefficients": &decoded.Coefficients,
		"method": &decoded.Method,
	}, "method"); err != nil {
		return err
	}
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	if _, present := fields["method"]; present && decoded.Method == "" {
		return fmt.Errorf("durante time model method must not be empty")
	}
	*m = decoded
	return nil
}

func (p *TimeProfileBinding) UnmarshalJSON(data []byte) error {
	var decoded TimeProfileBinding
	if err := decodeTimeModelFields(data, "time profile", map[string]any{
		"source_language": &decoded.SourceLanguage, "family": &decoded.Family,
		"runtime_image": &decoded.RuntimeImage, "engine_profile": &decoded.EngineProfile,
		"dispatch_profile": &decoded.DispatchProfile, "model_id": &decoded.ModelID,
		"model_version": &decoded.ModelVersion, "qualification_id": &decoded.QualificationID,
		"runtime_options": &decoded.RuntimeOptions,
		"cpuid_profile":   &decoded.CPUIDProfile,
	}, "runtime_options", "cpuid_profile"); err != nil {
		return err
	}
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	if _, present := fields["cpuid_profile"]; present && !ValidCPUIDProfile(decoded.CPUIDProfile) {
		return fmt.Errorf("durante time profile CPUID profile must be a supported profile")
	}
	*p = decoded
	return nil
}

func (r *TimeModels) UnmarshalJSON(data []byte) error {
	var decoded TimeModels
	if err := decodeTimeModelFields(data, "time models", map[string]any{
		"schema": &decoded.Schema, "models": &decoded.Models, "profiles": &decoded.Profiles,
	}); err != nil {
		return err
	}
	*r = decoded
	return nil
}

func ParseTimeModels(raw string) (*TimeModels, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxTimeModelsBytes || !utf8.ValidString(raw) {
		return nil, fmt.Errorf("invalid durante time models encoding or size")
	}
	var registry TimeModels
	if err := json.Unmarshal([]byte(raw), &registry); err != nil {
		return nil, err
	}
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	return &registry, nil
}

type timeModelKey struct {
	id      string
	version uint64
}

func validTimeModelID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00')
}

func (m TimeModel) resolve(binding TimeProfileBinding) TimePolicy {
	optionsID := ""
	if binding.RuntimeOptions != nil {
		optionsID, _ = binding.RuntimeOptions.ID() // Validate checks descriptors before selection.
	}
	return TimePolicy{
		runtimeOptionsID: optionsID,
		Method:           m.Method,
		ModelID:          m.ModelID, ModelVersion: m.ModelVersion, ReferenceID: m.ReferenceID,
		RuntimeID: binding.SourceLanguage + "@" + binding.RuntimeImage,
		Family:    binding.Family, EngineProfile: binding.EngineProfile, DispatchProfile: binding.DispatchProfile,
		CPUIDProfile:           binding.CPUIDProfile,
		InstructionNSNumerator: m.Coefficients.InstructionNSNumerator,
		REPByteNSNumerator:     m.Coefficients.REPByteNSNumerator, NSDenominator: m.Coefficients.NSDenominator,
		MOVSByteNSNumerator: m.Coefficients.MOVSByteNSNumerator, STOSByteNSNumerator: m.Coefficients.STOSByteNSNumerator,
	}
}

func (r TimeModels) Validate() error {
	if (r.Schema != 1 && r.Schema != 2) || len(r.Models) == 0 || len(r.Models) > maxTimeModels || len(r.Profiles) > maxTimeProfiles {
		return fmt.Errorf("durante time models requires schema 1 or 2, 1..%d models and at most %d profiles", maxTimeModels, maxTimeProfiles)
	}
	models := make(map[timeModelKey]TimeModel, len(r.Models))
	for _, model := range r.Models {
		if !validTimeModelID(model.ModelID) || !validTimeModelID(model.ReferenceID) || model.ModelVersion == 0 {
			return fmt.Errorf("durante time model requires explicit model, version and reference identities")
		}
		if err := model.resolve(TimeProfileBinding{}).validateWeights(); err != nil {
			return fmt.Errorf("durante time model: %w", err)
		}
		key := timeModelKey{model.ModelID, model.ModelVersion}
		if _, exists := models[key]; exists {
			return fmt.Errorf("duplicate durante time model %q version %d", key.id, key.version)
		}
		models[key] = model
	}
	type profileKey struct{ runtimeID, engine, dispatch, options, cpuid string }
	seen := make(map[profileKey]bool, len(r.Profiles))
	for _, binding := range r.Profiles {
		profile, ok := profiles.Resolve(binding.SourceLanguage)
		if !ok || profile.SourceLang != binding.SourceLanguage {
			return fmt.Errorf("durante time profile requires a canonical source-language ID")
		}
		family, ok := FamilyForLanguage(profile.SourceLang)
		if !ok || family != binding.Family {
			return fmt.Errorf("durante time profile family does not match its source-language ID")
		}
		runtimeID, err := TimeRuntimeID(binding.SourceLanguage, binding.RuntimeImage)
		if err != nil {
			return err
		}
		if !validTimeModelID(binding.QualificationID) {
			return fmt.Errorf("durante time profile requires an explicit qualification ID")
		}
		model, ok := models[timeModelKey{binding.ModelID, binding.ModelVersion}]
		if !ok {
			return fmt.Errorf("durante time profile refers to an unknown model version")
		}
		if err := model.resolve(binding).Validate(); err != nil {
			return err
		}
		optionsID := ""
		if r.Schema == 1 {
			if binding.RuntimeOptions != nil {
				return fmt.Errorf("schema 1 time profiles cannot claim runtime options binding")
			}
		} else {
			if binding.RuntimeOptions == nil || !SupportsRuntimeOptions(binding.SourceLanguage) {
				return fmt.Errorf("schema 2 time profile requires runtime_options and a supported launcher")
			}
			optionsID, err = binding.RuntimeOptions.ID()
			if err != nil {
				return err
			}
		}
		key := profileKey{runtimeID, binding.EngineProfile, binding.DispatchProfile, optionsID, binding.CPUIDProfile}
		if seen[key] {
			return fmt.Errorf("duplicate durante time profile for %q", runtimeID)
		}
		seen[key] = true
	}
	return nil
}

// Clone keeps immutable service/remote snapshots independent of operator slices.
func (r *TimeModels) Clone() *TimeModels {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Models = slices.Clone(r.Models)
	copy.Profiles = slices.Clone(r.Profiles)
	for i := range copy.Profiles {
		copy.Profiles[i].RuntimeOptions = r.Profiles[i].RuntimeOptions.clone()
	}
	return &copy
}
