package execute

import (
	"fmt"

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
	return cfg
}

// Conversion is attached after the execution state has been finalized. It does
// not change the diagnostic report, raw resource values, or outer verdict.
func finishWorkMeasurementTime(req *model.RunRequest, response model.RunResponse, cfg durante.Config) model.RunResponse {
	if !req.WorkMeasurementTime || response.WorkMeasurementExecution == nil {
		return response
	}
	d := *response.WorkMeasurementExecution
	response.WorkMeasurementExecution = &d
	d.TimeEstimate, d.NominalTimeExceeded, d.TimeEstimateUnavailable = nil, nil, ""
	if d.State != "complete" {
		d.TimeEstimateUnavailable = "execution_" + d.State
		return response
	}
	if !validDiagnosticWorkReport(req.Lang, response.WorkMeasurement) {
		d.TimeEstimateUnavailable = "incompatible_observation"
		return response
	}
	policy, runtimeID, err := cfg.TimePolicyFor(req.Lang)
	if err != nil {
		d.TimeEstimateUnavailable = "incompatible_time_configuration"
		return response
	}
	estimate, err := policy.Estimate(runtimeID, *response.WorkMeasurement)
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

func validateRemoteWorkMeasurementTime(req *model.RunRequest, response model.RunResponse, cfg durante.Config) error {
	d := response.WorkMeasurementExecution
	if d == nil { // Admission/initialization failed before an envelope execution.
		return nil
	}
	if !req.WorkMeasurementTime {
		if d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "" {
			return fmt.Errorf("invalid remote result: unsolicited work measurement time")
		}
		return nil
	}
	if _, _, err := cfg.TimePolicyFor(req.Lang); err != nil {
		return fmt.Errorf("invalid remote result: incompatible work measurement time configuration")
	}
	// Recompute from the complete raw observation and the control plane's own
	// configured downstream image/model, never from returned weights or time.
	expected := finishWorkMeasurementTime(req, response, cfg).WorkMeasurementExecution
	if (d.TimeEstimate == nil) != (expected.TimeEstimate == nil) ||
		(d.NominalTimeExceeded == nil) != (expected.NominalTimeExceeded == nil) ||
		d.TimeEstimateUnavailable != expected.TimeEstimateUnavailable {
		return fmt.Errorf("invalid remote result: work measurement time availability mismatch")
	}
	if d.TimeEstimate != nil && (*d.TimeEstimate != *expected.TimeEstimate || *d.NominalTimeExceeded != *expected.NominalTimeExceeded) {
		return fmt.Errorf("invalid remote result: work measurement time estimate mismatch")
	}
	return nil
}
