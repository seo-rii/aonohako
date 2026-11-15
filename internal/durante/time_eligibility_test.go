package durante

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestTimeEligibilityPreservesAllObservableExclusionReasons(t *testing.T) {
	r := timeReportFixture(100, 0)
	r.SignalsObserved, r.Quality = 2, "signals_observed"
	r.UnsupportedREPEntries, r.REPTelemetryStatus = timeCounter(3), REPPartial
	for _, p := range []TimePolicy{timePolicyFixture(), repTimePolicyFixture()} {
		before, _ := json.Marshal(r)
		want := []string{TimeSignalsObserved, TimeUnsupportedREP}
		if p.Formula() == TimeMethodREPBreakdown {
			want = append(want, TimeREPBreakdownMissing)
		}
		estimate, eligibility, err := p.EstimateWithEligibility(p.RuntimeID, r)
		if err == nil || estimate != (TimeEstimate{}) || eligibility.Eligible || eligibility.Validate() != nil || !reflect.DeepEqual(eligibility.ReasonCodes, want) {
			t.Fatalf("incomplete reasons or promoted observation: %+v %+v %v", estimate, eligibility, err)
		}
		after, _ := json.Marshal(r)
		if string(before) != string(after) {
			t.Fatal("assessment changed the original observation")
		}
	}
}

func TestTimeEligibilityDistinguishesLegacyDetailFromObservedZero(t *testing.T) {
	legacy := timeReportFixture(7, 0)
	for _, method := range []string{TimeMethod, TimeMethodREPBreakdown} {
		p := timePolicyFixture()
		if method == TimeMethodREPBreakdown {
			p = repTimePolicyFixture()
		}
		_, assessment, _ := p.EstimateWithEligibility(p.RuntimeID, legacy)
		if assessment.Eligible != (method == TimeMethod) {
			t.Fatal("legacy absence lost its method-specific meaning", assessment)
		}
		observed := legacy
		observed.REPBreakdown = &REPBreakdown{Version: 1}
		estimate, assessment, err := p.EstimateWithEligibility(p.RuntimeID, observed)
		if err != nil || !assessment.Eligible || assessment.Validate() != nil || estimate.Nanoseconds != 7 || len(assessment.ReasonCodes) != 0 || assessment.ReasonCodes == nil {
			t.Fatalf("observed zero was rejected or lost explicit eligibility: %+v %+v %v", estimate, assessment, err)
		}
	}
}

func TestEstimateAndEligibilityShareEveryGateAndArithmetic(t *testing.T) {
	for name, edit := range map[string]func(*TimePolicy, *Report, *string){
		"valid":                  func(*TimePolicy, *Report, *string) {},
		"configuration":          func(p *TimePolicy, _ *Report, _ *string) { p.NSDenominator = 0 },
		"runtime":                func(_ *TimePolicy, _ *Report, id *string) { *id += "-different" },
		"contract":               func(_ *TimePolicy, r *Report, _ *string) { r.Authority = "other" },
		"incomplete":             func(_ *TimePolicy, r *Report, _ *string) { r.Complete = false },
		"quality":                func(_ *TimePolicy, r *Report, _ *string) { r.Quality = "signals_observed" },
		"processes":              func(_ *TimePolicy, r *Report, _ *string) { r.Images = 0 },
		"REP disabled":           func(_ *TimePolicy, r *Report, _ *string) { r.REPEnabled = false },
		"missing counter":        func(_ *TimePolicy, r *Report, _ *string) { r.Instructions = nil },
		"zero instructions":      func(_ *TimePolicy, r *Report, _ *string) { r.Instructions = timeCounter(0) },
		"inconsistent partition": func(_ *TimePolicy, r *Report, _ *string) { r.REPBreakdown.MOVSBytes++ },
		"overflow":               func(_ *TimePolicy, r *Report, _ *string) { r.Instructions = timeCounter(math.MaxUint64) },
	} {
		t.Run(name, func(t *testing.T) {
			p, r := repTimePolicyFixture(), repTimeReport(11, 4, 3)
			runtimeID := p.RuntimeID
			edit(&p, &r, &runtimeID)
			got, eligibility, err := p.EstimateWithEligibility(runtimeID, r)
			legacy, legacyErr := p.Estimate(runtimeID, r)
			if (err == nil) != (name == "valid") || (err == nil) != eligibility.Eligible || (err == nil) != (legacyErr == nil) || got != legacy || eligibility.Validate() != nil {
				t.Fatalf("estimator and eligibility differ: %+v %+v %v; legacy=%+v %v", got, eligibility, err, legacy, legacyErr)
			}
			if err != nil && got != (TimeEstimate{}) {
				t.Fatal("failed assessment returned an estimate")
			}
		})
	}
}

func TestTimeEligibilityStagePriorityPreventsMisleadingLaterReasons(t *testing.T) {
	p, r := timePolicyFixture(), timeReportFixture(math.MaxUint64, 0)
	r.SignalsObserved, r.Quality = 1, "signals_observed"
	_, eligibility, _ := p.EstimateWithEligibility("wrong-runtime", r)
	if !reflect.DeepEqual(eligibility.ReasonCodes, []string{TimeRuntimeProfileMismatch}) {
		t.Fatal(eligibility)
	}
	_, eligibility, _ = p.EstimateWithEligibility(p.RuntimeID, r)
	if !reflect.DeepEqual(eligibility.ReasonCodes, []string{TimeSignalsObserved}) {
		t.Fatal("overflow must not be evaluated from an ineligible observation", eligibility)
	}
	r.SignalsObserved, r.Quality = 0, "uninterrupted"
	_, eligibility, _ = p.EstimateWithEligibility(p.RuntimeID, r)
	if !reflect.DeepEqual(eligibility.ReasonCodes, []string{TimeNumericOverflow}) {
		t.Fatal(eligibility)
	}
}

func TestTimeEligibilityJSONIsClosedAndPreservesExplicitEmptyReasons(t *testing.T) {
	for _, valid := range []string{
		`{"version":1,"eligible":true,"reason_codes":[]}`,
		`{"version":1,"eligible":false,"reason_codes":["signals_observed","unsupported_rep_entries","rep_breakdown_missing"]}`,
	} {
		var eligibility TimeEligibility
		if err := json.Unmarshal([]byte(valid), &eligibility); err != nil || eligibility.Validate() != nil {
			t.Fatal(valid, err)
		}
		encoded, _ := json.Marshal(eligibility)
		if strings.Contains(string(encoded), "null") {
			t.Fatal("explicit empty reasons became absent", string(encoded))
		}
	}
	for _, invalid := range []string{
		`null`, `[]`, `{}`, `{"version":1,"eligible":true}`, `{"version":1,"eligible":true,"reason_codes":null}`,
		`{"version":true,"eligible":true,"reason_codes":[]}`, `{"version":1.0,"eligible":true,"reason_codes":[]}`,
		`{"version":2,"eligible":true,"reason_codes":[]}`, `{"version":1,"eligible":null,"reason_codes":[]}`,
		`{"version":1,"eligible":1,"reason_codes":[]}`, `{"version":1,"eligible":false,"reason_codes":[]}`,
		`{"version":1,"eligible":true,"reason_codes":["signals_observed"]}`,
		`{"version":1,"eligible":false,"reason_codes":["unknown"]}`,
		`{"version":1,"eligible":false,"reason_codes":[null]}`,
		`{"version":1,"eligible":false,"reason_codes":["signals_observed","signals_observed"]}`,
		`{"version":1,"eligible":false,"reason_codes":["unsupported_rep_entries","signals_observed"]}`,
		`{"version":1,"eligible":true,"reason_codes":[],"extra":0}`,
		`{"version":1,"ELIGIBLE":true,"reason_codes":[]}`,
		`{"version":1,"eligible":false,"eligible":true,"reason_codes":[]}`,
	} {
		var eligibility TimeEligibility
		if err := json.Unmarshal([]byte(invalid), &eligibility); err == nil {
			t.Fatal("ambiguous eligibility accepted", invalid)
		}
	}
}
