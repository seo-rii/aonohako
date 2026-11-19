package execute

import (
	"fmt"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// Resolve a weighted clock from the same effective runtime options and raw
// memory envelope used by terminal time conversion. The wire budget is in ns.
func effectiveWorkClock(req *model.RunRequest, cfg durante.Config, tuning config.RuntimeTuningConfig) *durante.WorkClockRequest {
	if req.WorkMeasurementClock != nil {
		return req.WorkMeasurementClock
	}
	if !req.WorkMeasurementJudge || !cfg.Judge.VerdictsAllowed || cfg.Judge.Integrity != durante.JudgeIntegrityOff ||
		cfg.TimeModels == nil || cfg.TimeModels.Schema != 2 || req.Limits.TimeMs <= 0 {
		return nil
	}
	policy, _, err := ordinaryTimePolicy(req, cfg, tuning)
	if err != nil || policy.Formula() == durante.TimeMethodLatencyBoundREP || !cfg.Judge.JudgesFamily(policy.Family) {
		return nil
	}
	return &durante.WorkClockRequest{Version: 1, Budget: uint64(req.Limits.TimeMs) * 1_000_000, TimePolicy: &policy}
}

func validateJudgeClockBinding(req *model.RunRequest, cfg durante.Config, tuning config.RuntimeTuningConfig) error {
	if req.JudgeClockBinding == "" {
		return nil
	}
	clock := effectiveWorkClock(req, cfg, tuning)
	if clock == nil || clock.ModelBinding() != req.JudgeClockBinding {
		return fmt.Errorf("work_measurement_judge_clock_binding does not match the effective judge model")
	}
	return nil
}
