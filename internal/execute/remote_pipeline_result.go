package execute

import (
	"errors"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/timing"
)

// The control plane knows which private stages it delegated. Validate the
// native result independently of the optional Durante receipt.
func validateRemotePipelineResult(req *model.RunRequest, resp *model.RunResponse) error {
	if req.Pipeline == nil {
		return nil
	}
	plan := req.Pipeline
	if len(resp.Steps) > len(plan.Steps) {
		return errors.New("remote pipeline returned extra steps")
	}
	for i, step := range resp.Steps {
		expected := plan.Steps[i]
		interactive := strings.EqualFold(strings.TrimSpace(expected.Executor.Kind), "interactive")
		program := expected.Executor.ProgramID
		if interactive {
			program = expected.Executor.ParticipantProgramID
		}
		if step.ID != expected.ID || step.ProgramID != strings.TrimSpace(program) ||
			!publicPipelineStepSource(step, interactive) {
			return errors.New("remote pipeline step identity or verdict source is inconsistent")
		}
		if roles := step.CPUTimeByRole; roles != nil {
			if !interactive || roles.Contestant.ProgramID != step.ProgramID ||
				roles.Interactor.ProgramID != strings.TrimSpace(expected.Executor.InteractorProgramID) ||
				!pipelineRoleTimesMatch(step, roles) {
				return errors.New("remote interactive role timing is inconsistent")
			}
		}
		if i+1 < len(resp.Steps) && step.Status != model.RunStatusAccepted {
			return errors.New("remote pipeline continued after a failed step")
		}
		var maxHandoff int64
		for _, output := range expected.Outputs {
			maxHandoff += output.MaxBytes
		}
		if step.HandoffBytes < 0 || step.HandoffBytes > maxHandoff || (step.Status != model.RunStatusAccepted && step.HandoffBytes != 0) {
			return errors.New("remote pipeline handoff size is inconsistent")
		}
	}
	if !remotePipelineOutcomeMatches(plan, resp) {
		return errors.New("remote pipeline outcome does not match its execution prefix")
	}

	totals := aggregateStepResponse(model.RunResponse{}, resp.Steps)
	if resp.TimeMs != totals.TimeMs || resp.WallTimeMs != totals.WallTimeMs ||
		resp.CPUTimeMs != totals.CPUTimeMs || resp.CPUTimeNs != totals.CPUTimeNs ||
		!equalPipelineCounter(resp.RawCPUTimeNs, totals.RawCPUTimeNs) || !equalPipelineCounter(resp.RawCPUTimeMs, totals.RawCPUTimeMs) ||
		resp.ProcessCPUTimeMs != totals.ProcessCPUTimeMs || resp.MemoryKB != totals.MemoryKB {
		return errors.New("remote pipeline resource totals do not match its steps")
	}
	if norm := resp.CPUTimeNormalization; norm != nil && norm.Method != timing.CPUNormalizationMethod {
		return errors.New("remote pipeline returned an unknown normalization method")
	}
	// These are single-program or match diagnostics. They do not belong to
	// a pipeline and may carry free-form text about private stage inputs.
	resp.CPUAccounting, resp.CPUTimeSampling = nil, nil
	resp.Match, resp.StartedParticipants = nil, 0
	return nil
}

func pipelineRoleTimesMatch(step model.StepResult, roles *model.PipelineRoleTimes) bool {
	c, j := roles.Contestant, roles.Interactor
	if step.Status == model.RunStatusAccepted && (j.Status != model.RunStatusAccepted || j.ExitCode == nil || *j.ExitCode != 0) {
		return false
	}
	for _, role := range []model.PipelineRoleCPU{c, j} {
		switch role.Status {
		case model.RunStatusAccepted, model.RunStatusWA, model.RunStatusTLE, model.RunStatusMLE,
			model.RunStatusWLE, model.RunStatusRE, model.RunStatusInitFail:
		default:
			return false
		}
		if role.CPUTimeMs < 0 || role.CPUTimeMs > step.CPUTimeMs ||
			(role.Status == model.RunStatusAccepted && role.ExitCode != nil && *role.ExitCode != 0) {
			return false
		}
	}
	return equalPipelineCounter(step.ExitCode, c.ExitCode) && step.CPUTimeMs-c.CPUTimeMs == j.CPUTimeMs &&
		equalPipelineCounter(step.CPUTimeNs, sumStepNanoseconds(c.CPUTimeNs, j.CPUTimeNs)) &&
		equalPipelineCounter(step.RawCPUTimeNs, sumStepNanoseconds(c.RawCPUTimeNs, j.RawCPUTimeNs))
}

func equalPipelineCounter[T int | int64 | uint64](a, b *T) bool {
	return a == b || (a != nil && b != nil && *a == *b)
}

func remotePipelineOutcomeMatches(plan *model.PipelineV1, resp *model.RunResponse) bool {
	count := len(resp.Steps)
	if count > 0 {
		last := resp.Steps[count-1]
		if last.Status != model.RunStatusAccepted {
			// A failed interactive verdict can carry zero. Batch execution and
			// preparation failures have no score before the final judge runs.
			if resp.Score != nil && (*resp.Score != 0 || last.VerdictSource == "" ||
				!strings.EqualFold(strings.TrimSpace(plan.Steps[count-1].Executor.Kind), "interactive")) {
				return false
			}
			return resp.Status == last.Status && resp.VerdictSource == prefixStepVerdictSource(last.ID, last.VerdictSource)
		}
		if resp.Status == model.RunStatusRE && resp.VerdictSource == "step:"+last.ID+":artifact" {
			// Artifact capture/storage happens before HandoffBytes is published.
			return last.HandoffBytes == 0 && resp.Score == nil
		}
	} else if resp.Status == model.RunStatusInitFail {
		switch resp.VerdictSource {
		case "", "pipeline:resource", "pipeline:artifact":
			return resp.Score == nil
		}
	}

	if count < len(plan.Steps) {
		next := plan.Steps[count]
		prefix := "step:" + next.ID + ":"
		// A stage can fail during stdin or interactive artifact setup before
		// producing its first native result row.
		return resp.Status == model.RunStatusInitFail && resp.Score == nil &&
			(resp.VerdictSource == prefix+"stdin" ||
				(resp.VerdictSource == prefix+"artifact" && strings.EqualFold(strings.TrimSpace(next.Executor.Kind), "interactive")))
	}
	if count == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(plan.FinalJudge.Kind)) {
	case "diff":
		return resp.VerdictSource == "final:diff" && resp.Score == nil &&
			(resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusWA)
	case "spj":
		return resp.VerdictSource == "final:spj" &&
			(resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusWA || resp.Status == model.RunStatusRE || resp.Status == model.RunStatusInitFail)
	}
	return false
}

// Only return coordinator and sandbox source codes across this boundary.
// In particular, a downstream verdict source must not carry arbitrary text.
func publicPipelineStepSource(step model.StepResult, interactive bool) bool {
	source := step.VerdictSource
	if step.Status == model.RunStatusAccepted {
		if interactive {
			// The interactor can accept before the contestant exits. Preserve
			// its verdict even when the grace period ends the contestant.
			return source == "interactor"
		}
		return source == "" && (step.ExitCode == nil || *step.ExitCode == 0) && !step.StdoutTruncated
	}
	if interactive {
		switch source {
		case "", "contestant", "interactor", "interactor:interactor", "interactive", "interactive:wall_time":
			return true
		}
		role, suffix, ok := strings.Cut(source, ":")
		if !ok || (role != "contestant" && role != "interactor") || suffix == "" {
			return false
		}
		source = suffix
	}
	switch source {
	case "", "exit_code", "signal", "signal_unattributed", "wait_status", "stream_io",
		"stdout_limit", "file_output", "wall_time", "cpu_rlimit", "cpu_feature_policy", "cpu_accounting",
		"cpu_time", "cpu_time_cgroup", "cpu_time_final", "cpu_time_cgroup_final",
		"memory_rss", "memory_reported", "memory_cgroup", "memory_cgroup_final", "runtime_memory", "address_space",
		"pids_cgroup", "pids_cgroup_final", "sandbox_init", "sandbox_helper_oom", "entry_limit",
		"workspace_bytes", "workspace_bytes_final", "workspace_entries", "workspace_entries_final",
		"workspace_depth", "workspace_depth_final", "workspace_scan", "workspace_scan_final":
		return true
	}
	return false
}
