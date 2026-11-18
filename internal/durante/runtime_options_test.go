package durante

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func runtimeOptionsFixture() RuntimeOptions {
	return RuntimeOptions{Version: 1, Argv: []string{"/__aonohako_program__"}, Env: []string{"LANG=C.UTF-8", "PATH=/usr/bin"}, MemoryMB: 256}
}

func TestRuntimeOptionsRegistrySelectsExactDescriptorWithoutFallback(t *testing.T) {
	cfg := timeModelsFixture(t)
	cfg.TimeModels.Schema = 2
	cfg.TimeModels.Profiles = cfg.TimeModels.Profiles[:1]
	options := runtimeOptionsFixture()
	cfg.TimeModels.Profiles[0].RuntimeOptions = &options
	second := cfg.TimeModels.Models[0]
	second.ModelVersion = 2
	second.Coefficients.InstructionNSNumerator = 20
	cfg.TimeModels.Models = append(cfg.TimeModels.Models, second)
	binding := cfg.TimeModels.Profiles[0]
	binding.RuntimeOptions = options.clone()
	binding.RuntimeOptions.MemoryMB = 512
	binding.ModelVersion = 2
	cfg.TimeModels.Profiles = append(cfg.TimeModels.Profiles, binding)
	parsed, err := ParseTimeModels(timeConfigurationJSON(t, cfg.TimeModels))
	if err != nil || !reflect.DeepEqual(parsed, cfg.TimeModels) {
		t.Fatalf("schema 2 did not round trip: %v", err)
	}
	cfg.TimeModels = parsed
	for _, binding := range parsed.Profiles {
		policy, runtimeID, err := cfg.TimePolicyForOptions("cpp20", *binding.RuntimeOptions)
		if err != nil || policy.ModelVersion != binding.ModelVersion {
			t.Fatalf("wrong options binding selected: %+v %v", policy, err)
		}
		estimate, err := policy.Estimate(runtimeID, timeReportFixture(100, 5))
		id, _ := binding.RuntimeOptions.ID()
		if err != nil || estimate.RuntimeOptionsID != id || id == "" {
			t.Fatalf("estimate lost effective options: %+v %v", estimate, err)
		}
	}
	if _, _, err := cfg.TimePolicyFor("CPP20"); err == nil {
		t.Fatal("language-only lookup bypassed options binding")
	}
	options.MemoryMB = 1024
	if _, _, err := cfg.TimePolicyForOptions("CPP20", options); err == nil {
		t.Fatal("unbound options fell back to another model")
	}
}

func TestRuntimeOptionsRegistryRejectsMissingMixedAndUnsupportedDescriptors(t *testing.T) {
	for name, edit := range map[string]func(*TimeModels){
		"missing":      func(r *TimeModels) { r.Profiles[0].RuntimeOptions = nil },
		"legacy claim": func(r *TimeModels) { r.Schema = 1 },
		"version":      func(r *TimeModels) { r.Profiles[0].RuntimeOptions.Version = 2 },
		"duplicate":    func(r *TimeModels) { r.Profiles = append(r.Profiles, r.Profiles[0]) },
		"unsupported launcher": func(r *TimeModels) {
			r.Profiles[0].SourceLanguage, r.Profiles[0].Family = "JAVASCRIPT_DENO", "javascript-wasm"
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := timeModelsFixture(t)
			cfg.TimeModels.Schema, cfg.TimeModels.Profiles = 2, cfg.TimeModels.Profiles[:1]
			options := runtimeOptionsFixture()
			cfg.TimeModels.Profiles[0].RuntimeOptions = &options
			edit(cfg.TimeModels)
			if err := cfg.TimeModels.Validate(); err == nil {
				t.Fatal("incomplete or misleading binding accepted")
			}
		})
	}
}

func TestRuntimeOptionsJSONIdentityAndSnapshot(t *testing.T) {
	options := runtimeOptionsFixture()
	encoded, _ := json.Marshal(options)
	id, err := options.ID()
	var roundTrip RuntimeOptions
	if err != nil || json.Unmarshal(encoded, &roundTrip) != nil {
		t.Fatal("descriptor JSON failed", err)
	}
	if got, _ := roundTrip.ID(); got != id {
		t.Fatal("descriptor ID changed across JSON")
	}
	for _, invalid := range []string{
		strings.Replace(string(encoded), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(encoded), `"argv":`, `"unknown":`, 1),
		strings.Replace(string(encoded), `"memory_mb":256`, `"memory_mb":null`, 1),
		strings.Replace(string(encoded), `"version":1`, `"version":2`, 1),
	} {
		if json.Unmarshal([]byte(invalid), &roundTrip) == nil {
			t.Fatalf("ambiguous descriptor accepted: %s", invalid)
		}
	}
	duplicate := runtimeOptionsFixture()
	duplicate.Env = []string{"LANG=C.UTF-8", "LANG=other"}
	if _, err := duplicate.ID(); err == nil {
		t.Fatal("duplicate environment keys received an identity")
	}
	cfg := timeModelsFixture(t)
	cfg.TimeModels.Profiles[0].RuntimeOptions = &options
	cloned := cfg.TimeModels.Clone()
	options.Argv[0], options.Env[0] = "changed", "LANG=other"
	got, _ := cloned.Profiles[0].RuntimeOptions.ID()
	if got != id {
		t.Fatal("snapshot retained descriptor slices")
	}
}

func TestRuntimeOptionsLegacyRemainsExplicitlyUnbound(t *testing.T) {
	cfg := timeModelsFixture(t)
	p, runtimeID, err := cfg.TimePolicyFor("CPP20")
	if err != nil {
		t.Fatal(err)
	}
	estimate, err := p.Estimate(runtimeID, timeReportFixture(100, 5))
	if err != nil || estimate.RuntimeOptionsID != "" {
		t.Fatal("schema 1 was promoted to options-bound", err)
	}
	encoded, _ := json.Marshal(estimate)
	if strings.Contains(string(encoded), "runtime_options_id") {
		t.Fatal("legacy result fabricated options metadata")
	}
}
