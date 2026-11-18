package durante

import (
	"fmt"
	"math/big"
	"strings"
)

// TimeMethod identifies this conversion formula, not a calibrated machine or
// a claim that the counters are trusted hardware-retired instructions.
const TimeMethod = "weighted-work-nanoseconds-v1"
const TimeMethodREPBreakdown = "weighted-rep-operations-nanoseconds-v2"
const TimeMethodLatencyBoundREP = "latency-bound-rep-nanoseconds-v1"

// TimePolicy is an operator-supplied, versioned diagnostic conversion. There is
// deliberately no default policy or default weight. ReferenceID identifies the
// operator's reference/calibration record; supplying it does not validate that
// calibration. A change to weights or their applicable environment requires a
// new model version.
//
// V1 computes ceil((instructions * InstructionNSNumerator + rep_bytes *
// REPByteNSNumerator) / NSDenominator). V2 replaces the combined REP term with
// movs_bytes * MOVSByteNSNumerator + stos_bytes * STOSByteNSNumerator. Every
// selected weight must be positive. REP costs are additive to the instruction
// count, which already includes each instrumented REP instruction entry.
// The latency model replaces the instruction term with version 1 cycles_x16
// multiplied by LatencyX16NSNumerator. The denominator uses the same units as
// the fitted coefficients, so no additional factor of 16 is applied.
//
// RuntimeID is an exact, case-sensitive identity supplied by the caller from
// trusted execution metadata, ideally including its immutable runtime build.
// Family alone is insufficient to identify a compatible runtime. Report does
// not attest RuntimeID or the target's libc, so this policy cannot do so either.
type TimePolicy struct {
	runtimeOptionsID string // Set only by a validated schema 2 registry binding.
	Method           string `json:"method,omitempty"`
	ModelID          string `json:"model_id"`
	ModelVersion     uint64 `json:"model_version"`
	ReferenceID      string `json:"reference_id"`
	RuntimeID        string `json:"runtime_id"`
	Family           string `json:"family"`
	EngineProfile    string `json:"engine_profile"`
	DispatchProfile  string `json:"dispatch_profile"`
	// CPUIDProfile is the virtual processor the model was qualified under.
	// Empty means host CPUID; it then never applies to profiled counts.
	CPUIDProfile           string `json:"cpuid_profile,omitempty"`
	InstructionNSNumerator uint64 `json:"instruction_ns_numerator,omitempty"`
	LatencyX16NSNumerator  uint64 `json:"latency_x16_ns_numerator,omitempty"`
	REPByteNSNumerator     uint64 `json:"rep_byte_ns_numerator,omitempty"`
	MOVSByteNSNumerator    uint64 `json:"movs_byte_ns_numerator,omitempty"`
	STOSByteNSNumerator    uint64 `json:"stos_byte_ns_numerator,omitempty"`
	NSDenominator          uint64 `json:"ns_denominator"`
	// runtimeDeterminism is the option set the configuration applies to the
	// requested launcher; an observation must report the same one.
	runtimeDeterminism string
}

// TimeEstimate is a candidate reference-time value, never a verdict or an
// enforceable CPU limit. Its diagnostic authority is unchanged by conversion.
// Nanoseconds is rounded up once after summing all exact rational terms;
// Milliseconds is the same total rounded up to whole milliseconds.
type TimeEstimate struct {
	RuntimeOptionsID       string `json:"runtime_options_id,omitempty"`
	InstructionNSNumerator uint64 `json:"instruction_ns_numerator,omitempty"`
	LatencyX16NSNumerator  uint64 `json:"latency_x16_ns_numerator,omitempty"`
	REPByteNSNumerator     uint64 `json:"rep_byte_ns_numerator,omitempty"`
	MOVSByteNSNumerator    uint64 `json:"movs_byte_ns_numerator,omitempty"`
	STOSByteNSNumerator    uint64 `json:"stos_byte_ns_numerator,omitempty"`
	NSDenominator          uint64 `json:"ns_denominator"`
	Method                 string `json:"method"`
	Authority              string `json:"authority"`
	ModelID                string `json:"model_id"`
	ModelVersion           uint64 `json:"model_version"`
	ReferenceID            string `json:"reference_id"`
	RuntimeID              string `json:"runtime_id"`
	Family                 string `json:"family"`
	EngineProfile          string `json:"engine_profile"`
	DispatchProfile        string `json:"dispatch_profile"`
	CPUIDProfile           string `json:"cpuid_profile,omitempty"`
	RuntimeDeterminism     string `json:"runtime_determinism,omitempty"`
	Nanoseconds            int64  `json:"nanoseconds"`
	Milliseconds           int64  `json:"milliseconds"`
}

// WithRuntimeDeterminism returns a copy that expects observations made with
// the named runtime determinism option set (empty: none). Configuration
// resolution sets it from Config.RuntimeDeterminismFor; reference fitting sets
// it from the dataset's recorded value.
func (p TimePolicy) WithRuntimeDeterminism(value string) TimePolicy {
	p.runtimeDeterminism = value
	return p
}

// Formula preserves the original meaning of policies written before method
// selection existed. Adding operation-specific fields never selects v2 implicitly.
func (p TimePolicy) Formula() string {
	if p.Method == "" {
		return TimeMethod
	}
	return p.Method
}

func (p TimePolicy) Validate() error {
	for _, value := range []string{p.ModelID, p.ReferenceID, p.RuntimeID} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("durante time policy requires explicit model, reference and runtime identities")
		}
	}
	if p.ModelVersion == 0 {
		return fmt.Errorf("durante time policy requires an explicit model version")
	}
	if err := ValidateFamilies([]string{p.Family}); err != nil {
		return fmt.Errorf("durante time policy: %w", err)
	}
	if p.EngineProfile != EngineProfile {
		return fmt.Errorf("durante time policy has an unsupported engine profile")
	}
	switch p.DispatchProfile {
	case DispatchDefault:
	case DispatchGLIBC241:
		if p.Family != "cpp" {
			return fmt.Errorf("durante time policy dispatch profile requires the cpp family")
		}
	default:
		return fmt.Errorf("durante time policy requires a supported dispatch profile")
	}
	if p.CPUIDProfile != "" && !ValidCPUIDProfile(p.CPUIDProfile) {
		return fmt.Errorf("durante time policy has an unsupported CPUID profile")
	}
	return p.validateWeights()
}

func (p TimePolicy) validateWeights() error {
	if p.NSDenominator == 0 {
		return fmt.Errorf("durante time policy requires positive explicit weights and denominator")
	}
	switch p.Formula() {
	case TimeMethod, TimeMethodLatencyBoundREP:
		if p.REPByteNSNumerator == 0 || p.MOVSByteNSNumerator != 0 || p.STOSByteNSNumerator != 0 {
			return fmt.Errorf("time policy requires only the combined REP weight")
		}
	case TimeMethodREPBreakdown:
		if p.REPByteNSNumerator != 0 || p.MOVSByteNSNumerator == 0 || p.STOSByteNSNumerator == 0 {
			return fmt.Errorf("v2 time policy requires separate positive MOVS and STOS weights")
		}
	default:
		return fmt.Errorf("unsupported durante time method")
	}
	if p.Formula() == TimeMethodLatencyBoundREP {
		if p.LatencyX16NSNumerator == 0 || p.InstructionNSNumerator != 0 {
			return fmt.Errorf("latency time policy requires a positive latency weight and no instruction weight")
		}
	} else if p.InstructionNSNumerator == 0 || p.LatencyX16NSNumerator != 0 {
		return fmt.Errorf("instruction time policy requires a positive instruction weight and no latency weight")
	}
	return nil
}

// Estimate rejects missing, incomplete, interrupted, incompatible or overflowing
// input instead of returning an apparent zero or falling back to CPU time. The
// caller must preserve an existing failure verdict independently of this value.
// Even a complete report can be forged by the measured process: validation here
// establishes structural compatibility, not adversarial integrity.
func (p TimePolicy) Estimate(runtimeID string, report Report) (TimeEstimate, error) {
	estimate, _, err := p.EstimateWithEligibility(runtimeID, report)
	return estimate, err
}

// EstimateWithEligibility shares every admission check and the exact arithmetic
// with Estimate. A failed assessment always returns a zero estimate and an error.
func (p TimePolicy) EstimateWithEligibility(runtimeID string, report Report) (TimeEstimate, TimeEligibility, error) {
	if err := p.Validate(); err != nil {
		return TimeEstimate{}, timeEligibility(TimeIncompatibleConfiguration), err
	}
	if runtimeID != p.RuntimeID || report.Family != p.Family || report.EngineProfile != p.EngineProfile || report.DispatchProfile != p.DispatchProfile || report.CPUIDProfile != p.CPUIDProfile ||
		report.RuntimeDeterminism != p.runtimeDeterminism {
		return TimeEstimate{}, timeEligibility(TimeRuntimeProfileMismatch), fmt.Errorf("durante time policy runtime or profile mismatch")
	}
	if p.DispatchProfile == DispatchGLIBC241 && report.HostLibc != "2.41" {
		return TimeEstimate{}, timeEligibility(TimeHostLibcMismatch), fmt.Errorf("durante time policy host libc mismatch")
	}
	eligibility := report.TimeConversionEligibility(p.Formula())
	if !eligibility.Eligible {
		return TimeEstimate{}, eligibility, fmt.Errorf("durante time policy observation is not convertible: %s", strings.Join(eligibility.ReasonCodes, ", "))
	}
	// Sum the exact products before rounding. A v2 model partitions REP bytes
	// instead of charging both the old total and its operation-specific parts.
	var total, rep, weight, denominator, remainder big.Int
	work, workWeight := *report.Instructions, p.InstructionNSNumerator
	if p.Formula() == TimeMethodLatencyBoundREP {
		work, workWeight = report.LatencyBound.CyclesX16, p.LatencyX16NSNumerator
	}
	total.SetUint64(work)
	total.Mul(&total, weight.SetUint64(workWeight))
	terms := [][2]uint64{{*report.REPBytes, p.REPByteNSNumerator}}
	if p.Formula() == TimeMethodREPBreakdown {
		terms = [][2]uint64{{report.REPBreakdown.MOVSBytes, p.MOVSByteNSNumerator}, {report.REPBreakdown.STOSBytes, p.STOSByteNSNumerator}}
	}
	for _, term := range terms {
		rep.SetUint64(term[0])
		rep.Mul(&rep, weight.SetUint64(term[1]))
		total.Add(&total, &rep)
	}
	total.QuoRem(&total, denominator.SetUint64(p.NSDenominator), &remainder)
	if remainder.Sign() != 0 {
		total.Add(&total, big.NewInt(1))
	}
	if !total.IsInt64() {
		return TimeEstimate{}, timeEligibility(TimeNumericOverflow), fmt.Errorf("durante time estimate exceeds int64 nanoseconds")
	}
	nanoseconds := total.Int64()
	milliseconds := nanoseconds / 1_000_000
	if nanoseconds%1_000_000 != 0 {
		milliseconds++
	}
	return TimeEstimate{
		RuntimeOptionsID:       p.runtimeOptionsID,
		InstructionNSNumerator: p.InstructionNSNumerator,
		LatencyX16NSNumerator:  p.LatencyX16NSNumerator,
		REPByteNSNumerator:     p.REPByteNSNumerator, NSDenominator: p.NSDenominator,
		MOVSByteNSNumerator: p.MOVSByteNSNumerator, STOSByteNSNumerator: p.STOSByteNSNumerator,
		Method: p.Formula(), Authority: Authority,
		ModelID: p.ModelID, ModelVersion: p.ModelVersion, ReferenceID: p.ReferenceID,
		RuntimeID: p.RuntimeID, Family: p.Family,
		EngineProfile: p.EngineProfile, DispatchProfile: p.DispatchProfile,
		CPUIDProfile: p.CPUIDProfile, RuntimeDeterminism: p.runtimeDeterminism,
		Nanoseconds: nanoseconds, Milliseconds: milliseconds,
	}, eligibility, nil
}
