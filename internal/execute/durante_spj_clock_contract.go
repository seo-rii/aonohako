package execute

import (
	"fmt"
	"math"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func spjPeerClocksRequested(req *model.RunRequest) bool {
	return req != nil && req.SPJ != nil && req.WorkMeasurementPeerTime != nil && req.WorkMeasurementPeerTime.Version == 2
}

func peerRuntimeOptions(role workMeasurementRole, req *model.RunRequest, cfg durante.Config, tuning config.RuntimeTuningConfig) (durante.RuntimeOptions, error) {
	options, err := effectiveRuntimeOptions(req, cfg, tuning)
	if err == nil && role.role == "checker" {
		// runSPJ appends these three file operands after the program. Stable
		// tokens bind the protocol's argv without exposing private filenames.
		options.Argv = append(options.Argv, runtimeOptionsWorkspace+"/.tmp/spj-input", runtimeOptionsWorkspace+"/.tmp/spj-output", runtimeOptionsWorkspace+"/.tmp/spj-solution")
		err = options.Validate()
	}
	return options, err
}

func peerTimePolicy(role workMeasurementRole, req *model.RunRequest, cfg durante.Config, tuning config.RuntimeTuningConfig) (durante.TimePolicy, string, error) {
	if role.role != "checker" || cfg.TimeModels == nil || cfg.TimeModels.Schema != 2 {
		return timePolicyForRequest(req, cfg, tuning)
	}
	options, err := peerRuntimeOptions(role, req, cfg, tuning)
	if err != nil {
		return durante.TimePolicy{}, "", err
	}
	return cfg.TimePolicyForOptions(req.Lang, options)
}

func validateSPJPeerClockVerdict(req *model.RunRequest, r model.RunResponse) error {
	invalid := fmt.Errorf("invalid SPJ result: verdict disagrees with contestant or checker clock")
	if len(r.WorkMeasurements) != 2 || r.WorkMeasurements[0].Role != "contestant" || r.WorkMeasurements[1].Role != "checker" ||
		len(r.Steps) != 0 || r.Match != nil || r.WorkMeasurement != nil || r.StartedParticipants != 0 || r.WorkMeasurementJudgment != nil || r.WorkMeasurementObservation != nil {
		return invalid
	}
	c, k := r.WorkMeasurements[0], r.WorkMeasurements[1]
	if c.CheckerScore != nil || !equalPipelineCounter(c.ExitCode, r.ExitCode) || c.TimeBudget == nil || k.TimeBudget == nil {
		return invalid
	}
	if u := c.TimeBudget.RawUsage; u != nil {
		if u.CPUTimeNs == nil || u.WallTimeMs == nil || u.MemoryKB == nil ||
			r.CPUTimeNs != *u.CPUTimeNs || r.CPUTimeMs != ceilCPUMilliseconds(*u.CPUTimeNs) ||
			r.WallTimeMs != *u.WallTimeMs || r.TimeMs != *u.WallTimeMs || r.MemoryKB != *u.MemoryKB ||
			(r.RawCPUTimeNs != nil && *r.RawCPUTimeNs != r.CPUTimeNs) || (r.RawCPUTimeMs != nil && *r.RawCPUTimeMs != r.CPUTimeMs) {
			return invalid
		}
	}
	matches := func(status, source string) error {
		if r.Status != status || r.VerdictSource != source {
			return invalid
		}
		if source != "spj" && r.Score != nil {
			return invalid
		}
		return nil
	}
	skipped := k.ExecutionStatus == model.RunStatusInitFail && k.TerminationSource == "spj_checker_not_started" && k.Report == nil &&
		k.ExitCode == nil && k.TimeBudget.RawUsage == nil && k.TimeBudget.Unavailable == "role_not_started" && k.TimeBudget.RawSource == k.TerminationSource
	if k.CheckerScore != nil && (!req.SPJ.EmitScore || k.ExecutionStatus != "OK" || k.ExitCode == nil || *k.ExitCode != 0 || math.IsNaN(*k.CheckerScore) || math.IsInf(*k.CheckerScore, 0) || *k.CheckerScore < 0 || *k.CheckerScore > 1) {
		return invalid
	}
	if c.ExecutionStatus != "OK" || c.ExitCode == nil || *c.ExitCode != 0 {
		if !skipped {
			return invalid
		}
		switch {
		case c.ExecutionStatus == model.RunStatusTLE && (c.TerminationSource == "durante_work_clock" || c.TerminationSource == "durante_work_clock_final"):
			return matches(model.RunStatusTLE, "contestant:"+c.TerminationSource)
		case c.ExecutionStatus == "OK" && c.ExitCode != nil && *c.ExitCode != 0:
			return matches(model.RunStatusRE, "contestant:exit_code")
		case c.ExecutionStatus == model.RunStatusInitFail && c.TerminationSource == "durante_peer_clock_error":
			return matches(model.RunStatusInitFail, "contestant:"+c.TerminationSource)
		default:
			return invalid
		}
	}
	outputExceeded := len(req.FileOutputs) == 0 && c.TimeBudget.RawUsage != nil && c.TimeBudget.RawUsage.StdoutLimitExceeded
	if outputExceeded {
		if !skipped {
			return invalid
		}
		return matches(model.RunStatusRE, "contestant:stdout_limit")
	}
	if skipped {
		if len(req.FileOutputs) > 0 && r.VerdictSource == "contestant:file_output" {
			return matches(model.RunStatusRE, "contestant:file_output")
		}
		return matches(model.RunStatusInitFail, "checker:spj")
	}
	if k.ExecutionStatus != "OK" || k.ExitCode == nil {
		switch {
		case k.ExecutionStatus == model.RunStatusTLE && (k.TerminationSource == "durante_work_clock" || k.TerminationSource == "durante_work_clock_final"):
			return matches(model.RunStatusInitFail, "checker:"+k.TerminationSource)
		case k.ExecutionStatus == model.RunStatusInitFail && k.TerminationSource == "durante_peer_clock_error":
			return matches(model.RunStatusInitFail, "checker:"+k.TerminationSource)
		default:
			return invalid
		}
	}
	if *k.ExitCode != 0 {
		if req.SPJ.EmitScore && (r.Score == nil || *r.Score != 0) || !req.SPJ.EmitScore && r.Score != nil {
			return invalid
		}
		return matches(model.RunStatusWA, "spj")
	}
	if req.SPJ.EmitScore && k.CheckerScore == nil {
		return matches(model.RunStatusInitFail, "checker:spj")
	}
	if (r.Score == nil) != (k.CheckerScore == nil) || r.Score != nil && *r.Score != *k.CheckerScore {
		return invalid
	}
	return matches(model.RunStatusAccepted, "spj")
}
