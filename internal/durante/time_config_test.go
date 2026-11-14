package durante

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const timeConfigImageDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func timeConfigurationFixture(t *testing.T) Config {
	t.Helper()
	cfg, err := ParseConfig("cpp", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	policy := timePolicyFixture()
	policy.RuntimeID = "CPP20@" + timeConfigImageDigest
	cfg.TimePolicy = &policy
	cfg.RuntimeImage = timeConfigImageDigest
	cfg.ResourcePolicy = &ResourcePolicy{
		MaxLimits:      ResourceLimits{CPUTimeMs: 4000, WallTimeMs: 8000, MemoryMB: 512},
		MemoryBudgetMB: 1280, ReservedMemoryMB: 128,
	}
	return cfg
}

func timeConfigurationJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseTimePolicyExplicitContract(t *testing.T) {
	for _, raw := range []string{"", " \t\n"} {
		if policy, err := ParseTimePolicy(raw); err != nil || policy != nil {
			t.Fatalf("empty configuration enabled a policy: %+v, %v", policy, err)
		}
	}
	want := *timeConfigurationFixture(t).TimePolicy
	want.ModelVersion = math.MaxUint64
	want.InstructionNSNumerator = math.MaxUint64
	want.REPByteNSNumerator = math.MaxUint64
	want.NSDenominator = math.MaxUint64
	policy, err := ParseTimePolicy(timeConfigurationJSON(t, want))
	if err != nil || policy == nil || *policy != want {
		t.Fatalf("explicit policy lost exact values: %+v, %v", policy, err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(timeConfigurationJSON(t, want)), &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		for _, null := range []bool{false, true} {
			name := "missing " + key
			if null {
				name = "null " + key
			}
			t.Run(name, func(t *testing.T) {
				candidate := make(map[string]json.RawMessage, len(fields))
				for k, value := range fields {
					candidate[k] = value
				}
				if null {
					candidate[key] = json.RawMessage("null")
				} else {
					delete(candidate, key)
				}
				if policy, err := ParseTimePolicy(timeConfigurationJSON(t, candidate)); err == nil || policy != nil {
					t.Fatalf("incomplete contract returned a policy: %+v, %v", policy, err)
				}
			})
		}
	}
}

func TestParseTimePolicyRejectsAmbiguousOrInvalidJSON(t *testing.T) {
	raw := timeConfigurationJSON(t, timeConfigurationFixture(t).TimePolicy)
	for name, value := range map[string]string{
		"null object":       "null",
		"array":             "[]",
		"unknown field":     strings.Replace(raw, "{", `{"extra":1,`, 1),
		"case folded field": strings.Replace(raw, `"model_id"`, `"MODEL_ID"`, 1),
		"duplicate field":   strings.Replace(raw, "{", `{"model_id":"other",`, 1),
		"escaped duplicate": strings.Replace(raw, "{", `{"model_\u0069d":"other",`, 1),
		"trailing object":   raw + "{}",
		"trailing garbage":  raw + "x",
		"string number":     strings.Replace(raw, `"model_version":1`, `"model_version":"1"`, 1),
		"fraction":          strings.Replace(raw, `"model_version":1`, `"model_version":1.0`, 1),
		"exponent":          strings.Replace(raw, `"model_version":1`, `"model_version":1e0`, 1),
		"negative number":   strings.Replace(raw, `"model_version":1`, `"model_version":-1`, 1),
		"overflow":          strings.Replace(raw, `"model_version":1`, `"model_version":18446744073709551616`, 1),
		"boolean":           strings.Replace(raw, `"model_version":1`, `"model_version":true`, 1),
		"numeric identity":  strings.Replace(raw, `"model_id":"arithmetic-test"`, `"model_id":1`, 1),
		"invalid policy":    strings.Replace(raw, `"ns_denominator":1`, `"ns_denominator":0`, 1),
		"oversized":         strings.Repeat(" ", maxTimePolicyBytes) + raw,
		"invalid UTF-8":     strings.Replace(raw, "arithmetic-test", string([]byte{0xff}), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if policy, err := ParseTimePolicy(value); err == nil || policy != nil {
				t.Fatalf("invalid configuration returned a policy: %+v, %v", policy, err)
			}
		})
	}
}

func TestTimeRuntimeIDUsesCanonicalSourceAndImmutableImage(t *testing.T) {
	for language, source := range map[string]string{" cpp20 ": "CPP20", "CPP": "CPP", "c11": "C11", "C": "C"} {
		got, err := TimeRuntimeID(language, timeConfigImageDigest)
		if err != nil || got != source+"@"+timeConfigImageDigest {
			t.Fatalf("%q: identity %q, %v", language, got, err)
		}
	}
	for _, language := range []string{"binary", "unknown", "RUST2021", "PYTHON3", "JAVA25"} {
		if got, err := TimeRuntimeID(language, timeConfigImageDigest); err == nil || got != "" {
			t.Fatalf("non-C/C++ source %q returned identity %q: %v", language, got, err)
		}
	}
	for _, digest := range []string{"", "runner:latest", timeConfigImageDigest + " ", strings.ToUpper(timeConfigImageDigest), "sha256:abc"} {
		if got, err := TimeRuntimeID("CPP20", digest); err == nil || got != "" {
			t.Fatalf("invalid image %q returned identity %q: %v", digest, got, err)
		}
	}
}

func TestValidateTimeConfigurationRequiresIndependentRuntimeBinding(t *testing.T) {
	for _, cfg := range []Config{{}, {RuntimeImage: timeConfigImageDigest}, timeConfigurationFixture(t)} {
		if err := cfg.ValidateTimeConfiguration(); err != nil {
			t.Fatalf("valid configuration rejected: %v", err)
		}
	}
	compatibleDispatch := timeConfigurationFixture(t)
	compatibleDispatch.DispatchProfile = DispatchGLIBC241
	compatibleDispatch.TimePolicy.DispatchProfile = DispatchGLIBC241
	if err := compatibleDispatch.ValidateTimeConfiguration(); err != nil {
		t.Fatalf("matching qualified dispatch rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"resource policy absent":  func(c *Config) { c.ResourcePolicy = nil },
		"resource policy invalid": func(c *Config) { c.ResourcePolicy.MaxLimits.MemoryMB = 0 },
		"family disabled":         func(c *Config) { c.Families = []string{"native"} },
		"other policy family":     func(c *Config) { c.TimePolicy.Family = "native" },
		"invalid policy":          func(c *Config) { c.TimePolicy.NSDenominator = 0 },
		"dispatch mismatch":       func(c *Config) { c.DispatchProfile = DispatchGLIBC241 },
		"image absent":            func(c *Config) { c.RuntimeImage = "" },
		"image tag":               func(c *Config) { c.RuntimeImage = "runner:latest" },
		"image mismatch":          func(c *Config) { c.RuntimeImage = "sha256:" + strings.Repeat("f", 64) },
		"source alias":            func(c *Config) { c.TimePolicy.RuntimeID = "cpp20@" + timeConfigImageDigest },
		"source omitted":          func(c *Config) { c.TimePolicy.RuntimeID = timeConfigImageDigest },
		"ambiguous source":        func(c *Config) { c.TimePolicy.RuntimeID = "binary@" + timeConfigImageDigest },
		"other source family":     func(c *Config) { c.TimePolicy.RuntimeID = "RUST2021@" + timeConfigImageDigest },
		"extra runtime component": func(c *Config) { c.TimePolicy.RuntimeID += "@other" },
		"invalid disabled image":  func(c *Config) { c.TimePolicy = nil; c.RuntimeImage = "runner:latest" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := timeConfigurationFixture(t)
			mutate(&cfg)
			if err := cfg.ValidateTimeConfiguration(); err == nil {
				t.Fatal("incompatible time configuration accepted")
			}
			if policy, runtimeID, err := cfg.TimePolicyFor("CPP20"); err == nil || policy != (TimePolicy{}) || runtimeID != "" {
				t.Fatalf("incompatible configuration returned a policy: %+v, %q, %v", policy, runtimeID, err)
			}
		})
	}
}

func TestTimePolicyForRequiresExactSourceAndReturnsValueCopy(t *testing.T) {
	cfg := timeConfigurationFixture(t)
	policy, runtimeID, err := cfg.TimePolicyFor(" cpp20 ")
	if err != nil || policy != *cfg.TimePolicy || runtimeID != cfg.TimePolicy.RuntimeID {
		t.Fatalf("matching source rejected: %+v, %q, %v", policy, runtimeID, err)
	}
	policy.InstructionNSNumerator++
	if policy.InstructionNSNumerator == cfg.TimePolicy.InstructionNSNumerator {
		t.Fatal("returned policy aliases configured policy")
	}
	for _, language := range []string{"CPP", "CPP23", "C11", "binary", "PYTHON3"} {
		if policy, runtimeID, err := cfg.TimePolicyFor(language); err == nil || policy != (TimePolicy{}) || runtimeID != "" {
			t.Fatalf("unbound source %q returned a policy: %+v, %q, %v", language, policy, runtimeID, err)
		}
	}
	cfg.TimePolicy = nil
	if policy, runtimeID, err := cfg.TimePolicyFor("CPP20"); err == nil || policy != (TimePolicy{}) || runtimeID != "" {
		t.Fatalf("disabled configuration returned a policy: %+v, %q, %v", policy, runtimeID, err)
	}
}
