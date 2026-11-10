package durante

import (
	"fmt"
	"math/big"
	"strings"
)

// TimeMethod identifies this conversion formula, not a calibrated machine or
// a claim that the counters are trusted hardware-retired instructions.
const TimeMethod = "weighted-work-nanoseconds-v1"

// TimePolicy is an operator-supplied, versioned diagnostic conversion. There is
// deliberately no default policy or default weight. ReferenceID identifies the
// operator's reference/calibration record; supplying it does not validate that
// calibration. A change to weights or their applicable environment requires a
// new model version.
//
// The formula is ceil((instructions * InstructionNSNumerator +
// rep_bytes * REPByteNSNumerator) / NSDenominator). Both weights must be positive
// so a configured model cannot silently discard either measured work dimension.
// REP byte cost is additive to the instruction count, which already includes
// each instrumented REP instruction entry.
//
// RuntimeID is an exact, case-sensitive identity supplied by the caller from
// trusted execution metadata, ideally including its immutable runtime build.
// Family alone is insufficient to identify a compatible runtime. Report does
// not attest RuntimeID or the target's libc, so this policy cannot do so either.
type TimePolicy struct {
	ModelID                string `json:"model_id"`
	ModelVersion           uint64 `json:"model_version"`
	ReferenceID            string `json:"reference_id"`
	RuntimeID              string `json:"runtime_id"`
	Family                 string `json:"family"`
	EngineProfile          string `json:"engine_profile"`
	DispatchProfile        string `json:"dispatch_profile"`
	InstructionNSNumerator uint64 `json:"instruction_ns_numerator"`
	REPByteNSNumerator     uint64 `json:"rep_byte_ns_numerator"`
	NSDenominator          uint64 `json:"ns_denominator"`
}

// TimeEstimate is a candidate reference-time value, never a verdict or an
// enforceable CPU limit. Its diagnostic authority is unchanged by conversion.
// Nanoseconds is rounded up once after summing both exact rational terms;
// Milliseconds is the same total rounded up to whole milliseconds.
type TimeEstimate struct {
	InstructionNSNumerator uint64 `json:"instruction_ns_numerator"`
	REPByteNSNumerator     uint64 `json:"rep_byte_ns_numerator"`
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
	Nanoseconds            int64  `json:"nanoseconds"`
	Milliseconds           int64  `json:"milliseconds"`
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
	if p.InstructionNSNumerator == 0 || p.REPByteNSNumerator == 0 || p.NSDenominator == 0 {
		return fmt.Errorf("durante time policy requires positive explicit weights and denominator")
	}
	return nil
}

// Estimate rejects missing, incomplete, interrupted, incompatible or overflowing
// input instead of returning an apparent zero or falling back to CPU time. The
// caller must preserve an existing failure verdict independently of this value.
// Even a complete report can be forged by the measured process: validation here
// establishes structural compatibility, not adversarial integrity.
func (p TimePolicy) Estimate(runtimeID string, report Report) (TimeEstimate, error) {
	if err := p.Validate(); err != nil {
		return TimeEstimate{}, err
	}
	if runtimeID != p.RuntimeID || report.Family != p.Family || report.EngineProfile != p.EngineProfile || report.DispatchProfile != p.DispatchProfile {
		return TimeEstimate{}, fmt.Errorf("durante time policy runtime or profile mismatch")
	}
	if report.Schema != 1 || report.ClientVersion != 1 || report.Metric != Metric || report.Scope != Scope || report.Authority != Authority {
		return TimeEstimate{}, fmt.Errorf("durante time policy report contract mismatch")
	}
	if p.DispatchProfile == DispatchGLIBC241 && report.HostLibc != "2.41" {
		return TimeEstimate{}, fmt.Errorf("durante time policy host libc mismatch")
	}
	if !report.Complete || report.Reason != "" || report.Quality != "uninterrupted" || report.SignalsObserved != 0 {
		return TimeEstimate{}, fmt.Errorf("durante time policy requires complete uninterrupted accounting")
	}
	if report.Processes <= 0 || report.Images < report.Processes || !report.REPEnabled || report.Instructions == nil || report.REPBytes == nil || report.REPEntries == nil || report.UnsupportedREPEntries == nil {
		return TimeEstimate{}, fmt.Errorf("durante time policy requires complete process and REP counters")
	}
	if *report.Instructions == 0 || *report.UnsupportedREPEntries != 0 || (*report.REPEntries == 0 && *report.REPBytes != 0) {
		return TimeEstimate{}, fmt.Errorf("durante time policy has invalid or unsupported counters")
	}

	// Each input is uint64, so the sum needs at most 129 bits. Exact bounded
	// integer arithmetic avoids overflow and loss above float64's 53-bit range.
	var total, rep, weight, denominator, remainder big.Int
	total.SetUint64(*report.Instructions)
	total.Mul(&total, weight.SetUint64(p.InstructionNSNumerator))
	rep.SetUint64(*report.REPBytes)
	rep.Mul(&rep, weight.SetUint64(p.REPByteNSNumerator))
	total.Add(&total, &rep)
	total.QuoRem(&total, denominator.SetUint64(p.NSDenominator), &remainder)
	if remainder.Sign() != 0 {
		total.Add(&total, big.NewInt(1))
	}
	if !total.IsInt64() {
		return TimeEstimate{}, fmt.Errorf("durante time estimate exceeds int64 nanoseconds")
	}
	nanoseconds := total.Int64()
	milliseconds := nanoseconds / 1_000_000
	if nanoseconds%1_000_000 != 0 {
		milliseconds++
	}
	return TimeEstimate{
		InstructionNSNumerator: p.InstructionNSNumerator,
		REPByteNSNumerator:     p.REPByteNSNumerator, NSDenominator: p.NSDenominator,
		Method: TimeMethod, Authority: Authority,
		ModelID: p.ModelID, ModelVersion: p.ModelVersion, ReferenceID: p.ReferenceID,
		RuntimeID: p.RuntimeID, Family: p.Family,
		EngineProfile: p.EngineProfile, DispatchProfile: p.DispatchProfile,
		Nanoseconds: nanoseconds, Milliseconds: milliseconds,
	}, nil
}
