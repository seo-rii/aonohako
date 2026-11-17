package durante

import (
	"math"
	"strings"
	"testing"
)

func judgeConfig() Config {
	image := "sha256:" + strings.Repeat("a", 64)
	return Config{
		Families: []string{"cpp"}, DispatchProfile: DispatchDefault, RuntimeImage: image,
		ResourcePolicy: &ResourcePolicy{
			MaxLimits:      ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 10000, MemoryMB: 512},
			MemoryBudgetMB: 4096, ReservedMemoryMB: 512,
		},
		TimePolicy: &TimePolicy{
			ModelID: "unit", ModelVersion: 1, ReferenceID: "unit", RuntimeID: "CPP20@" + image,
			Family: "cpp", EngineProfile: EngineProfile, DispatchProfile: DispatchDefault,
			InstructionNSNumerator: 1, REPByteNSNumerator: 1, NSDenominator: 1,
		},
	}
}

func TestParseJudgePolicy(t *testing.T) {
	off, err := ParseJudgePolicy("off", "", "", "")
	if err != nil || off.Enabled || off.Integrity != JudgeIntegrityOff {
		t.Fatalf("off: %+v %v", off, err)
	}
	empty, err := ParseJudgePolicy("", "cpp", "", "")
	if err != nil || empty.Enabled {
		t.Fatalf("empty mode must disable regardless of families: %+v %v", empty, err)
	}
	on, err := ParseJudgePolicy("on", "cpp, cpython", "plausibility", "50")
	if err != nil || !on.Enabled || len(on.Families) != 2 || on.Integrity != JudgeIntegrityPlausibility || on.IntegrityRatio != 50 {
		t.Fatalf("on: %+v %v", on, err)
	}
	for _, tc := range []struct{ mode, families, integrity, ratio string }{
		{"maybe", "cpp", "", ""},
		{"on", "", "", ""},
		{"on", "cpp,cpp", "", ""},
		{"on", "bogus", "", ""},
		{"on", "cpp", "strict", ""},
		{"on", "cpp", "plausibility", ""},
		{"on", "cpp", "plausibility", "0"},
		{"on", "cpp", "plausibility", "-3"},
		{"on", "cpp", "off", "50"},
	} {
		if _, err := ParseJudgePolicy(tc.mode, tc.families, tc.integrity, tc.ratio); err == nil {
			t.Fatalf("expected error for %+v", tc)
		}
	}
}

func TestJudgePolicyValidate(t *testing.T) {
	cfg := judgeConfig()
	if err := (JudgePolicy{Integrity: JudgeIntegrityOff}).Validate(cfg); err != nil {
		t.Fatalf("disabled policy should validate: %v", err)
	}
	good := JudgePolicy{Enabled: true, Families: []string{"cpp"}, Integrity: JudgeIntegrityOff}
	if err := good.Validate(cfg); err != nil {
		t.Fatalf("good policy: %v", err)
	}
	// Family not enabled as a measurement family.
	if err := (JudgePolicy{Enabled: true, Families: []string{"jvm"}, Integrity: JudgeIntegrityOff}).Validate(cfg); err == nil {
		t.Fatal("expected unenabled family rejection")
	}
	// Plausibility needs a ratio.
	if err := (JudgePolicy{Enabled: true, Families: []string{"cpp"}, Integrity: JudgeIntegrityPlausibility}).Validate(cfg); err == nil {
		t.Fatal("expected missing ratio rejection")
	}
	// Missing time model.
	noTime := cfg
	noTime.TimePolicy = nil
	if err := good.Validate(noTime); err == nil {
		t.Fatal("expected missing time model rejection")
	}
	// Missing resource policy.
	noRes := cfg
	noRes.ResourcePolicy = nil
	if err := good.Validate(noRes); err == nil {
		t.Fatal("expected missing resource policy rejection")
	}
	// glibc dispatch only judges cpp.
	glibc := cfg
	glibc.DispatchProfile = DispatchGLIBC241
	if err := (JudgePolicy{Enabled: true, Families: []string{"cpython"}, Integrity: JudgeIntegrityOff}).Validate(glibc); err == nil {
		t.Fatal("expected dispatch/family rejection")
	}
}

func boolp(b bool) *bool     { return &b }
func uintp(v uint64) *uint64 { return &v }

func estimate(ns int64) *TimeEstimate {
	ms := ns / 1_000_000
	if ns%1_000_000 != 0 {
		ms++
	}
	return &TimeEstimate{Nanoseconds: ns, Milliseconds: ms, Authority: Authority}
}

func TestJudgeDecideVerdicts(t *testing.T) {
	off := JudgePolicy{Enabled: true, Families: []string{"cpp"}, Integrity: JudgeIntegrityOff}
	for _, tc := range []struct {
		name    string
		in      JudgeInputs
		verdict JudgeVerdict
		flagged bool
	}{
		{"accepted below", JudgeInputs{State: "complete", OutputMatches: boolp(true), Estimate: estimate(500), NominalTimeExceeded: boolp(false)}, JudgeAccepted, false},
		{"accepted equal", JudgeInputs{State: "complete", OutputMatches: boolp(true), Estimate: estimate(1_000_000), NominalTimeExceeded: boolp(false)}, JudgeAccepted, false},
		{"tle over", JudgeInputs{State: "complete", OutputMatches: boolp(true), Estimate: estimate(2_000_000), NominalTimeExceeded: boolp(true)}, JudgeTimeLimitExceeded, false},
		{"wrong answer", JudgeInputs{State: "complete", OutputMatches: boolp(false)}, JudgeWrongAnswer, false},
		{"unconvertible", JudgeInputs{State: "complete", OutputMatches: boolp(true), Estimate: nil, NominalTimeExceeded: nil}, JudgeRuntimeError, false},
		{"resource exhausted tle", JudgeInputs{State: "resource_exhausted"}, JudgeTimeLimitExceeded, false},
		{"incomplete re", JudgeInputs{State: "incomplete"}, JudgeRuntimeError, false},
		{"failed re", JudgeInputs{State: "failed"}, JudgeRuntimeError, false},
		{"cancelled re", JudgeInputs{State: "cancelled"}, JudgeRuntimeError, false},
		{"unknown state re", JudgeInputs{State: "mystery"}, JudgeRuntimeError, false},
		{"missing output re", JudgeInputs{State: "complete", OutputMatches: nil}, JudgeRuntimeError, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := off.Decide(tc.in)
			if got.Verdict != tc.verdict || got.IntegrityFlagged != tc.flagged {
				t.Fatalf("got %q flagged=%v, want %q flagged=%v (%s)", got.Verdict, got.IntegrityFlagged, tc.verdict, tc.flagged, got.Reason)
			}
		})
	}
}

func TestJudgeDecidePlausibility(t *testing.T) {
	policy := JudgePolicy{Enabled: true, Families: []string{"cpp"}, Integrity: JudgeIntegrityPlausibility, IntegrityRatio: 50}
	base := JudgeInputs{State: "complete", OutputMatches: boolp(true), Estimate: estimate(1000), NominalTimeExceeded: boolp(false)}
	// raw CPU within ratio * estimate (50 * 1000 = 50000): accepted.
	ok := base
	ok.RawCPUTimeNs = uintp(50_000)
	if got := policy.Decide(ok); got.Verdict != JudgeAccepted || got.IntegrityFlagged {
		t.Fatalf("plausible should pass: %+v", got)
	}
	// raw CPU above the bound: flagged runtime error, never accepted.
	bad := base
	bad.RawCPUTimeNs = uintp(50_001)
	if got := policy.Decide(bad); got.Verdict != JudgeRuntimeError || !got.IntegrityFlagged {
		t.Fatalf("implausible should be flagged RE: %+v", got)
	}
	// A would-be TLE that is implausible is still RE (never silently accepted).
	tle := JudgeInputs{State: "complete", OutputMatches: boolp(true), Estimate: estimate(1000), NominalTimeExceeded: boolp(true), RawCPUTimeNs: uintp(50_001)}
	if got := policy.Decide(tle); got.Verdict != JudgeRuntimeError || !got.IntegrityFlagged {
		t.Fatalf("implausible tle should be flagged RE: %+v", got)
	}
	// Missing raw CPU under plausibility: runtime error, not accepted.
	missing := base
	missing.RawCPUTimeNs = nil
	if got := policy.Decide(missing); got.Verdict != JudgeRuntimeError || got.IntegrityFlagged {
		t.Fatalf("missing raw cpu should be RE: %+v", got)
	}
	// A huge ratio must not overflow.
	huge := JudgePolicy{Enabled: true, Families: []string{"cpp"}, Integrity: JudgeIntegrityPlausibility, IntegrityRatio: math.MaxUint64}
	big := base
	big.RawCPUTimeNs = uintp(math.MaxUint64)
	if got := huge.Decide(big); got.Verdict != JudgeAccepted {
		t.Fatalf("huge ratio should not overflow to flagged: %+v", got)
	}
}
