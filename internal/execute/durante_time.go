package execute

import (
	"fmt"
	"slices"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// Both execution and forwarding hold their own immutable operator settings.
func snapshotWorkMeasurementConfig(cfg durante.Config) durante.Config {
	cfg.Families = append([]string(nil), cfg.Families...)
	if cfg.ResourcePolicy != nil {
		policy := *cfg.ResourcePolicy
		cfg.ResourcePolicy = &policy
	}
	if cfg.TimePolicy != nil {
		policy := *cfg.TimePolicy
		cfg.TimePolicy = &policy
	}
	cfg.TimeModels = cfg.TimeModels.Clone()
	cfg.Judge.Families = append([]string(nil), cfg.Judge.Families...)
	return cfg
}

// Conversion is attached after the execution state has been finalized. It does
// not change the diagnostic report, raw resource values, or outer verdict.
func finishWorkMeasurementTime(req *model.RunRequest, response model.RunResponse, cfg durante.Config, runtimeTuning ...config.RuntimeTuningConfig) model.RunResponse {
	if !req.WorkMeasurementTime || response.WorkMeasurementExecution == nil {
		return response
	}
	d := *response.WorkMeasurementExecution
	response.WorkMeasurementExecution = &d
	d.TimeEstimate, d.NominalTimeExceeded, d.TimeEstimateUnavailable = nil, nil, ""
	d.TimeEstimateEligibility = nil
	unavailable := func(code string) {
		d.TimeEstimateEligibility = &durante.TimeEligibility{Version: 1, ReasonCodes: []string{code}}
	}
	terminalClock := d.State == "budget_exceeded" && d.TerminationSource == "durante_work_clock_final"
	if d.State != "complete" && !terminalClock {
		d.TimeEstimateUnavailable = "execution_" + d.State
		switch d.State {
		case "cancelled", "resource_exhausted", "failed", "incomplete":
			unavailable(d.TimeEstimateUnavailable)
		default:
			unavailable(durante.TimeExecutionNotComplete)
		}
		return response
	}
	if !validDiagnosticWorkReport(req.Lang, response.WorkMeasurement) {
		d.TimeEstimateUnavailable = "incompatible_observation"
		unavailable(durante.TimeIncompatibleObservation)
		return response
	}
	policy, runtimeID, err := ordinaryTimePolicy(req, cfg, optionalRuntimeTuning(runtimeTuning))
	if err != nil {
		d.TimeEstimateUnavailable = "incompatible_time_configuration"
		unavailable(durante.TimeIncompatibleConfiguration)
		return response
	}
	estimate, eligibility, err := policy.EstimateWithEligibility(runtimeID, *response.WorkMeasurement)
	d.TimeEstimateEligibility = &eligibility
	if err != nil {
		d.TimeEstimateUnavailable = "observation_not_convertible"
		return response
	}
	// ValidateWorkMeasurementLimits bounds nominal time by the raw envelope's
	// 600,000 ms ceiling before execution; compare in nanoseconds without rounding.
	exceeded := estimate.Nanoseconds > int64(req.Limits.TimeMs)*1_000_000
	d.TimeEstimate, d.NominalTimeExceeded = &estimate, &exceeded
	return response
}

func validateRemoteWorkMeasurementTime(req *model.RunRequest, response model.RunResponse, cfg durante.Config, runtimeTuning ...config.RuntimeTuningConfig) error {
	d := response.WorkMeasurementExecution
	if d == nil { // Admission/initialization failed before an envelope execution.
		return nil
	}
	if !req.WorkMeasurementTime {
		if d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "" || d.TimeEstimateEligibility != nil {
			return fmt.Errorf("invalid remote result: unsolicited work measurement time")
		}
		return nil
	}
	if _, _, err := ordinaryTimePolicy(req, cfg, optionalRuntimeTuning(runtimeTuning)); err != nil {
		return fmt.Errorf("invalid remote result: incompatible work measurement time configuration")
	}
	// Recompute from the complete raw observation and the control plane's own
	// configured downstream image/model, never from returned weights or time.
	expected := finishWorkMeasurementTime(req, response, cfg, runtimeTuning...).WorkMeasurementExecution
	if (d.TimeEstimate == nil) != (expected.TimeEstimate == nil) ||
		(d.NominalTimeExceeded == nil) != (expected.NominalTimeExceeded == nil) ||
		d.TimeEstimateUnavailable != expected.TimeEstimateUnavailable {
		return fmt.Errorf("invalid remote result: work measurement time availability mismatch")
	}
	if d.TimeEstimate != nil && (*d.TimeEstimate != *expected.TimeEstimate || *d.NominalTimeExceeded != *expected.NominalTimeExceeded) {
		return fmt.Errorf("invalid remote result: work measurement time estimate mismatch")
	}
	// Absence preserves older runner compatibility. Present eligibility never
	// overrides the recomputed estimate or the pre-existing unavailable string.
	if d.TimeEstimateEligibility != nil && (d.TimeEstimateEligibility.Validate() != nil || d.TimeEstimateEligibility.Eligible != expected.TimeEstimateEligibility.Eligible || !slices.Equal(d.TimeEstimateEligibility.ReasonCodes, expected.TimeEstimateEligibility.ReasonCodes)) {
		return fmt.Errorf("invalid remote result: work measurement time eligibility mismatch")
	}
	return nil
}
