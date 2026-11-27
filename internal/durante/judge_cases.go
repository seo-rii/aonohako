package durante

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"aonohako/internal/runtimeidentity"
	"aonohako/internal/runtimepolicy"
)

const (
	maxJudgeCasesBytes = 64 << 10
	maxJudgeCases      = 256
)

type judgeCaseKey struct {
	problemID, language, stdinSHA256 string
	timeMS                           int
}

// JudgeCases binds each registered case and deadline to exactly one model.
// It is immutable after parsing and can be shared across executor snapshots.
type JudgeCases struct {
	bindings map[judgeCaseKey]string
}

func ParseJudgeCases(raw string) (*JudgeCases, error) {
	if len(raw) > maxJudgeCasesBytes || !utf8.ValidString(raw) {
		return nil, fmt.Errorf("invalid judge cases encoding or size (maximum %d bytes)", maxJudgeCasesBytes)
	}
	var schema int
	var cases []json.RawMessage
	if err := decodeTimeModelFields([]byte(raw), "judge cases", map[string]any{"schema": &schema, "cases": &cases}); err != nil {
		return nil, err
	}
	if schema != 1 || len(cases) == 0 || len(cases) > maxJudgeCases {
		return nil, fmt.Errorf("judge cases requires schema 1 and 1..%d cases", maxJudgeCases)
	}
	registry := &JudgeCases{bindings: make(map[judgeCaseKey]string, len(cases))}
	for i, entry := range cases {
		var key judgeCaseKey
		var binding string
		if err := decodeTimeModelFields(entry, "judge case", map[string]any{
			"problem_id": &key.problemID, "source_language": &key.language, "stdin_sha256": &key.stdinSHA256,
			"time_ms": &key.timeMS, "model_binding": &binding,
		}); err != nil {
			return nil, fmt.Errorf("case %d: %w", i, err)
		}
		if key.problemID == "" || runtimepolicy.ValidateProblemID(key.problemID) != nil {
			return nil, fmt.Errorf("case %d requires a valid nonempty problem_id", i)
		}
		if key.language != "CPP20" || key.timeMS <= 0 {
			return nil, fmt.Errorf("case %d requires source_language CPP20 and positive time_ms", i)
		}
		if !runtimeidentity.ValidImageDigest("sha256:" + key.stdinSHA256) {
			return nil, fmt.Errorf("case %d requires lowercase hex stdin_sha256", i)
		}
		digest, ok := strings.CutPrefix(binding, "work-clock-v1:")
		if !ok || !runtimeidentity.ValidImageDigest(digest) {
			return nil, fmt.Errorf("case %d requires a work-clock-v1:sha256 model_binding", i)
		}
		if _, exists := registry.bindings[key]; exists {
			return nil, fmt.Errorf("duplicate judge case at index %d", i)
		}
		registry.bindings[key] = binding
	}
	return registry, nil
}

// Allows requires the same operator model used to qualify the exact input.
func (r *JudgeCases) Allows(problemID, sourceLanguage, stdinSHA256 string, timeMS int, modelBinding string) bool {
	if r == nil {
		return false
	}
	binding, ok := r.bindings[judgeCaseKey{problemID, sourceLanguage, stdinSHA256, timeMS}]
	return ok && binding == modelBinding
}

// ValidateJudgeCases checks the scope against the deployment's resolved live
// clocks, including its image, CPU profile and effective runtime options.
func (c Config) ValidateJudgeCases() error {
	if c.JudgeCases == nil {
		return nil
	}
	if !c.Judge.JudgesFamily("cpp") || c.Judge.Integrity != JudgeIntegrityOff || c.TimeModels == nil || c.TimeModels.Schema != 2 || c.ResourcePolicy == nil {
		return fmt.Errorf("judge cases requires a cpp judge in on or shadow mode, integrity off, schema 2 time models and a resource policy")
	}
	limits := c.ResourcePolicy.MaxLimits
	liveBindings := make(map[string]bool)
	for _, profile := range c.TimeModels.Profiles {
		if profile.SourceLanguage != "CPP20" || profile.RuntimeOptions == nil || profile.RuntimeOptions.MemoryMB > limits.MemoryMB {
			continue
		}
		policy, _, err := c.TimePolicyForOptions("CPP20", *profile.RuntimeOptions)
		if err == nil && (policy.Formula() == TimeMethod || policy.Formula() == TimeMethodREPBreakdown) {
			liveBindings[(WorkClockRequest{TimePolicy: &policy}).ModelBinding()] = true
		}
	}
	for key, binding := range c.JudgeCases.bindings {
		if !liveBindings[binding] {
			return fmt.Errorf("judge case for %q at %d ms has no matching live CPP20 model_binding in this deployment", key.problemID, key.timeMS)
		}
		if key.timeMS >= limits.CPUTimeMs || key.timeMS >= limits.WallTimeMs {
			return fmt.Errorf("judge case for %q at %d ms requires larger raw CPU and wall limits", key.problemID, key.timeMS)
		}
	}
	return nil
}
