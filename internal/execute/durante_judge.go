package execute

import (
	"context"
	"fmt"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// judgeVerdictStatus maps the durante verdict enum to a public run status.
func judgeVerdictStatus(v durante.JudgeVerdict) (string, bool) {
	switch v {
	case durante.JudgeAccepted:
		return model.RunStatusAccepted, true
	case durante.JudgeWrongAnswer:
		return model.RunStatusWA, false
	case durante.JudgeTimeLimitExceeded:
		return model.RunStatusTLE, false
	case durante.JudgeRuntimeError:
		return model.RunStatusRE, false
	default:
		return model.RunStatusRE, false
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

// computeJudgment derives the verdict from a finalized response. It returns the
// judgment, its run status, whether that status is Accepted, and whether judge
// mode applies at all (requested, judged family, envelope execution present).
// It reads only already-decided inputs and never mutates the response.
func computeJudgment(req *model.RunRequest, response model.RunResponse, cfg durante.Config) (*model.WorkMeasurementJudgment, string, bool, bool) {
	if !req.WorkMeasurementJudge {
		return nil, "", false, false
	}
	family, ok := durante.FamilyForLanguage(req.Lang)
	if !ok || !cfg.Judge.JudgesFamily(family) {
		return nil, "", false, false
	}
	exec := response.WorkMeasurementExecution
	if exec == nil {
		// No envelope execution was produced (pre-execution admission failure).
		return nil, "", false, false
	}
	result := cfg.Judge.Decide(durante.JudgeInputs{
		State:               exec.State,
		OutputMatches:       exec.OutputMatches,
		Estimate:            exec.TimeEstimate,
		NominalTimeExceeded: exec.NominalTimeExceeded,
		NominalTimeMs:       int64(req.Limits.TimeMs),
		RawCPUTimeNs:        judgeRawCPUTimeNs(response),
	})
	status, accepted := judgeVerdictStatus(result.Verdict)
	judgment := &model.WorkMeasurementJudgment{
		Authority:         durante.Authority,
		Verdict:           status,
		Reason:            result.Reason,
		State:             exec.State,
		NominalTimeMs:     int64(req.Limits.TimeMs),
		EstimatedTimeMs:   result.EstimateMs,
		EstimatedTimeNs:   result.EstimateNs,
		IntegrityMode:     string(result.IntegrityMode),
		IntegrityFlagged:  result.IntegrityFlagged,
		TerminationSource: exec.TerminationSource,
	}
	return judgment, status, accepted, true
}

// finishWorkMeasurementJudge converts the finalized diagnostic envelope and time
// estimate into a verdict, replacing the diagnostic InitFail response. It runs
// after finishWorkMeasurementDiagnostic and finishWorkMeasurementTime, so the
// execution state, output match and time estimate are already decided. Requests
// that did not ask for judge mode, or that the operator policy does not judge,
// are returned unchanged (still the diagnostic InitFail response).
func finishWorkMeasurementJudge(req *model.RunRequest, response model.RunResponse, cfg durante.Config) model.RunResponse {
	judgment, status, accepted, applicable := computeJudgment(req, response, cfg)
	if !applicable {
		return response
	}
	response.WorkMeasurementJudgment = judgment
	response.Status = status
	response.VerdictSource = "durante_judge"
	response.Reason = judgment.Reason
	if accepted {
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
func validateRemoteWorkMeasurementJudge(req *model.RunRequest, response model.RunResponse, cfg durante.Config) error {
	j := response.WorkMeasurementJudgment
	if !req.WorkMeasurementJudge {
		if j != nil {
			return fmt.Errorf("invalid remote result: unsolicited work measurement judgment")
		}
		return nil
	}
	expected, status, accepted, applicable := computeJudgment(req, response, cfg)
	if !applicable {
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
	if !judgmentsEqual(j, expected) {
		return fmt.Errorf("invalid remote result: work measurement judgment mismatch")
	}
	if response.Status != status || response.VerdictSource != "durante_judge" {
		return fmt.Errorf("invalid remote result: work measurement judge verdict mismatch")
	}
	if accepted {
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
