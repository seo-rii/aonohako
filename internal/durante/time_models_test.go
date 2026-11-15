package durante

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func timeModelsFixture(t *testing.T) Config {
	t.Helper()
	cfg := timeConfigurationFixture(t)
	cfg.Families = []string{"cpp", "cpython", "pypy", "native"}
	cfg.TimePolicy = nil
	cfg.TimeModels = &TimeModels{Schema: 1, Models: []TimeModel{{
		ModelID: "shared-test", ModelVersion: 1, ReferenceID: "synthetic-only",
		Coefficients: TimeCoefficients{InstructionNSNumerator: 2, REPByteNSNumerator: 3, NSDenominator: 4},
	}}, Profiles: []TimeProfileBinding{}}
	for _, source := range []string{"CPP20", "C11", "PYTHON3", "PYPY3", "RUST2021"} {
		family, _ := FamilyForLanguage(source)
		cfg.TimeModels.Profiles = append(cfg.TimeModels.Profiles, TimeProfileBinding{
			SourceLanguage: source, Family: family, RuntimeImage: cfg.RuntimeImage,
			EngineProfile: EngineProfile, DispatchProfile: DispatchDefault,
			ModelID: "shared-test", ModelVersion: 1, QualificationID: "synthetic-" + source,
		})
	}
	return cfg
}

func TestTimeModelsShareCoefficientsAcrossExplicitProfiles(t *testing.T) {
	cfg := timeModelsFixture(t)
	registry, err := ParseTimeModels(timeConfigurationJSON(t, cfg.TimeModels))
	if err != nil || !reflect.DeepEqual(registry, cfg.TimeModels) {
		t.Fatalf("registry changed in JSON: %+v, %v", registry, err)
	}
	cfg.TimeModels = registry
	for _, binding := range registry.Profiles {
		t.Run(binding.SourceLanguage, func(t *testing.T) {
			policy, runtimeID, err := cfg.TimePolicyFor(strings.ToLower(binding.SourceLanguage))
			if err != nil || runtimeID != binding.SourceLanguage+"@"+cfg.RuntimeImage || policy.Family != binding.Family || policy.ModelID != "shared-test" {
				t.Fatalf("profile did not resolve its shared model: %+v %q %v", policy, runtimeID, err)
			}
			report := timeReportFixture(100, 5)
			report.Family = binding.Family
			estimate, err := policy.Estimate(runtimeID, report)
			if err != nil || estimate.Nanoseconds != 54 || estimate.InstructionNSNumerator != 2 || estimate.REPByteNSNumerator != 3 || estimate.NSDenominator != 4 {
				t.Fatalf("identical counters did not use shared exact coefficients: %+v %v", estimate, err)
			}
			policy.InstructionNSNumerator = 999
			if registry.Models[0].Coefficients.InstructionNSNumerator != 2 {
				t.Fatal("resolved policy changed the shared model")
			}
		})
	}
}

func TestTimeModelsRequireIndependentImageAndExplicitApproval(t *testing.T) {
	for name, edit := range map[string]func(*Config){
		"catalog only":          func(c *Config) { c.TimeModels.Profiles = []TimeProfileBinding{} },
		"other source":          func(c *Config) { c.TimeModels.Profiles = c.TimeModels.Profiles[:2] },
		"other image":           func(c *Config) { c.RuntimeImage = "sha256:" + strings.Repeat("a", 64) },
		"family disabled":       func(c *Config) { c.Families = []string{"cpp"} },
		"incompatible dispatch": func(c *Config) { c.DispatchProfile = DispatchGLIBC241 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := timeModelsFixture(t)
			edit(&cfg)
			// A catalog may contain bindings for other deployments. Merely loading
			// it does not declare that this source/image combination is approved.
			if err := cfg.ValidateTimeConfiguration(); err != nil {
				t.Fatal(err)
			}
			if policy, runtimeID, err := cfg.TimePolicyFor("PYTHON3"); err == nil || policy != (TimePolicy{}) || runtimeID != "" {
				t.Fatalf("unapproved execution received a model: %+v %q %v", policy, runtimeID, err)
			}
		})
	}
	for name, edit := range map[string]func(*Config){
		"no image":      func(c *Config) { c.RuntimeImage = "" },
		"mutable image": func(c *Config) { c.RuntimeImage = "runner:latest" },
		"no raw policy": func(c *Config) { c.ResourcePolicy = nil },
		"both settings": func(c *Config) { p := timePolicyFixture(); c.TimePolicy = &p },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := timeModelsFixture(t)
			edit(&cfg)
			if err := cfg.ValidateTimeConfiguration(); err == nil {
				t.Fatal("incompatible settings accepted")
			}
		})
	}
}

func TestTimeModelsSelectExactVersionImageAndDispatch(t *testing.T) {
	cfg := timeModelsFixture(t)
	second := cfg.TimeModels.Models[0]
	second.ModelVersion, second.Coefficients.InstructionNSNumerator = 2, 7
	cfg.TimeModels.Models = append(cfg.TimeModels.Models, second)
	binding := cfg.TimeModels.Profiles[0]
	binding.ModelVersion, binding.RuntimeImage = 2, "sha256:"+strings.Repeat("b", 64)
	cfg.TimeModels.Profiles = append(cfg.TimeModels.Profiles, binding)
	portable := binding
	portable.ModelVersion, portable.DispatchProfile = 1, DispatchGLIBC241
	cfg.TimeModels.Profiles = append(cfg.TimeModels.Profiles, portable)
	for _, tc := range []struct {
		image, dispatch         string
		wantVersion, wantWeight uint64
	}{
		{timeConfigImageDigest, DispatchDefault, 1, 2},
		{binding.RuntimeImage, DispatchDefault, 2, 7},
		{binding.RuntimeImage, DispatchGLIBC241, 1, 2},
	} {
		cfg.RuntimeImage, cfg.DispatchProfile = tc.image, tc.dispatch
		policy, _, err := cfg.TimePolicyFor("CPP20")
		if err != nil || policy.ModelVersion != tc.wantVersion || policy.InstructionNSNumerator != tc.wantWeight || policy.DispatchProfile != tc.dispatch {
			t.Fatalf("wrong execution profile selected: %+v %v", policy, err)
		}
	}
}

func TestTimeModelsRejectInvalidOrAmbiguousBindings(t *testing.T) {
	for name, edit := range map[string]func(*TimeModels){
		"schema":            func(r *TimeModels) { r.Schema++ },
		"no models":         func(r *TimeModels) { r.Models = nil },
		"zero coefficient":  func(r *TimeModels) { r.Models[0].Coefficients.REPByteNSNumerator = 0 },
		"unknown method":    func(r *TimeModels) { r.Models[0].Method = "other" },
		"invalid identity":  func(r *TimeModels) { r.Models[0].ReferenceID = " reference " },
		"duplicate model":   func(r *TimeModels) { r.Models = append(r.Models, r.Models[0]) },
		"duplicate profile": func(r *TimeModels) { r.Profiles = append(r.Profiles, r.Profiles[0]) },
		"ambiguous model": func(r *TimeModels) {
			other := r.Models[0]
			other.ModelVersion++
			r.Models = append(r.Models, other)
			binding := r.Profiles[0]
			binding.ModelVersion++
			r.Profiles = append(r.Profiles, binding)
		},
		"missing qualification":       func(r *TimeModels) { r.Profiles[0].QualificationID = "" },
		"source alias":                func(r *TimeModels) { r.Profiles[0].SourceLanguage = "cpp20" },
		"unknown source":              func(r *TimeModels) { r.Profiles[0].SourceLanguage = "binary" },
		"family mismatch":             func(r *TimeModels) { r.Profiles[0].Family = "native" },
		"missing model":               func(r *TimeModels) { r.Profiles[0].ModelID = "other" },
		"missing model version":       func(r *TimeModels) { r.Profiles[0].ModelVersion++ },
		"unknown engine":              func(r *TimeModels) { r.Profiles[0].EngineProfile = "old-engine" },
		"unknown dispatch":            func(r *TimeModels) { r.Profiles[0].DispatchProfile = "unknown" },
		"mutable binding image":       func(r *TimeModels) { r.Profiles[0].RuntimeImage = "runner:latest" },
		"JVM not qualified":           func(r *TimeModels) { r.Profiles[0].SourceLanguage, r.Profiles[0].Family = "JAVA21", "jvm" },
		"non-C++ controlled dispatch": func(r *TimeModels) { r.Profiles[2].DispatchProfile = DispatchGLIBC241 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := timeModelsFixture(t)
			edit(cfg.TimeModels)
			if err := cfg.ValidateTimeConfiguration(); err == nil {
				t.Fatal("invalid registry accepted")
			}
			if registry, err := ParseTimeModels(timeConfigurationJSON(t, cfg.TimeModels)); err == nil || registry != nil {
				t.Fatalf("invalid registry JSON accepted: %+v %v", registry, err)
			}
		})
	}
}

func TestParseTimeModelsPreservesExactFieldsAndRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{"", " \n"} {
		if registry, err := ParseTimeModels(raw); err != nil || registry != nil {
			t.Fatal("empty setting enabled models", err)
		}
	}
	cfg := timeModelsFixture(t)
	cfg.TimeModels.Models[0].Method = TimeMethod
	cfg.TimeModels.Models[0].Coefficients = TimeCoefficients{math.MaxUint64, math.MaxUint64, math.MaxUint64}
	raw := timeConfigurationJSON(t, cfg.TimeModels)
	if got, err := ParseTimeModels(raw); err != nil || !reflect.DeepEqual(got, cfg.TimeModels) {
		t.Fatal("explicit method or uint64 coefficients changed", err)
	}
	for name, invalid := range map[string]string{
		"null": "null", "array": "[]", "trailing object": raw + "{}",
		"unknown root":          strings.Replace(raw, `"schema":1`, `"schema":1,"extra":1`, 1),
		"duplicate root":        strings.Replace(raw, `"schema":1`, `"schema":1,"schema":1`, 1),
		"duplicate model field": strings.Replace(raw, `"model_id":"shared-test"`, `"model_id":"other","model_id":"shared-test"`, 1),
		"escaped duplicate":     strings.Replace(raw, `"model_id":"shared-test"`, `"model_\u0069d":"other","model_id":"shared-test"`, 1),
		"case alias":            strings.Replace(raw, `"runtime_image"`, `"RUNTIME_IMAGE"`, 1),
		"missing profile field": strings.Replace(raw, `"qualification_id":"synthetic-CPP20"`, `"unexpected":"x"`, 1),
		"null qualification":    strings.Replace(raw, `"qualification_id":"synthetic-CPP20"`, `"qualification_id":null`, 1),
		"null method":           strings.Replace(raw, `"method":"`+TimeMethod+`"`, `"method":null`, 1),
		"null profiles":         strings.Replace(raw, `"profiles":[`, `"profiles":null,"discard":[`, 1),
		"fraction":              strings.Replace(raw, `"model_version":1`, `"model_version":1.0`, 1),
		"overflow":              strings.Replace(raw, `18446744073709551615`, `18446744073709551616`, 1),
		"unknown coefficient":   strings.Replace(raw, `"ns_denominator"`, `"unexpected"`, 1),
		"invalid UTF8":          strings.Replace(raw, "synthetic-only", string([]byte{0xff}), 1),
		"too large":             strings.Repeat(" ", maxTimeModelsBytes) + raw,
	} {
		t.Run(name, func(t *testing.T) {
			if registry, err := ParseTimeModels(invalid); err == nil || registry != nil {
				t.Fatalf("invalid JSON accepted: %+v %v", registry, err)
			}
		})
	}
}
