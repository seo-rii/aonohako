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
	if req.Pipeline == nil {
		if observationEligible(req, cfg) == "" {
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

func remotePipelineObservation(req *model.RunRequest, primary model.RunResponse, cfg durante.Config, raw json.RawMessage) *model.WorkMeasurementObservation {
	unavailable := &model.WorkMeasurementObservation{Version: 2, State: "unavailable", Authority: durante.Authority,
		Reason: "remote pipeline observation is unavailable"}
	var block model.WorkMeasurementObservation
	if json.Unmarshal(raw, &block) != nil || block.Version != 2 || block.Authority != durante.Authority ||
		block.State != "pipeline" || block.Reason != "" || block.OutputCheck != "" || block.StdinSHA256 != "" ||
		block.Report != nil || len(block.Steps) != len(primary.Steps) || len(primary.Steps) > len(req.Pipeline.Steps) {
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
		if row.State == "complete" && row.Reason == "" && row.OutputCheck == "native_output" &&
			batch != nil && observationEligible(batch, cfg) == "" && native.Status == model.RunStatusAccepted &&
			native.ExitCode != nil && *native.ExitCode == 0 && !native.StdoutTruncated &&
			!artifactFailure && observationReportMatches(batch, cfg, row.Report) &&
			(row.Report.DispatchProfile != durante.DispatchDefault || row.Report.HostLibc == "") {
			continue
		}
		// A remote diagnostic reason may contain private resource or artifact bytes.
		*row = model.PipelineStepObservation{StepID: native.ID, ProgramID: native.ProgramID,
			State: "unavailable", Reason: "no verified batch observation"}
	}
	return &block
}
