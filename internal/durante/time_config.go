package durante

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"aonohako/internal/profiles"
	"aonohako/internal/runtimeidentity"
)

const maxTimePolicyBytes = 4096

// ParseTimePolicy requires an explicit conversion contract, without supplying
// any default weights or treating a reference ID as proof of calibration.
func ParseTimePolicy(raw string) (*TimePolicy, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxTimePolicyBytes || !utf8.ValidString(raw) {
		return nil, fmt.Errorf("invalid durante time policy encoding or size")
	}
	var policy TimePolicy
	fields := map[string]any{
		"method":                   &policy.Method,
		"model_id":                 &policy.ModelID,
		"model_version":            &policy.ModelVersion,
		"reference_id":             &policy.ReferenceID,
		"runtime_id":               &policy.RuntimeID,
		"family":                   &policy.Family,
		"engine_profile":           &policy.EngineProfile,
		"dispatch_profile":         &policy.DispatchProfile,
		"instruction_ns_numerator": &policy.InstructionNSNumerator,
		"rep_byte_ns_numerator":    &policy.REPByteNSNumerator,
		"movs_byte_ns_numerator":   &policy.MOVSByteNSNumerator,
		"stos_byte_ns_numerator":   &policy.STOSByteNSNumerator,
		"ns_denominator":           &policy.NSDenominator,
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("durante time policy must be an object")
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, fmt.Errorf("invalid durante time policy key")
		}
		field, exists := fields[key]
		if !exists || seen[key] {
			return nil, fmt.Errorf("unknown or duplicate durante time policy field %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid durante time policy field %q", key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("durante time policy field %q cannot be null", key)
		}
		if err := json.Unmarshal(value, field); err != nil {
			return nil, fmt.Errorf("invalid durante time policy field %q", key)
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("invalid durante time policy object end")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing durante time policy data")
	}
	required := []string{"model_id", "model_version", "reference_id", "runtime_id", "family", "engine_profile", "dispatch_profile", "instruction_ns_numerator", "ns_denominator"}
	switch policy.Formula() {
	case TimeMethod:
		required = append(required, "rep_byte_ns_numerator")
		if seen["movs_byte_ns_numerator"] || seen["stos_byte_ns_numerator"] {
			return nil, fmt.Errorf("v1 time policy cannot include operation-specific weights")
		}
	case TimeMethodREPBreakdown:
		required = append(required, "method", "movs_byte_ns_numerator", "stos_byte_ns_numerator")
		if seen["rep_byte_ns_numerator"] {
			return nil, fmt.Errorf("v2 time policy cannot include the combined REP weight")
		}
	default:
		return nil, fmt.Errorf("unsupported durante time method")
	}
	if seen["method"] && policy.Method == "" {
		return nil, fmt.Errorf("explicit durante time method cannot be empty")
	}
	for _, key := range required {
		if !seen[key] {
			return nil, fmt.Errorf("durante time policy requires field %q explicitly", key)
		}
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &policy, nil
}

// TimeRuntimeID binds a canonical supported source profile to the operator's
// independently configured deployment image. The digest is metadata, not
// runtime attestation.
func TimeRuntimeID(language, imageDigest string) (string, error) {
	profile, ok := profiles.Resolve(language)
	if !ok {
		return "", fmt.Errorf("durante time requires a registered source-language profile")
	}
	family, ok := FamilyForLanguage(profile.SourceLang)
	if !ok {
		return "", fmt.Errorf("durante time requires a registered measurement family")
	}
	if err := ValidateMeasurementFamily(family); err != nil {
		return "", err
	}
	if !runtimeidentity.ValidImageDigest(imageDigest) {
		return "", fmt.Errorf("durante runtime image must be an immutable sha256 image digest")
	}
	return profile.SourceLang + "@" + imageDigest, nil
}

func (c Config) ValidateTimeConfiguration() error {
	if c.RuntimeImage != "" && !runtimeidentity.ValidImageDigest(c.RuntimeImage) {
		return fmt.Errorf("durante runtime image must be an immutable sha256 image digest")
	}
	if c.TimePolicy != nil && c.TimeModels != nil {
		return fmt.Errorf("durante time policy and time models cannot be configured together")
	}
	if c.TimeModels != nil {
		if err := c.TimeModels.Validate(); err != nil {
			return err
		}
		if !runtimeidentity.ValidImageDigest(c.RuntimeImage) {
			return fmt.Errorf("durante time models requires an independently configured immutable runtime image")
		}
		if err := c.ResourcePolicy.validate(); err != nil {
			return fmt.Errorf("durante time models requires a valid resource policy: %w", err)
		}
		return nil
	}
	if c.TimePolicy == nil {
		return nil
	}
	if err := c.TimePolicy.Validate(); err != nil {
		return err
	}
	if c.TimePolicy.Family != "cpp" {
		return fmt.Errorf("durante time policy requires the cpp family")
	}
	if err := c.ResourcePolicy.validate(); err != nil {
		return fmt.Errorf("durante time policy requires a valid resource policy: %w", err)
	}
	if c.TimePolicy.DispatchProfile != c.DispatchProfile {
		return fmt.Errorf("durante time policy dispatch profile does not match the configured dispatch profile")
	}
	source, _, hasImage := strings.Cut(c.TimePolicy.RuntimeID, "@")
	if !hasImage {
		return fmt.Errorf("durante time policy runtime ID requires a canonical source and image digest")
	}
	runtimeID, err := TimeRuntimeID(source, c.RuntimeImage)
	if err != nil {
		return err
	}
	if runtimeID != c.TimePolicy.RuntimeID {
		return fmt.Errorf("durante time policy runtime ID does not match its canonical source and configured image digest")
	}
	family, err := c.Family(source)
	if err != nil {
		return fmt.Errorf("durante time policy: %w", err)
	}
	if family != c.TimePolicy.Family {
		return fmt.Errorf("durante time policy family does not match its source-language profile")
	}
	return nil
}

// TimePolicyFor returns a value copy so callers cannot mutate the configured
// policy through the result. Shared models require an explicit binding for each
// source/image/engine/dispatch combination. Legacy policies keep their original
// single C/C++ source binding.
func (c Config) TimePolicyFor(language string) (TimePolicy, string, error) {
	if err := c.ValidateTimeConfiguration(); err != nil {
		return TimePolicy{}, "", err
	}
	if c.TimeModels != nil {
		if _, err := c.Family(language); err != nil {
			return TimePolicy{}, "", err
		}
		runtimeID, err := TimeRuntimeID(language, c.RuntimeImage)
		if err != nil {
			return TimePolicy{}, "", err
		}
		for _, binding := range c.TimeModels.Profiles {
			if binding.SourceLanguage+"@"+binding.RuntimeImage != runtimeID || binding.EngineProfile != EngineProfile || binding.DispatchProfile != c.DispatchProfile {
				continue
			}
			for _, model := range c.TimeModels.Models {
				if model.ModelID == binding.ModelID && model.ModelVersion == binding.ModelVersion {
					return model.resolve(binding), runtimeID, nil
				}
			}
		}
		return TimePolicy{}, "", fmt.Errorf("durante time has no approved profile for the requested source, runtime image and dispatch")
	}
	if c.TimePolicy == nil {
		return TimePolicy{}, "", fmt.Errorf("durante time policy is not enabled")
	}
	runtimeID, err := TimeRuntimeID(language, c.RuntimeImage)
	if err != nil {
		return TimePolicy{}, "", err
	}
	if runtimeID != c.TimePolicy.RuntimeID {
		return TimePolicy{}, "", fmt.Errorf("durante time policy does not match the requested source-language profile")
	}
	return *c.TimePolicy, runtimeID, nil
}
