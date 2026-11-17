package durante

import (
	"fmt"
	"math/big"
	"strings"
)

// Judge mode turns a completed diagnostic observation into an actual verdict.
// It is operator-gated and opt-in per request; see docs/durante-judging.md. The
// counts remain application-mutable: this is a verdict policy over the existing
// envelope and time conversion, not a trusted measurement backend.

type JudgeIntegrity string

const (
	// JudgeIntegrityOff trusts the reported counts. JudgeIntegrityPlausibility
	// additionally requires the instrumented run's own raw CPU time to be
	// consistent with the converted work, withholding a verdict otherwise.
	JudgeIntegrityOff          JudgeIntegrity = "off"
	JudgeIntegrityPlausibility JudgeIntegrity = "plausibility"
)

// JudgeVerdict is this package's verdict enum. The execute layer maps it to the
// public run-status strings; keeping it separate avoids importing model here.
type JudgeVerdict string

const (
	JudgeAccepted          JudgeVerdict = "accepted"
	JudgeWrongAnswer       JudgeVerdict = "wrong_answer"
	JudgeTimeLimitExceeded JudgeVerdict = "time_limit_exceeded"
	JudgeRuntimeError      JudgeVerdict = "runtime_error"
)

// JudgePolicy is the operator's judge-mode configuration. Enabling it does not
// enable any family on its own; a judged family must also be an enabled,
// qualified measurement family.
type JudgePolicy struct {
	// Enabled configures the judge machinery: shadow comparisons may run.
	Enabled bool
	// VerdictsAllowed additionally lets work_measurement_judge requests decide
	// the returned verdict. Mode "shadow" enables only comparisons; "on" both.
	VerdictsAllowed bool
	// Families lists measurement families for which a request may ask for a
	// verdict. A measured family absent here cannot be judged.
	Families  []string
	Integrity JudgeIntegrity
	// IntegrityRatio is the maximum believable ratio of instrumented raw CPU
	// nanoseconds to converted virtual nanoseconds. Only used for plausibility.
	IntegrityRatio uint64
}

// ParseJudgePolicy reads the four judge environment variables. An empty or
// "off" mode disables judge mode and ignores the other values. "shadow" runs
// only non-authoritative comparisons; "on" also allows judge verdicts.
func ParseJudgePolicy(mode, families, integrity, ratio string) (JudgePolicy, error) {
	verdicts := false
	switch strings.TrimSpace(mode) {
	case "", "off":
		return JudgePolicy{Integrity: JudgeIntegrityOff}, nil
	case "shadow":
	case "on":
		verdicts = true
	default:
		return JudgePolicy{}, fmt.Errorf("judge mode must be \"on\", \"shadow\" or \"off\"")
	}
	policy := JudgePolicy{Enabled: true, VerdictsAllowed: verdicts, Integrity: JudgeIntegrityOff}
	for _, name := range strings.Split(families, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			policy.Families = append(policy.Families, trimmed)
		}
	}
	if len(policy.Families) == 0 {
		return JudgePolicy{}, fmt.Errorf("judge mode requires at least one judged family")
	}
	if err := ValidateFamilies(policy.Families); err != nil {
		return JudgePolicy{}, fmt.Errorf("judge mode families: %w", err)
	}
	for i, name := range policy.Families {
		for _, earlier := range policy.Families[:i] {
			if earlier == name {
				return JudgePolicy{}, fmt.Errorf("duplicate judge mode family %q", name)
			}
		}
	}
	switch JudgeIntegrity(strings.TrimSpace(integrity)) {
	case "", JudgeIntegrityOff:
		policy.Integrity = JudgeIntegrityOff
	case JudgeIntegrityPlausibility:
		policy.Integrity = JudgeIntegrityPlausibility
	default:
		return JudgePolicy{}, fmt.Errorf("judge integrity must be \"off\" or \"plausibility\"")
	}
	if policy.Integrity == JudgeIntegrityPlausibility {
		parsed, err := parsePositiveUint(ratio)
		if err != nil {
			return JudgePolicy{}, fmt.Errorf("judge integrity ratio: %w", err)
		}
		policy.IntegrityRatio = parsed
	} else if strings.TrimSpace(ratio) != "" {
		return JudgePolicy{}, fmt.Errorf("judge integrity ratio requires the plausibility mode")
	}
	return policy, nil
}

func parsePositiveUint(raw string) (uint64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("a positive integer is required")
	}
	var value uint64
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("must be a positive integer")
		}
		next := value*10 + uint64(r-'0')
		if next < value {
			return 0, fmt.Errorf("value is too large")
		}
		value = next
	}
	if value == 0 {
		return 0, fmt.Errorf("must be greater than zero")
	}
	return value, nil
}

// JudgesFamily reports whether this policy admits a verdict for the family.
func (p JudgePolicy) JudgesFamily(family string) bool {
	if !p.Enabled {
		return false
	}
	for _, name := range p.Families {
		if name == family {
			return true
		}
	}
	return false
}

// Validate checks the policy against the enclosing measurement configuration:
// every judged family must be an enabled, qualified measurement family, and
// plausibility requires a positive ratio.
func (p JudgePolicy) Validate(cfg Config) error {
	if !p.Enabled {
		if len(p.Families) != 0 || p.IntegrityRatio != 0 || p.VerdictsAllowed {
			return fmt.Errorf("disabled judge mode cannot carry families, verdicts or an integrity ratio")
		}
		return nil
	}
	if len(p.Families) == 0 {
		return fmt.Errorf("judge mode requires at least one judged family")
	}
	for _, family := range p.Families {
		if err := ValidateMeasurementFamily(family); err != nil {
			return fmt.Errorf("judge mode family %q: %w", family, err)
		}
		enabled := false
		for _, name := range cfg.Families {
			enabled = enabled || name == family
		}
		if !enabled {
			return fmt.Errorf("judge mode family %q is not an enabled measurement family", family)
		}
		if cfg.DispatchProfile == DispatchGLIBC241 && family != "cpp" {
			return fmt.Errorf("judge mode family %q is incompatible with dispatch profile %s", family, cfg.DispatchProfile)
		}
	}
	switch p.Integrity {
	case JudgeIntegrityOff:
		if p.IntegrityRatio != 0 {
			return fmt.Errorf("judge integrity ratio requires the plausibility mode")
		}
	case JudgeIntegrityPlausibility:
		if p.IntegrityRatio == 0 {
			return fmt.Errorf("judge plausibility mode requires a positive integrity ratio")
		}
	default:
		return fmt.Errorf("unknown judge integrity mode %q", p.Integrity)
	}
	// Without a fixed virtual processor, counts follow each instance's CPU,
	// which is exactly the instability judge mode exists to remove.
	if cfg.CPUIDProfile == "" {
		return fmt.Errorf("judge mode requires a configured CPUID profile")
	}
	// Node, CPython and PyPy count differently across identical runs unless
	// their background compilation and per-process hash seed are pinned.
	for _, family := range p.Families {
		if (family == "cpython" || family == "pypy" || family == "javascript-wasm") && cfg.RuntimeDeterminism == "" {
			return fmt.Errorf("judge mode family %q requires runtime determinism", family)
		}
	}
	// Judge mode needs a resource envelope and a time model to produce a
	// virtual-millisecond verdict at all.
	if cfg.ResourcePolicy == nil {
		return fmt.Errorf("judge mode requires a configured resource policy")
	}
	if cfg.TimePolicy == nil && cfg.TimeModels == nil {
		return fmt.Errorf("judge mode requires a configured time policy or time models")
	}
	return nil
}

// JudgeResult is the computed verdict plus the inputs that produced it.
type JudgeResult struct {
	Verdict          JudgeVerdict
	Reason           string
	IntegrityMode    JudgeIntegrity
	IntegrityFlagged bool
	EstimateMs       *int64
	EstimateNs       *int64
}

// JudgeInputs are the finalized diagnostic facts the verdict is computed from.
// RawCPUTimeNs is the instrumented run's own scheduled CPU time, used only by
// the plausibility integrity check; it already includes instrumentation cost.
type JudgeInputs struct {
	State               string
	OutputMatches       *bool
	Estimate            *TimeEstimate
	NominalTimeExceeded *bool
	NominalTimeMs       int64
	RawCPUTimeNs        *uint64
}

// Decide applies the fixed verdict rules in docs/durante-judging.md. It never
// returns Accepted or Time Limit Exceeded for an unavailable, interrupted,
// forced-stopped or implausible observation.
func (p JudgePolicy) Decide(in JudgeInputs) JudgeResult {
	result := JudgeResult{IntegrityMode: p.Integrity}
	switch in.State {
	case "complete":
		if in.OutputMatches == nil {
			result.Verdict, result.Reason = JudgeRuntimeError, "output comparison unavailable"
			return result
		}
		if !*in.OutputMatches {
			result.Verdict, result.Reason = JudgeWrongAnswer, "output does not match expected"
			return result
		}
		if in.Estimate == nil || in.NominalTimeExceeded == nil {
			result.Verdict, result.Reason = JudgeRuntimeError, "work count could not be converted to a time estimate"
			return result
		}
		ms := in.Estimate.Milliseconds
		ns := in.Estimate.Nanoseconds
		result.EstimateMs, result.EstimateNs = &ms, &ns
		if p.Integrity == JudgeIntegrityPlausibility {
			if in.RawCPUTimeNs == nil {
				result.Verdict = JudgeRuntimeError
				result.Reason = "raw CPU time unavailable for the plausibility check"
				return result
			}
			if implausibleWork(*in.RawCPUTimeNs, ns, p.IntegrityRatio) {
				result.Verdict = JudgeRuntimeError
				result.IntegrityFlagged = true
				result.Reason = "reported work is implausibly small relative to measured raw CPU time"
				return result
			}
		}
		if *in.NominalTimeExceeded {
			result.Verdict = JudgeTimeLimitExceeded
			result.Reason = "estimated work time exceeds the limit"
			return result
		}
		result.Verdict, result.Reason = JudgeAccepted, "output matches and estimated work time is within the limit"
		return result
	case "resource_exhausted":
		result.Verdict = JudgeTimeLimitExceeded
		result.Reason = "execution exceeded the raw resource envelope"
		return result
	default: // incomplete, failed, cancelled, or any unknown state.
		result.Verdict = JudgeRuntimeError
		result.Reason = "diagnostic execution did not complete: " + in.State
		return result
	}
}

// implausibleWork reports rawCPUns > ratio * estimateNs using wide arithmetic,
// so a large ratio cannot overflow. A nonpositive estimate is never plausible.
func implausibleWork(rawCPUns uint64, estimateNs int64, ratio uint64) bool {
	if estimateNs <= 0 {
		return true
	}
	var bound, raw big.Int
	bound.SetInt64(estimateNs)
	bound.Mul(&bound, new(big.Int).SetUint64(ratio))
	raw.SetUint64(rawCPUns)
	return raw.Cmp(&bound) > 0
}
