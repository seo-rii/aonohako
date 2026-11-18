package durante

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func repTimePolicyFixture() TimePolicy {
	p := timePolicyFixture()
	p.Method, p.ModelVersion = TimeMethodREPBreakdown, 2
	p.REPByteNSNumerator, p.MOVSByteNSNumerator, p.STOSByteNSNumerator = 0, 2, 5
	return p
}

func repTimeReport(instructions, movs, stos uint64) Report {
	r := timeReportFixture(instructions, movs+stos)
	r.REPEntries = timeCounter(2)
	r.REPBreakdown = &REPBreakdown{Version: 1, MOVSBytes: movs, STOSBytes: stos, MOVSEntries: 1, STOSEntries: 1}
	return r
}

func TestREPOperationTimeModelUsesExactSeparateCosts(t *testing.T) {
	for _, tc := range []struct {
		name                                                                             string
		instructions, movs, stos, instructionWeight, movsWeight, stosWeight, denominator uint64
		want                                                                             int64
	}{
		{"copy", 3, 8, 0, 1, 2, 5, 1, 19},
		{"fill", 3, 0, 8, 1, 2, 5, 1, 43},
		{"round once", 1, 1, 1, 1, 1, 1, 3, 1},
		{"fraction", 1, 1, 1, 1, 1, 1, 2, 2},
		{"observed zero", 1, 0, 0, 1, 2, 5, 1, 1},
		{"beyond float precision", 1<<53 + 1, 1, 1, 1, 2, 5, 1, 1<<53 + 8},
		{"wide product", math.MaxInt64 - 2, 1, 1, math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := repTimePolicyFixture()
			p.InstructionNSNumerator, p.MOVSByteNSNumerator, p.STOSByteNSNumerator, p.NSDenominator = tc.instructionWeight, tc.movsWeight, tc.stosWeight, tc.denominator
			r := repTimeReport(tc.instructions, tc.movs, tc.stos)
			got, err := p.Estimate(p.RuntimeID, r)
			if err != nil || got.Nanoseconds != tc.want || got.Method != TimeMethodREPBreakdown || got.REPByteNSNumerator != 0 || got.MOVSByteNSNumerator != p.MOVSByteNSNumerator || got.STOSByteNSNumerator != p.STOSByteNSNumerator {
				t.Fatalf("separate-cost estimate=%+v error=%v want=%d", got, err, tc.want)
			}
		})
	}
}

func TestREPOperationTimeModelRequiresObservedUsableDetail(t *testing.T) {
	for name, edit := range map[string]func(*Report){
		"legacy missing":  func(r *Report) { r.REPBreakdown = nil },
		"bad partition":   func(r *Report) { r.REPBreakdown.MOVSBytes++ },
		"unknown version": func(r *Report) { r.REPBreakdown.Version++ },
		"missing entries": func(r *Report) { r.REPBreakdown.MOVSEntries = 0 },
		"partial":         func(r *Report) { r.UnsupportedREPEntries = timeCounter(1); r.REPTelemetryStatus = REPPartial },
		"interrupted":     func(r *Report) { r.SignalsObserved = 1; r.Quality = "signals_observed" },
		"stopped":         func(r *Report) { *r = *r.WithoutTotals("cancelled") },
		"result overflow": func(r *Report) { r.Instructions = timeCounter(math.MaxInt64) },
	} {
		t.Run(name, func(t *testing.T) {
			p, r := repTimePolicyFixture(), repTimeReport(3, 8, 4)
			edit(&r)
			if got, err := p.Estimate(p.RuntimeID, r); err == nil || got != (TimeEstimate{}) {
				t.Fatalf("invalid observation returned time: %+v, %v", got, err)
			}
		})
	}
}

func TestREPOperationSharedModelsKeepFormulaAndProfileBindings(t *testing.T) {
	cfg := timeModelsFixture(t)
	m := &cfg.TimeModels.Models[0]
	m.Method = TimeMethodREPBreakdown
	m.Coefficients = TimeCoefficients{InstructionNSNumerator: 1, MOVSByteNSNumerator: 2, STOSByteNSNumerator: 5, NSDenominator: 1}
	raw := timeConfigurationJSON(t, cfg.TimeModels)
	parsed, err := ParseTimeModels(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TimeModels = parsed
	for _, binding := range cfg.TimeModels.Profiles {
		policy, runtimeID, err := cfg.TimePolicyFor(binding.SourceLanguage)
		if err != nil {
			t.Fatal(err)
		}
		report := repTimeReport(3, 8, 4)
		report.Family = binding.Family
		estimate, err := policy.Estimate(runtimeID, report)
		if err != nil || estimate.Nanoseconds != 39 || estimate.Method != TimeMethodREPBreakdown || estimate.RuntimeID != runtimeID {
			t.Fatalf("shared v2 formula changed for %s: %+v %v", binding.SourceLanguage, estimate, err)
		}
	}
	for name, candidate := range map[string]string{
		"missing method":   strings.Replace(raw, `"method":"`+TimeMethodREPBreakdown+`",`, "", 1),
		"empty method":     strings.Replace(raw, TimeMethodREPBreakdown, "", 1),
		"v1 with split":    strings.Replace(raw, TimeMethodREPBreakdown, TimeMethod, 1),
		"missing STOS":     strings.Replace(raw, `"stos_byte_ns_numerator":5,`, "", 1),
		"mixed zero total": strings.Replace(raw, `"coefficients":{`, `"coefficients":{"rep_byte_ns_numerator":0,`, 1),
		"null MOVS":        strings.Replace(raw, `"movs_byte_ns_numerator":2`, `"movs_byte_ns_numerator":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseTimeModels(candidate); err == nil || got != nil {
				t.Fatalf("invalid shared v2 formula accepted: %+v %v", got, err)
			}
		})
	}
}

func TestParseREPOperationTimeModelSelectsOneFormula(t *testing.T) {
	p := repTimePolicyFixture()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseTimePolicy(string(raw))
	if err != nil || got == nil || *got != p {
		t.Fatalf("v2 round trip: %+v %v", got, err)
	}
	for name, candidate := range map[string]string{
		"missing method":   strings.Replace(string(raw), `"method":"`+TimeMethodREPBreakdown+`",`, "", 1),
		"unknown method":   strings.Replace(string(raw), TimeMethodREPBreakdown, "future", 1),
		"empty method":     strings.Replace(string(raw), TimeMethodREPBreakdown, "", 1),
		"missing MOVS":     strings.Replace(string(raw), `"movs_byte_ns_numerator":2,`, "", 1),
		"zero STOS":        strings.Replace(string(raw), `"stos_byte_ns_numerator":5`, `"stos_byte_ns_numerator":0`, 1),
		"null MOVS":        strings.Replace(string(raw), `"movs_byte_ns_numerator":2`, `"movs_byte_ns_numerator":null`, 1),
		"mixed total":      strings.Replace(string(raw), "{", `{"rep_byte_ns_numerator":1,`, 1),
		"mixed zero total": strings.Replace(string(raw), "{", `{"rep_byte_ns_numerator":0,`, 1),
		"boolean STOS":     strings.Replace(string(raw), `"stos_byte_ns_numerator":5`, `"stos_byte_ns_numerator":true`, 1),
		"duplicate MOVS":   strings.Replace(string(raw), "{", `{"movs_byte_ns_numerator":2,`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseTimePolicy(candidate); err == nil || got != nil {
				t.Fatalf("invalid v2 policy accepted: %+v %v", got, err)
			}
		})
	}
	legacy := timePolicyFixture()
	oldJSON, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(oldJSON), `"method"`) || strings.Contains(string(oldJSON), "movs_byte") {
		t.Fatalf("legacy JSON changed: %s %v", oldJSON, err)
	}
	for _, method := range []string{"", TimeMethod} {
		legacy.Method = method
		before, err := legacy.Estimate(legacy.RuntimeID, timeReportFixture(3, 12))
		if err != nil {
			t.Fatal(err)
		}
		after, err := legacy.Estimate(legacy.RuntimeID, repTimeReport(3, 8, 4))
		if err != nil || before != after {
			t.Fatalf("v1 formula changed with detail: %+v %+v %v", before, after, err)
		}
	}
}
