package durante

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestLatencyTimeModelUsesBoundInsteadOfInstructionCount(t *testing.T) {
	p := timePolicyFixture()
	p.Method, p.InstructionNSNumerator, p.LatencyX16NSNumerator = TimeMethodLatencyBoundREP, 0, 2
	p.NSDenominator = 3
	r := timeReportFixture(3, 5)
	r.LatencyBound = &LatencyBound{Version: 1, CyclesX16: 32}
	got, err := p.Estimate(p.RuntimeID, r)
	if err != nil || got.Nanoseconds != 23 || got.LatencyX16NSNumerator != 2 || got.Method != p.Method {
		t.Fatalf("latency estimate: %+v %v", got, err)
	}
	r.LatencyBound.CyclesX16 = 33
	got, err = p.Estimate(p.RuntimeID, r)
	if err != nil || got.Nanoseconds != 24 {
		t.Fatalf("combined rounding: %+v %v", got, err)
	}
	r.LatencyBound.CyclesX16 = math.MaxUint64
	if _, e, err := p.EstimateWithEligibility(p.RuntimeID, r); err == nil || e.ReasonCodes[0] != TimeNumericOverflow {
		t.Fatalf("overflow: %+v %v", e, err)
	}
	r.LatencyBound = nil
	if _, e, err := p.EstimateWithEligibility(p.RuntimeID, r); err == nil || e.ReasonCodes[0] != TimeLatencyBoundMissing {
		t.Fatalf("absent counter: %+v %v", e, err)
	}
	r.LatencyBound = &LatencyBound{Version: 1, CyclesX16: 11}
	if _, e, err := p.EstimateWithEligibility(p.RuntimeID, r); err == nil || e.ReasonCodes[0] != TimeLatencyBoundInvalid {
		t.Fatalf("invalid counter: %+v %v", e, err)
	}
}

func TestLatencyWeightSelectionOnStandaloneAndSharedModels(t *testing.T) {
	p := timePolicyFixture()
	p.Method, p.InstructionNSNumerator, p.LatencyX16NSNumerator = TimeMethodLatencyBoundREP, 0, 2
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseTimePolicy(string(raw))
	if err != nil || *parsed != p {
		t.Fatalf("standalone latency policy: %+v %v", parsed, err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), "{", `{"instruction_ns_numerator":0,`, 1),
		strings.Replace(string(raw), TimeMethodLatencyBoundREP, TimeMethod, 1),
		strings.Replace(string(raw), `"latency_x16_ns_numerator":2`, `"latency_x16_ns_numerator":0`, 1),
	} {
		if _, err := ParseTimePolicy(bad); err == nil {
			t.Fatal("accepted incompatible standalone weights")
		}
	}
	cfg := timeModelsFixture(t)
	m := &cfg.TimeModels.Models[0]
	m.Method = p.Method
	m.Coefficients.InstructionNSNumerator, m.Coefficients.LatencyX16NSNumerator = 0, 2
	wire := timeConfigurationJSON(t, cfg.TimeModels)
	registry, err := ParseTimeModels(wire)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TimeModels = registry
	lang := registry.Profiles[0].SourceLanguage
	if _, _, err := cfg.TimePolicyFor(lang); err == nil {
		t.Fatal("resolved latency model on a runner with counter disabled")
	}
	cfg.LatencyBound = true
	resolved, _, err := cfg.TimePolicyFor(lang)
	if err != nil || resolved.Method != p.Method || resolved.LatencyX16NSNumerator != 2 {
		t.Fatalf("shared model: %+v %v", resolved, err)
	}
	if _, err := ParseTimeModels(strings.Replace(wire, `"latency_x16_ns_numerator"`, `"instruction_ns_numerator":0,"latency_x16_ns_numerator"`, 1)); err == nil {
		t.Fatal("accepted two work dimensions in registry")
	}
}
