package durante

import (
	"math"
	"testing"
)

func timePolicyFixture() TimePolicy {
	// Synthetic arithmetic-test weights, not a calibrated deployment policy.
	return TimePolicy{
		ModelID: "arithmetic-test", ModelVersion: 1, ReferenceID: "synthetic-test-only",
		RuntimeID: "CPP23@runtime-image-test", Family: "cpp",
		EngineProfile: EngineProfile, DispatchProfile: DispatchDefault,
		InstructionNSNumerator: 1, REPByteNSNumerator: 1, NSDenominator: 1,
	}
}

func timeCounter(value uint64) *uint64 { return &value }

func timeReportFixture(instructions, repBytes uint64) Report {
	repEntries := uint64(0)
	if repBytes != 0 {
		repEntries = 1
	}
	return Report{
		Schema: 1, ClientVersion: 1, Metric: Metric, Scope: Scope, Authority: Authority,
		Complete: true, Quality: "uninterrupted", REPEnabled: true,
		Instructions: timeCounter(instructions), REPBytes: timeCounter(repBytes),
		REPEntries: timeCounter(repEntries), UnsupportedREPEntries: timeCounter(0),
		Processes: 1, Images: 1, Family: "cpp", EngineProfile: EngineProfile,
		DispatchProfile: DispatchDefault,
	}
}

func TestTimePolicyRequiresExplicitCompatibleConfiguration(t *testing.T) {
	if err := (TimePolicy{}).Validate(); err == nil {
		t.Fatal("zero policy must not supply an implicit mapping")
	}
	tests := map[string]func(*TimePolicy){
		"model":              func(p *TimePolicy) { p.ModelID = "" },
		"model whitespace":   func(p *TimePolicy) { p.ModelID = " model " },
		"model version":      func(p *TimePolicy) { p.ModelVersion = 0 },
		"reference":          func(p *TimePolicy) { p.ReferenceID = "" },
		"reference NUL":      func(p *TimePolicy) { p.ReferenceID = "ref\x00" },
		"runtime":            func(p *TimePolicy) { p.RuntimeID = "" },
		"family":             func(p *TimePolicy) { p.Family = "unknown" },
		"engine":             func(p *TimePolicy) { p.EngineProfile = "another-engine" },
		"dispatch missing":   func(p *TimePolicy) { p.DispatchProfile = "" },
		"dispatch unknown":   func(p *TimePolicy) { p.DispatchProfile = "another-dispatch" },
		"instruction weight": func(p *TimePolicy) { p.InstructionNSNumerator = 0 },
		"REP weight":         func(p *TimePolicy) { p.REPByteNSNumerator = 0 },
		"denominator":        func(p *TimePolicy) { p.NSDenominator = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy := timePolicyFixture()
			mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("invalid policy accepted")
			}
			got, err := policy.Estimate(policy.RuntimeID, timeReportFixture(1, 0))
			if err == nil || got != (TimeEstimate{}) {
				t.Fatalf("invalid policy returned a time: %+v, %v", got, err)
			}
		})
	}
}

func TestTimeEstimateExactArithmeticAndRounding(t *testing.T) {
	tests := []struct {
		name                     string
		instructions, repBytes   uint64
		instructionNS, repByteNS uint64
		denominator              uint64
		wantNS, wantMS           int64
	}{
		{"sum before rounding", 1, 1, 1, 1, 2, 1, 1},
		{"weighted REP", 3, 2, 2, 3, 4, 3, 1},
		{"fractional ns", 1, 0, 1, 1, 3, 1, 1},
		{"large denominator", 1, 0, 1, 1, math.MaxUint64, 1, 1},
		{"below ms", 999_999, 0, 1, 1, 1, 999_999, 1},
		{"exact ms", 1_000_000, 0, 1, 1, 1, 1_000_000, 1},
		{"above ms", 1_000_001, 0, 1, 1, 1, 1_000_001, 2},
		{"fraction above ms", 2_000_001, 0, 1, 1, 2, 1_000_001, 2},
		{"above float precision", 1<<53 + 1, 0, 1, 1, 1, 9_007_199_254_740_993, 9_007_199_255},
		{"wide product with representable result", math.MaxInt64, 0, math.MaxUint64, 1, math.MaxUint64, math.MaxInt64, 9_223_372_036_855},
		{"wide sum with representable result", math.MaxInt64, math.MaxInt64, 1, 1, 2, math.MaxInt64, 9_223_372_036_855},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := timePolicyFixture()
			policy.InstructionNSNumerator = tt.instructionNS
			policy.REPByteNSNumerator = tt.repByteNS
			policy.NSDenominator = tt.denominator
			report := timeReportFixture(tt.instructions, tt.repBytes)
			got, err := policy.Estimate(policy.RuntimeID, report)
			if err != nil || got.Nanoseconds != tt.wantNS || got.Milliseconds != tt.wantMS {
				t.Fatalf("got %+v, %v; want %d ns / %d ms", got, err, tt.wantNS, tt.wantMS)
			}
			if got.Method != TimeMethod || got.Authority != Authority || got.ModelID != policy.ModelID || got.ModelVersion != policy.ModelVersion || got.ReferenceID != policy.ReferenceID || got.RuntimeID != policy.RuntimeID || got.Family != policy.Family || got.EngineProfile != policy.EngineProfile || got.DispatchProfile != policy.DispatchProfile {
				t.Fatalf("conversion lost its diagnostic provenance: %+v", got)
			}
			if *report.Instructions != tt.instructions || *report.REPBytes != tt.repBytes {
				t.Fatal("conversion changed input counters")
			}
		})
	}
}

func TestTimeEstimateRejectsOverflow(t *testing.T) {
	tests := []struct {
		name                     string
		instructions, repBytes   uint64
		instructionNS, repByteNS uint64
		denominator              uint64
	}{
		{"final int64 overflow", math.MaxUint64, 0, 1, 1, 1},
		{"rounding over int64", math.MaxUint64, 0, 1, 1, 2},
		{"129-bit numerator", math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := timePolicyFixture()
			policy.InstructionNSNumerator = tt.instructionNS
			policy.REPByteNSNumerator = tt.repByteNS
			policy.NSDenominator = tt.denominator
			got, err := policy.Estimate(policy.RuntimeID, timeReportFixture(tt.instructions, tt.repBytes))
			if err == nil || got != (TimeEstimate{}) {
				t.Fatalf("overflow returned a time: %+v, %v", got, err)
			}
		})
	}
}

func TestTimeEstimateRejectsInvalidAndIncompleteReports(t *testing.T) {
	tests := map[string]func(*Report){
		"schema":                  func(r *Report) { r.Schema++ },
		"client":                  func(r *Report) { r.ClientVersion++ },
		"metric":                  func(r *Report) { r.Metric = "hardware-instructions" },
		"scope":                   func(r *Report) { r.Scope = "single-process" },
		"authority":               func(r *Report) { r.Authority = "trusted" },
		"incomplete":              func(r *Report) { r.Complete = false },
		"reason":                  func(r *Report) { r.Reason = "missing process" },
		"signal quality":          func(r *Report) { r.Quality = "signals_observed" },
		"signal count":            func(r *Report) { r.SignalsObserved = 1 },
		"no processes":            func(r *Report) { r.Processes = 0 },
		"missing process image":   func(r *Report) { r.Images = 0 },
		"REP disabled":            func(r *Report) { r.REPEnabled = false },
		"missing instructions":    func(r *Report) { r.Instructions = nil },
		"missing REP bytes":       func(r *Report) { r.REPBytes = nil },
		"missing REP entries":     func(r *Report) { r.REPEntries = nil },
		"missing unsupported REP": func(r *Report) { r.UnsupportedREPEntries = nil },
		"zero instructions":       func(r *Report) { r.Instructions = timeCounter(0) },
		"unsupported REP":         func(r *Report) { r.UnsupportedREPEntries = timeCounter(1) },
		"REP bytes without entry": func(r *Report) { r.REPEntries = timeCounter(0) },
		"family":                  func(r *Report) { r.Family = "python" },
		"engine":                  func(r *Report) { r.EngineProfile = "other-engine" },
		"dispatch":                func(r *Report) { r.DispatchProfile = DispatchGLIBC241 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy := timePolicyFixture()
			report := timeReportFixture(100, 8)
			mutate(&report)
			got, err := policy.Estimate(policy.RuntimeID, report)
			if err == nil || got != (TimeEstimate{}) {
				t.Fatalf("invalid report returned a time: %+v, %v", got, err)
			}
		})
	}
}

func TestTimeEstimateBindsRuntimeAndDispatch(t *testing.T) {
	policy := timePolicyFixture()
	report := timeReportFixture(10, 0)
	for _, runtimeID := range []string{"", "CPP23@different-image", "cpp23@runtime-image-test"} {
		if got, err := policy.Estimate(runtimeID, report); err == nil || got != (TimeEstimate{}) {
			t.Fatalf("runtime %q accepted: %+v, %v", runtimeID, got, err)
		}
	}
	policy.DispatchProfile = DispatchGLIBC241
	report.DispatchProfile = DispatchGLIBC241
	for _, host := range []string{"", "2.40", "2.42"} {
		report.HostLibc = host
		if got, err := policy.Estimate(policy.RuntimeID, report); err == nil || got != (TimeEstimate{}) {
			t.Fatalf("host libc %q accepted: %+v, %v", host, got, err)
		}
	}
	report.HostLibc = "2.41"
	got, err := policy.Estimate(policy.RuntimeID, report)
	if err != nil || got.Authority != Authority || report.TargetLibcAttested {
		t.Fatalf("host compatibility must not imply target attestation: %+v, %v", got, err)
	}
}

func TestTimeEstimateUsesEntireCollectedProcessTree(t *testing.T) {
	report, err := aggregate(basicTree(), 10)
	if err != nil {
		t.Fatal(err)
	}
	policy := timePolicyFixture()
	report.Family, report.EngineProfile, report.DispatchProfile = policy.Family, policy.EngineProfile, policy.DispatchProfile
	got, err := policy.Estimate(policy.RuntimeID, report)
	if err != nil || got.Nanoseconds != 300 || report.Processes != 2 {
		t.Fatalf("expected combined parent and child work, got %+v, %v", got, err)
	}
}

func TestTimePolicyRejectsPreStabilizationEngineProfile(t *testing.T) {
	policy := timePolicyFixture()
	policy.EngineProfile = "dynamorio-11.3-vm32m-v1"
	if err := policy.Validate(); err == nil {
		t.Fatal("old runtime calibration survived patched engine profile")
	}
	policy = timePolicyFixture()
	report := timeReportFixture(100, 0)
	report.EngineProfile = "dynamorio-11.3-vm32m-v1"
	if _, err := policy.Estimate(policy.RuntimeID, report); err == nil {
		t.Fatal("old runtime report accepted under patched calibration")
	}
}
