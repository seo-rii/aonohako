package execute

import (
	"fmt"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// ApplyJudgeWorkLimits applies the deployment policy before HTTP queue admission.
func ApplyJudgeWorkLimits(req *model.RunRequest, execution config.ExecutionConfig) error {
	if !req.WorkMeasurementJudge {
		return nil
	}
	tuning, err := resolveRuntimeTuning(req, execution.RuntimeTuning, execution.RuntimeTuningProfiles)
	if err != nil {
		return err
	}
	admitted, err := resolveJudgeWorkLimits(req, execution.WorkMeasurement, tuning)
	if err == nil {
		req.WorkMeasurementLimits = admitted.WorkMeasurementLimits
	}
	return err
}

// Use the actual raw memory size when choosing the runtime and its model.
// The offline descriptor uses the same candidate before the model is registered.
func candidateJudgeWorkLimits(req *model.RunRequest, cfg durante.Config) (*model.RunRequest, error) {
	if req.WorkMeasurementLimits != nil {
		return req, nil
	}
	if cfg.ResourcePolicy == nil {
		return nil, fmt.Errorf("work_measurement_judge without work_measurement_limits requires an operator resource policy")
	}
	candidate := *req
	limits := cfg.ResourcePolicy.MaxLimits
	candidate.WorkMeasurementLimits = &limits
	return &candidate, nil
}

func resolveJudgeWorkLimits(req *model.RunRequest, cfg durante.Config, tuning config.RuntimeTuningConfig) (*model.RunRequest, error) {
	if !req.WorkMeasurementJudge || !req.WorkMeasurementTime || !req.WorkMeasurement || req.WorkMeasurementShadow || req.WorkMeasurementClock != nil {
		return req, nil
	}
	candidate, err := candidateJudgeWorkLimits(req, cfg)
	if err != nil {
		return nil, err
	}
	if effectiveWorkClock(candidate, cfg, tuning) == nil {
		if req.WorkMeasurementLimits == nil {
			return nil, fmt.Errorf("work_measurement_limits is required unless a deciding online Durante clock is bound to the operator's raw envelope")
		}
		return req, nil
	}
	limits := candidate.WorkMeasurementLimits
	if limits.CPUTimeMs <= req.Limits.TimeMs || limits.WallTimeMs <= req.Limits.TimeMs {
		return nil, fmt.Errorf("online Durante clock requires raw cpu_time_ms and wall_time_ms greater than nominal limits.time_ms")
	}
	if err := cfg.ResourcePolicy.ValidateRequest(*limits); err != nil {
		return nil, err
	}
	return candidate, nil
}

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
