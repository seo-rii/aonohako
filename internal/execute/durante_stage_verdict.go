package execute

import (
	"errors"
	"math"
	"strings"

	"aonohako/internal/model"
)

var errStageClockVerdict = errors.New("inconsistent staged work clock result")

func stageNotStarted(r model.WorkMeasurementProcess) bool {
	return r.ExecutionStatus == model.RunStatusInitFail && r.TerminationSource == "stage_not_started" && r.Report == nil && r.ExitCode == nil && r.CheckerScore == nil &&
		r.TimeBudget != nil && r.TimeBudget.Unavailable == "role_not_started" && r.TimeBudget.RawSource == "stage_not_started" && r.TimeBudget.RawUsage == nil
}

func stageCheckerMatches(k model.WorkMeasurementProcess, emit bool, status, source, prefix, normal string, score *float64) bool {
	if stageNotStarted(k) {
		return status == model.RunStatusInitFail && source == prefix+"spj" && score == nil
	}
	if k.ExecutionStatus != "OK" {
		return k.CheckerScore == nil && status == model.RunStatusInitFail && score == nil && source == prefix+k.TerminationSource &&
			((k.ExecutionStatus == model.RunStatusInitFail && k.TerminationSource == "durante_peer_clock_error") ||
				(k.ExecutionStatus == model.RunStatusTLE && (k.TerminationSource == "durante_work_clock_final" || k.TerminationSource == "durante_work_clock")))
	}
	if k.ExitCode == nil {
		return false
	}
	if *k.ExitCode != 0 {
		return status == model.RunStatusWA && source == normal && k.CheckerScore == nil && ((!emit && score == nil) || (emit && score != nil && *score == 0))
	}
	if emit && k.CheckerScore == nil {
		return status == model.RunStatusInitFail && source == prefix+"spj" && score == nil
	}
	if k.CheckerScore != nil && (!emit || math.IsNaN(*k.CheckerScore) || *k.CheckerScore < 0 || *k.CheckerScore > 1) {
		return false
	}
	return status == model.RunStatusAccepted && source == normal && (score == nil) == (k.CheckerScore == nil) && (score == nil || *score == *k.CheckerScore)
}

func stageCountersMatch(row model.WorkMeasurementProcess, step model.StepResult) bool {
	if !equalPipelineCounter(row.ExitCode, step.ExitCode) || step.TimeMs != step.WallTimeMs || step.CPUTimeMs < 0 || step.ProcessCPUTimeMs < 0 || step.WallTimeMs < 0 || step.MemoryKB < 0 {
		return false
	}
	if step.RawCPUTimeMs != nil && *step.RawCPUTimeMs != step.CPUTimeMs || step.RawCPUTimeNs != nil && !equalPipelineCounter(step.RawCPUTimeNs, step.CPUTimeNs) {
		return false
	}
	u := row.TimeBudget.RawUsage
	if u == nil {
		return step.CPUTimeNs == nil && row.ExecutionStatus == model.RunStatusInitFail && (!stageNotStarted(row) || (step.CPUTimeMs == 0 && step.ProcessCPUTimeMs == 0 && step.MemoryKB == 0))
	}
	return equalPipelineCounter(u.CPUTimeNs, step.CPUTimeNs) && u.CPUTimeNs != nil && step.CPUTimeMs == ceilCPUMilliseconds(*u.CPUTimeNs) &&
		u.WallTimeMs != nil && *u.WallTimeMs == step.WallTimeMs && u.MemoryKB != nil && *u.MemoryKB == step.MemoryKB
}

func stageInteractiveMatches(rows []model.WorkMeasurementProcess, step model.StepResult) bool {
	if stageNotStarted(rows[0]) && stageNotStarted(rows[1]) {
		return step.Status == model.RunStatusInitFail && step.VerdictSource == "" && step.CPUTimeByRole == nil && step.ExitCode == nil && step.CPUTimeMs == 0 && step.CPUTimeNs == nil && step.MemoryKB == 0 && step.ProcessCPUTimeMs == 0
	}
	times := step.CPUTimeByRole
	if times == nil || !pipelineRoleTimesMatch(step, times) || step.TimeMs != step.WallTimeMs || step.WallTimeMs < 0 || step.ProcessCPUTimeMs < 0 || step.MemoryKB < 0 {
		return false
	}
	var expected [2]model.StepResult
	nativeRoles := [2]model.PipelineRoleCPU{times.Contestant, times.Interactor}
	var memory int64
	for i, row := range rows {
		role := nativeRoles[i]
		if stageNotStarted(row) || role.ProgramID != row.ProgramID || !equalPipelineCounter(role.ExitCode, row.ExitCode) || role.RawCPUTimeNs != nil && !equalPipelineCounter(role.RawCPUTimeNs, role.CPUTimeNs) {
			return false
		}
		raw := execResult{Status: row.ExecutionStatus, ExitCode: row.ExitCode, VerdictSource: row.TerminationSource}
		if u := row.TimeBudget.RawUsage; u != nil {
			if u.CPUTimeNs == nil || u.MemoryKB == nil || !equalPipelineCounter(role.CPUTimeNs, u.CPUTimeNs) || u.WallTimeMs == nil || *u.WallTimeMs > step.WallTimeMs {
				return false
			}
			raw.StdoutTruncated = u.StdoutLimitExceeded
			memory = max(memory, *u.MemoryKB)
		} else if role.CPUTimeNs != nil {
			return false
		}
		if i == 0 {
			expected[i] = interactiveContestantStepResult(&model.RunRequest{}, raw)
		} else {
			expected[i] = interactiveInteractorStepResult(&model.RunRequest{}, raw)
		}
		if expected[i].Status != role.Status {
			return false
		}
	}
	status, source := interactivePeerClockVerdict(rows, expected[:])
	return step.Status == status && step.VerdictSource == source && step.MemoryKB == memory
}

func stageBatchMatches(role workMeasurementRole, row model.WorkMeasurementProcess, step model.StepResult, legacyFinal bool, checker *model.WorkMeasurementProcess, score *float64) bool {
	if step.CPUTimeByRole != nil || !stageCountersMatch(row, step) {
		return false
	}
	if stageNotStarted(row) {
		return step.Status == model.RunStatusInitFail && step.VerdictSource == ""
	}
	if row.ExecutionStatus != "OK" {
		return row.TerminationSource != "durante_peer_cancelled" && row.ExecutionStatus == step.Status && row.TerminationSource == step.VerdictSource
	}
	if row.ExitCode == nil {
		return false
	}
	if *row.ExitCode != 0 {
		return step.Status == model.RunStatusRE && step.VerdictSource == "exit_code"
	}
	if row.TimeBudget.RawUsage == nil {
		return false
	}
	if row.TimeBudget.RawUsage.StdoutLimitExceeded {
		return step.Status == model.RunStatusRE && step.VerdictSource == "stdout_limit"
	}
	if len(role.stageRequest.FileOutputs) != 0 && step.Status == model.RunStatusRE && step.VerdictSource == "file_output" {
		return true
	}
	if !legacyFinal {
		return step.Status == model.RunStatusAccepted && step.VerdictSource == ""
	}
	if checker != nil {
		return stageCheckerMatches(*checker, role.stageRequest.SPJ.EmitScore, step.Status, step.VerdictSource, "checker:", "spj", score)
	}
	source := "stdout"
	if len(role.stageRequest.FileOutputs) != 0 {
		source = "file_output"
	}
	return (step.Status == model.RunStatusAccepted || step.Status == model.RunStatusWA) && step.VerdictSource == source && score == nil
}

// Validate the execution prefix, including roles that never started. The final
// checker has its own clock and contributes no native time to the stage total.
func validateStagedPeerClockVerdict(req *model.RunRequest, resp model.RunResponse) error {
	roles, err := stageWorkMeasurementRoles(req)
	if err != nil || len(roles) != len(resp.WorkMeasurements) || resp.WorkMeasurement != nil || resp.WorkMeasurementExecution != nil || resp.WorkMeasurementObservation != nil || resp.WorkMeasurementJudgment != nil || resp.Match != nil || resp.StartedParticipants != 0 {
		return errStageClockVerdict
	}
	rows := resp.WorkMeasurements
	var checker *model.WorkMeasurementProcess
	for i, role := range roles {
		row := &rows[i]
		if row.StepID != role.stepID || row.ProgramID != role.programID || row.Role != role.role || row.ParticipantIndex != nil || row.TimeBudget == nil || row.CheckerScore != nil && row.Role != "checker" {
			return errStageClockVerdict
		}
		if row.TimeBudget.Unavailable == "role_not_started" && !stageNotStarted(*row) {
			return errStageClockVerdict
		}
		if row.Role == "checker" {
			checker = row
		}
	}
	wantSteps := len(req.Steps)
	if req.Pipeline != nil {
		wantSteps = len(req.Pipeline.Steps)
	}
	if len(resp.Steps) > wantSteps {
		return errStageClockVerdict
	}
	totals := aggregateStepResponse(model.RunResponse{}, resp.Steps)
	if resp.TimeMs != totals.TimeMs || resp.WallTimeMs != totals.WallTimeMs || resp.CPUTimeNs != totals.CPUTimeNs || resp.CPUTimeMs != totals.CPUTimeMs || resp.MemoryKB != totals.MemoryKB || resp.ProcessCPUTimeMs != totals.ProcessCPUTimeMs || !equalPipelineCounter(resp.RawCPUTimeMs, totals.RawCPUTimeMs) || !equalPipelineCounter(resp.RawCPUTimeNs, totals.RawCPUTimeNs) {
		return errStageClockVerdict
	}
	index := 0
	for i := 0; i < wantSteps; i++ {
		role := roles[index]
		start := index
		index++
		if role.interactive {
			index++
		}
		group := rows[start:index]
		if i >= len(resp.Steps) {
			for _, row := range group {
				if !stageNotStarted(row) {
					return errStageClockVerdict
				}
			}
			continue
		}
		s := resp.Steps[i]
		if s.ID != role.stepID || s.ProgramID != role.programID || i+1 < len(resp.Steps) && s.Status != model.RunStatusAccepted {
			return errStageClockVerdict
		}
		if req.Pipeline == nil {
			if s.HandoffBytes < 0 || (s.Status != model.RunStatusAccepted && s.VerdictSource != "stdout_limit" || i+1 == wantSteps) && s.HandoffBytes != 0 {
				return errStageClockVerdict
			}
			if i+1 < len(resp.Steps) {
				limit := int64(outputLimitBytes(role.stageRequest))
				if h := req.Steps[i].Handoff; h != nil && h.MaxBytes > 0 {
					limit = h.MaxBytes
				}
				if s.HandoffBytes > limit {
					return errStageClockVerdict
				}
			}
		}
		if role.interactive {
			if !stageInteractiveMatches(group, s) {
				return errStageClockVerdict
			}
		} else if !stageBatchMatches(role, group[0], s, req.Pipeline == nil && i == wantSteps-1, checker, resp.Score) {
			return errStageClockVerdict
		}
	}
	if req.Pipeline != nil {
		copy := resp
		if validateRemotePipelineResult(req, &copy) != nil {
			return errStageClockVerdict
		}
	}
	n := len(resp.Steps)
	if n > 0 {
		last := resp.Steps[n-1]
		legacyVerdict := req.Pipeline == nil && n == wantSteps && checker != nil && (last.VerdictSource == "spj" || strings.HasPrefix(last.VerdictSource, "checker:"))
		if legacyVerdict {
			if resp.Status != last.Status || resp.VerdictSource != prefixStepVerdictSource(last.ID, last.VerdictSource) || !equalPipelineCounter(resp.ExitCode, last.ExitCode) {
				return errStageClockVerdict
			}
			return nil
		}
		if req.Pipeline != nil && checker != nil && n == wantSteps && last.Status == model.RunStatusAccepted &&
			(resp.VerdictSource == "final:spj" || strings.HasPrefix(resp.VerdictSource, "final:checker:")) {
			if !stageCheckerMatches(*checker, req.Pipeline.FinalJudge.SPJ.EmitScore, resp.Status, resp.VerdictSource, "final:checker:", "final:spj", resp.Score) || resp.ExitCode != nil {
				return errStageClockVerdict
			}
			return nil
		}
	}
	if checker != nil && !stageNotStarted(*checker) {
		return errStageClockVerdict
	}
	if n > 0 {
		last := resp.Steps[n-1]
		if last.Status != model.RunStatusAccepted || req.Pipeline == nil && n == wantSteps {
			if resp.Status != last.Status || resp.VerdictSource != prefixStepVerdictSource(last.ID, last.VerdictSource) || !equalPipelineCounter(resp.ExitCode, last.ExitCode) {
				return errStageClockVerdict
			}
			if resp.Score != nil {
				return errStageClockVerdict
			}
			return nil
		}
		if req.Pipeline == nil && resp.VerdictSource == prefixStepVerdictSource(last.ID, "handoff") && (resp.Status == model.RunStatusInitFail || resp.Status == model.RunStatusRE) && resp.Score == nil && resp.ExitCode == nil {
			return nil
		}
	}
	// The native pipeline prefix and final diff were checked above. Setup,
	// artifact and final judge results have no contestant exit of their own.
	if req.Pipeline != nil {
		if resp.ExitCode != nil {
			return errStageClockVerdict
		}
		return nil
	}
	if resp.Status != model.RunStatusInitFail || resp.Score != nil || n >= wantSteps || resp.ExitCode != nil {
		return errStageClockVerdict
	}
	if n == 0 && resp.VerdictSource == "" {
		return nil
	}
	for _, src := range []string{"stdin_url", "stdin_parts", "handoff"} {
		if resp.VerdictSource == prefixStepVerdictSource(req.Steps[n].ID, src) {
			return nil
		}
	}
	return errStageClockVerdict
}
