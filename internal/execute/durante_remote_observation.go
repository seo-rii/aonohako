package execute

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

// A missing or newer diagnostic payload can only affect the optional replay.
// The remote native result must already have passed its ordinary validation.
func remoteObservation(req *model.RunRequest, primary model.RunResponse, cfg durante.Config, raw json.RawMessage) *model.WorkMeasurementObservation {
	if !automaticObservationRequested(req, cfg) {
		return nil
	}
	if req.Pipeline != nil {
		return remotePipelineObservation(req, primary, cfg, raw)
	}
	if runvalidation.UsesSteps(req) {
		return remoteLegacyObservation(req, primary, cfg, raw)
	}
	if req.Interactor != nil {
		return remoteStandaloneInteractiveObservation(req, primary, cfg, raw)
	}
	unavailable := func(reason string) *model.WorkMeasurementObservation {
		return &model.WorkMeasurementObservation{Version: 1, Authority: durante.Authority, State: "unavailable", Reason: reason}
	}
	if reason := observationEligible(req, cfg); reason != "" {
		return unavailable(reason)
	}
	if len(raw) == 0 {
		return unavailable("remote runner returned no observation")
	}
	var block model.WorkMeasurementObservation
	if err := json.Unmarshal(raw, &block); err != nil || block.Version != 1 || block.Authority != durante.Authority || block.Steps != nil {
		return unavailable("incompatible remote observation")
	}
	if block.State == "unavailable" && block.Report == nil && block.StdinSHA256 == "" && block.OutputCheck == "" && strings.TrimSpace(block.Reason) != "" && len(block.Reason) <= 512 {
		return &block
	}
	check := observationOutputCheck(req, primary)
	if check == "" || block.OutputCheck != check || block.State != "complete" || block.Reason != "" || !observationReportMatches(req, cfg, block.Report) {
		return unavailable("remote observation is inconsistent with the request or primary verdict")
	}
	if strings.TrimSpace(req.StdinURL) != "" {
		digest, err := hex.DecodeString(block.StdinSHA256)
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != block.StdinSHA256 {
			return unavailable("remote observation has no valid shared input digest")
		}
	} else if block.StdinSHA256 != "" {
		return unavailable("unexpected remote observation input digest")
	}
	return &block
}

func remoteObservationBudget(req *model.RunRequest, cfg durante.Config) time.Duration {
	if !automaticObservationRequested(req, cfg) || cfg.ResourcePolicy == nil {
		return 0
	}
	wall := time.Duration(cfg.ResourcePolicy.MaxLimits.WallTimeMs) * time.Millisecond
	perReplay := max(stdinURLDownloadTimeout, wall) + wall
	if runvalidation.UsesSteps(req) {
		var budget time.Duration
		for i := range min(len(req.Steps), runvalidation.MaxSteps) {
			if single := legacyStepRequest(req, i); single != nil && observationEligible(single, cfg) == "" {
				budget += perReplay
			}
		}
		return budget
	}
	if req.Pipeline == nil {
		single := req
		if req.Interactor != nil {
			single = standaloneInteractiveContestant(req)
		}
		if single != nil && observationEligible(single, cfg) == "" {
			return perReplay
		}
		return 0
	}
	var budget time.Duration
	for _, step := range req.Pipeline.Steps[:min(len(req.Pipeline.Steps), runvalidation.MaxPipelineSteps)] {
		batch := pipelineObservationRequest(req, step)
		if batch != nil && observationEligible(batch, cfg) == "" {
			budget += perReplay
		}
	}
	return budget
}

func decodeRemoteStepObservation(raw json.RawMessage, state string, count int) *model.WorkMeasurementObservation {
	var block model.WorkMeasurementObservation
	if json.Unmarshal(raw, &block) != nil || block.Version != 2 || block.Authority != durante.Authority ||
		block.State != state || block.Reason != "" || block.OutputCheck != "" || block.StdinSHA256 != "" ||
		block.Report != nil || len(block.Steps) != count {
		return nil
	}
	return &block
}

func verifyRemoteStepObservation(row *model.PipelineStepObservation, batch *model.RunRequest, cfg durante.Config, eligible bool, outputCheck string) {
	if eligible && batch != nil && observationEligible(batch, cfg) == "" &&
		row.State == "complete" && row.Reason == "" && row.OutputCheck == outputCheck &&
		observationReportMatches(batch, cfg, row.Report) &&
		(row.Report.DispatchProfile != durante.DispatchDefault || row.Report.HostLibc == "") {
		return
	}
	// Discard unverified counters and any diagnostic text from the worker.
	*row = model.PipelineStepObservation{StepID: row.StepID, ProgramID: row.ProgramID,
		State: "unavailable", Reason: "no verified contestant observation"}
}

func remoteStandaloneInteractiveObservation(req *model.RunRequest, primary model.RunResponse, cfg durante.Config, raw json.RawMessage) *model.WorkMeasurementObservation {
	count := 0
	if len(primary.Steps) > 0 {
		count = 1
	}
	block := decodeRemoteStepObservation(raw, "interactive", count)
	if block == nil || (count == 1 && (block.Steps[0].StepID != "contestant" || block.Steps[0].ProgramID != "contestant")) {
		return &model.WorkMeasurementObservation{Version: 2, State: "unavailable", Authority: durante.Authority,
			Reason: "remote interactive observation is unavailable"}
	}
	if count == 0 {
		return block
	}
	single := standaloneInteractiveContestant(req)
	eligible := completedStandaloneInteractiveExchange(primary) && standaloneInteractiveAccountingMatches(req, primary)
	verifyRemoteStepObservation(&block.Steps[0], single, cfg, eligible, "interactive_transcript")
	return block
}

// A complete interactive receipt refers to a normally completed native run of
// each role within its own CPU allowance. The aggregate includes both roles.
func standaloneInteractiveAccountingMatches(req *model.RunRequest, primary model.RunResponse) bool {
	contestant, judge := primary.Steps[0], primary.Steps[1]
	judgeLimit := interactorRunRequest(req).Limits.TimeMs
	if (req.Limits.TimeMs > 0 && contestant.CPUTimeMs > int64(req.Limits.TimeMs)) ||
		(judgeLimit > 0 && judge.CPUTimeMs > int64(judgeLimit)) {
		return false
	}
	totals := aggregateStepResponse(model.RunResponse{}, primary.Steps)
	return primary.CPUTimeMs == totals.CPUTimeMs && primary.CPUTimeNs == totals.CPUTimeNs &&
		equalPipelineCounter(primary.RawCPUTimeNs, totals.RawCPUTimeNs) && equalPipelineCounter(primary.RawCPUTimeMs, totals.RawCPUTimeMs) &&
		primary.ProcessCPUTimeMs == totals.ProcessCPUTimeMs && primary.MemoryKB == totals.MemoryKB
}

func remotePipelineObservation(req *model.RunRequest, primary model.RunResponse, cfg durante.Config, raw json.RawMessage) *model.WorkMeasurementObservation {
	unavailable := &model.WorkMeasurementObservation{Version: 2, State: "unavailable", Authority: durante.Authority,
		Reason: "remote pipeline observation is unavailable"}
	block := decodeRemoteStepObservation(raw, "pipeline", len(primary.Steps))
	if block == nil || len(primary.Steps) > len(req.Pipeline.Steps) {
		return unavailable
	}
	finalJudgeRan := strings.HasPrefix(primary.VerdictSource, "final:")
	if finalJudgeRan && len(primary.Steps) != len(req.Pipeline.Steps) {
		return unavailable
	}
	for i, native := range primary.Steps {
		step := req.Pipeline.Steps[i]
		artifactFailure := primary.VerdictSource == "step:"+step.ID+":artifact"
		if primary.VerdictSource == "step:"+step.ID+":stdin" ||
			((i+1 < len(primary.Steps) || finalJudgeRan) && (native.Status != model.RunStatusAccepted || artifactFailure)) {
			return unavailable
		}
		program := step.Executor.ProgramID
		if strings.EqualFold(strings.TrimSpace(step.Executor.Kind), "interactive") {
			program = step.Executor.ParticipantProgramID
		}
		row := &block.Steps[i]
		if row.StepID != step.ID || row.ProgramID != strings.TrimSpace(program) ||
			row.StepID != native.ID || row.ProgramID != native.ProgramID {
			return unavailable
		}
		batch := pipelineObservationRequest(req, step)
		eligible := native.Status == model.RunStatusAccepted && native.ExitCode != nil && *native.ExitCode == 0 && !native.StdoutTruncated && !artifactFailure
		check := "native_output"
		if strings.EqualFold(strings.TrimSpace(step.Executor.Kind), "interactive") {
			check = "interactive_transcript"
			roles := native.CPUTimeByRole
			eligible = !artifactFailure && !native.StdoutTruncated && completedInteractiveExchange(native) && roles.Contestant.ProgramID == native.ProgramID &&
				roles.Interactor.ProgramID == strings.TrimSpace(step.Executor.InteractorProgramID) &&
				interactiveRoleCPUWithinLimits(roles.Contestant.CPUTimeMs, roles.Interactor.CPUTimeMs,
					&model.RunRequest{Limits: step.Limits, Interactor: &model.InteractorSpec{Limits: step.Executor.InteractorLimits}})
		}
		verifyRemoteStepObservation(row, batch, cfg, eligible, check)
	}
	return block
}

func interactiveRoleCPUWithinLimits(contestantMs, interactorMs int64, req *model.RunRequest) bool {
	judgeLimit := interactorRunRequest(req).Limits.TimeMs
	return contestantMs >= 0 && interactorMs >= 0 &&
		(req.Limits.TimeMs <= 0 || contestantMs <= int64(req.Limits.TimeMs)) &&
		(judgeLimit <= 0 || interactorMs <= int64(judgeLimit))
}

func remoteLegacyObservation(req *model.RunRequest, primary model.RunResponse, cfg durante.Config, raw json.RawMessage) *model.WorkMeasurementObservation {
	unavailable := &model.WorkMeasurementObservation{Version: 2, State: "unavailable", Authority: durante.Authority,
		Reason: "remote step observations are unavailable"}
	rows := primary.Steps
	block := decodeRemoteStepObservation(raw, "steps", len(rows))
	if block == nil || len(rows) > len(req.Steps) {
		return unavailable
	}
	if primary.Status == model.RunStatusAccepted || primary.Status == model.RunStatusWA {
		if len(rows) == 0 || len(rows) != len(req.Steps) {
			return unavailable
		}
	}
	if len(rows) == 1 && len(req.Steps) == 2 {
		first, next := rows[0], req.Steps[1]
		if first.Status == model.RunStatusAccepted {
			handoffFailure := (primary.Status == model.RunStatusRE || primary.Status == model.RunStatusInitFail) &&
				primary.VerdictSource == "step:"+req.Steps[0].ID+":handoff"
			stdinSource := "handoff"
			if len(next.StdinParts) != 0 {
				stdinSource = "stdin_parts"
			}
			nextInputFailure := primary.Status == model.RunStatusInitFail && primary.VerdictSource == "step:"+next.ID+":"+stdinSource
			if !handoffFailure && !nextInputFailure {
				return unavailable
			}
		} else if primary.Status != first.Status || primary.VerdictSource != prefixStepVerdictSource(first.ID, first.VerdictSource) {
			return unavailable
		}
	}
	for i, native := range rows {
		step := req.Steps[i]
		row := &block.Steps[i]
		if row.StepID != step.ID || row.ProgramID != step.ProgramID || native.ID != step.ID || native.ProgramID != step.ProgramID {
			return unavailable
		}
		handoffFailed := primary.VerdictSource == "step:"+step.ID+":handoff"
		single := legacyStepRequest(req, i)
		final := i == len(req.Steps)-1
		if !final && !handoffFailed && (i+1 < len(rows) || native.Status == model.RunStatusAccepted) {
			if single == nil || !legacyNativeStepOutput(single, native, false) || step.Handoff == nil {
				return unavailable
			}
			maxBytes := step.Handoff.MaxBytes
			if maxBytes == 0 {
				maxBytes = int64(outputLimitBytes(single))
			}
			if native.HandoffBytes < 0 || native.HandoffBytes > maxBytes {
				return unavailable
			}
		}
		if handoffFailed && i+1 < len(rows) {
			return unavailable
		}
		if final && (primary.Status != native.Status || primary.VerdictSource != prefixStepVerdictSource(step.ID, native.VerdictSource)) {
			return unavailable
		}
		eligible := single != nil && !handoffFailed && legacyNativeStepOutput(single, native, final)
		verifyRemoteStepObservation(row, single, cfg, eligible, "native_output")
	}
	return block
}
