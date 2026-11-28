package execute

import (
	"context"
	"fmt"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// judgeVerdictStatus maps the durante verdict enum to a public run status.
func judgeVerdictStatus(v durante.JudgeVerdict) string {
	switch v {
	case durante.JudgeAccepted:
		return model.RunStatusAccepted
	case durante.JudgeWrongAnswer:
		return model.RunStatusWA
	case durante.JudgeTimeLimitExceeded:
		return model.RunStatusTLE
	case durante.JudgeUnavailable:
		return model.RunStatusInitFail
	case durante.JudgeRuntimeError:
		return model.RunStatusRE
	default:
		return model.RunStatusRE
	}
}

// judgeRawCPUTimeNs is the instrumented run's own scheduled CPU time, used by
// the plausibility integrity check. Judge mode runs with normalization off, so
// the authoritative CPUTimeNs is already raw; prefer an explicit raw field.
func judgeRawCPUTimeNs(response model.RunResponse) *uint64 {
	if response.RawCPUTimeNs != nil {
		return response.RawCPUTimeNs
	}
	if response.CPUTimeNs > 0 {
		value := response.CPUTimeNs
		return &value
	}
	return nil
}

// computeJudgment derives the work verdict from an eligible finalized envelope.
// A scoped judge also checks whether the measured peak permits a decision.
func computeJudgment(req *model.RunRequest, response model.RunResponse, cfg durante.Config, tuning ...config.RuntimeTuningConfig) *model.WorkMeasurementJudgment {
	if !req.WorkMeasurementJudge {
		return nil
	}
	family, ok := durante.FamilyForLanguage(req.Lang)
	if !ok || !cfg.Judge.JudgesFamily(family) {
		return nil
	}
	exec := response.WorkMeasurementExecution
	if exec == nil {
		// No envelope execution was produced (pre-execution admission failure).
		return nil
	}
	result := cfg.Judge.Decide(durante.JudgeInputs{
		State:               exec.State,
		OutputMatches:       exec.OutputMatches,
		Estimate:            exec.TimeEstimate,
		NominalTimeExceeded: exec.NominalTimeExceeded,
		NominalTimeMs:       int64(req.Limits.TimeMs),
		RawCPUTimeNs:        judgeRawCPUTimeNs(response),
	})
	// Both the worker and the verifier bind the clock receipt to their own
	// model before attributing a budget stop or an unavailable terminal clock.
	if exec.State == "budget_exceeded" || exec.TerminationSource == terminalClockUnavailable {
		plan := effectiveWorkClock(req, cfg, optionalRuntimeTuning(tuning))
		report := response.WorkMeasurement
		if plan != nil && plan.TimePolicy != nil && report != nil && report.DispatchProfile == plan.TimePolicy.DispatchProfile &&
			(report.DispatchProfile != durante.DispatchGLIBC241 || report.HostLibc == "2.41") &&
			withinRawMeasurementLimits(response, exec.RawLimits) && validateRemoteWorkClock(req, response, plan) == nil {
			if exec.TerminationSource == terminalClockUnavailable {
				result.Verdict, result.Reason = durante.JudgeUnavailable, "Durante work clock unavailable at target exit"
			} else {
				result.Verdict = durante.JudgeTimeLimitExceeded
				result.Reason = "Durante work clock exceeded the nominal time limit"
				if estimate := exec.TimeEstimate; estimate != nil {
					result.EstimateNs = &estimate.Nanoseconds
					result.EstimateMs = &estimate.Milliseconds
				}
			}
		}
	}
	return &model.WorkMeasurementJudgment{
		Authority:         durante.Authority,
		Verdict:           judgeVerdictStatus(result.Verdict),
		Reason:            result.Reason,
		State:             exec.State,
		NominalTimeMs:     int64(req.Limits.TimeMs),
		EstimatedTimeMs:   result.EstimateMs,
		EstimatedTimeNs:   result.EstimateNs,
		IntegrityMode:     string(result.IntegrityMode),
		IntegrityFlagged:  result.IntegrityFlagged,
		TerminationSource: exec.TerminationSource,
	}
}

// A larger raw envelope measures the target and the instrumentor together.
// An observed overage cannot decide the contestant's native memory verdict.
func scopedMemoryJudgment(req *model.RunRequest, cfg durante.Config, response model.RunResponse, judgment *model.WorkMeasurementJudgment) *model.WorkMeasurementJudgment {
	// Every admitted scoped decision has a clock receipt. Shadow comparisons
	// run without that clock and keep their diagnostic interpretation.
	if cfg.JudgeCases == nil || response.WorkMeasurementExecution.Clock == nil {
		return judgment
	}
	switch judgment.Verdict {
	case model.RunStatusAccepted, model.RunStatusWA, model.RunStatusTLE, model.RunStatusRE:
		var reason string
		switch {
		case response.MemoryKB <= 0:
			reason = "Durante peak RSS is unavailable for the nominal memory limit"
		case response.MemoryKB > int64(req.Limits.MemoryMB)*1024:
			reason = "Durante peak RSS exceeds the nominal memory limit and native memory usage is unknown"
		default:
			return judgment
		}
		withheld := *judgment
		withheld.Verdict = model.RunStatusInitFail
		withheld.Reason = reason
		return &withheld
	default:
		return judgment
	}
}

// finishWorkMeasurementJudge converts the finalized diagnostic envelope and time
// estimate into a verdict, replacing the diagnostic InitFail response. It runs
// after finishWorkMeasurementDiagnostic and finishWorkMeasurementTime, so the
// execution state, output match and time estimate are already decided. Requests
// that did not ask for judge mode, or that the operator policy does not judge,
// are returned unchanged (still the diagnostic InitFail response).
func finishWorkMeasurementJudge(req *model.RunRequest, response model.RunResponse, cfg durante.Config, tuning ...config.RuntimeTuningConfig) model.RunResponse {
	judgment := computeJudgment(req, response, cfg, tuning...)
	if judgment == nil {
		return response
	}
	return applyWorkMeasurementJudgment(response, scopedMemoryJudgment(req, cfg, response, judgment))
}

func applyWorkMeasurementJudgment(response model.RunResponse, judgment *model.WorkMeasurementJudgment) model.RunResponse {
	response.WorkMeasurementJudgment = judgment
	response.Status = judgment.Verdict
	response.VerdictSource = "durante_judge"
	response.Reason = judgment.Reason
	if judgment.Verdict == model.RunStatusAccepted {
		score := 1.0
		response.Score = &score
	} else {
		response.Score = nil
	}
	return response
}

// validateRemoteWorkMeasurementJudge independently recomputes the verdict from
// the returned raw observation and the control plane's own judge policy, and
// rejects a result whose judgment, status, score or envelope does not match, or
// that an older runner returned without a judgment block. The time estimate the
// recomputation relies on is itself re-verified by validateRemoteWorkMeasurementTime.
func validateRemoteWorkMeasurementJudge(req *model.RunRequest, response model.RunResponse, cfg durante.Config, tuning ...config.RuntimeTuningConfig) error {
	j := response.WorkMeasurementJudgment
	if !req.WorkMeasurementJudge {
		if j != nil {
			return fmt.Errorf("invalid remote result: unsolicited work measurement judgment")
		}
		return nil
	}
	expected := computeJudgment(req, response, cfg, tuning...)
	if expected == nil {
		// Judge was requested but this control plane cannot judge it (no envelope
		// execution, or family/policy mismatch). A judgment would be unverifiable.
		if j != nil || response.WorkMeasurementExecution != nil || response.WorkMeasurement != nil ||
			response.Status != model.RunStatusInitFail || response.Score != nil || response.VerdictSource == "durante_judge" {
			return fmt.Errorf("invalid remote result: unverifiable work measurement judgment")
		}
		return nil
	}
	if j == nil {
		return fmt.Errorf("invalid remote result: missing work measurement judgment")
	}
	// Older workers converted a pre-session initialization failure into a
	// failed RE envelope. Only propagate a contestant RE with evidence that
	// the target ran or the worker identified a termination source.
	if expected.Verdict == model.RunStatusRE && expected.State == "failed" && expected.TerminationSource == "" &&
		response.WorkMeasurement == nil && response.ExitCode == nil && response.CPUAccounting == nil &&
		response.CPUTimeNs == 0 && response.CPUTimeMs == 0 && response.ProcessCPUTimeMs == 0 {
		return fmt.Errorf("remote judge returned a runtime error without evidence of contestant execution")
	}
	// A sealed worker can return the original work verdict. Validate that
	// verdict in full before applying the controller's memory policy. A worker
	// that already withheld the verdict must supply the exact local reason.
	if j.Verdict == model.RunStatusInitFail {
		guarded := scopedMemoryJudgment(req, cfg, response, expected)
		if guarded != expected && response.Reason != guarded.Reason {
			return fmt.Errorf("invalid remote result: scoped memory verdict reason mismatch")
		}
		expected = guarded
	}
	if !judgmentsEqual(j, expected) {
		return fmt.Errorf("invalid remote result: work measurement judgment mismatch")
	}
	if response.Status != expected.Verdict || response.VerdictSource != "durante_judge" {
		return fmt.Errorf("invalid remote result: work measurement judge verdict mismatch")
	}
	if expected.Verdict == model.RunStatusAccepted {
		if response.Score == nil || *response.Score != 1.0 {
			return fmt.Errorf("invalid remote result: accepted judge verdict without a score")
		}
	} else if response.Score != nil {
		return fmt.Errorf("invalid remote result: non-accepted judge verdict carries a score")
	}
	return nil
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// judgmentsEqual compares two judgments by value, dereferencing pointer fields,
// so a faithful re-serialization is not rejected over pointer identity.
func judgmentsEqual(a, b *model.WorkMeasurementJudgment) bool {
	return a.Authority == b.Authority && a.Verdict == b.Verdict && a.Reason == b.Reason &&
		a.State == b.State && a.NominalTimeMs == b.NominalTimeMs &&
		int64PtrEqual(a.EstimatedTimeMs, b.EstimatedTimeMs) &&
		int64PtrEqual(a.EstimatedTimeNs, b.EstimatedTimeNs) &&
		a.IntegrityMode == b.IntegrityMode && a.IntegrityFlagged == b.IntegrityFlagged &&
		a.TerminationSource == b.TerminationSource
}

// validateJudgeRequest gates a judge-mode request against the operator policy.
// Request-shape rules (time, limits, single program) are enforced earlier by
// ValidateWorkMeasurementLimits; this adds the operator and family gate.
func validateJudgeRequest(ctx context.Context, req *model.RunRequest, cfg durante.Config) error {
	if !req.WorkMeasurementJudge {
		return nil
	}
	if !cfg.Judge.Enabled {
		return fmt.Errorf("work_measurement_judge is not enabled on this deployment")
	}
	// Mode "shadow" configures the machinery for comparisons only; a client
	// cannot obtain a deciding verdict until the operator selects "on".
	if !cfg.Judge.VerdictsAllowed && !isShadowJudge(ctx) {
		return fmt.Errorf("work_measurement_judge verdicts are not enabled on this deployment (shadow mode only)")
	}
	if !req.WorkMeasurementTime || req.WorkMeasurementLimits == nil {
		return fmt.Errorf("work_measurement_judge requires work_measurement_time and work_measurement_limits")
	}
	family, ok := durante.FamilyForLanguage(req.Lang)
	if !ok {
		return fmt.Errorf("work_measurement_judge requires a registered source-language profile")
	}
	if !cfg.Judge.JudgesFamily(family) {
		return fmt.Errorf("work_measurement_judge is not enabled for family %q", family)
	}
	if err := cfg.Judge.Validate(cfg); err != nil {
		return fmt.Errorf("work_measurement_judge: %w", err)
	}
	return nil
}
