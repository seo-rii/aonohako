package execute

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/timing"
)

const (
	defaultMaxOutputBytes        = 64 << 10
	hardMaxOutputBytes           = 8 << 20
	defaultWorkspaceBytes        = 128 << 20
	hardMaxWorkspaceBytes        = 1 << 30
	maxBinaryFiles               = 512
	maxRunPrograms               = 8
	maxRunSteps                  = 2
	maxSidecarOutputSpecs        = 64
	addressSpaceSlackKB          = 8 << 10
	sandboxThreadLimit           = 128
	maxBinaryFileBytes           = 16 << 20
	maxBinaryTotalBytes          = 48 << 20
	maxCapturedFileBytes         = 8 << 20
	maxCapturedSidecarTotalBytes = 16 << 20
	maxStepHandoffBytes          = hardMaxOutputBytes
	maxImageStreamBytes          = 8 << 20
	maxImageReadChunkBytes       = 256 << 10
	maxImageEventBytes           = 1 << 20
	maxImageEventsPerRead        = 8
	defaultSPJTimeMs             = 1000
	defaultSPJMemoryMB           = 256
	ocamlRunParam                = "s=32k"
)

type Hooks struct {
	OnImage func(mime, b64 string, ts int64)
	OnLog   func(stream, msg string)
}

type cappedBuffer struct {
	limit     int
	truncated bool
	buf       bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.truncated = true
			p = p[:remaining]
		}
		if _, err := b.buf.Write(p); err != nil {
			return 0, err
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return written, nil
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *cappedBuffer) Truncated() bool {
	return b.truncated
}

type Service struct {
	deploymentTarget      platform.DeploymentTarget
	runtimeTuning         config.RuntimeTuningConfig
	runtimeTuningProfiles map[string]config.RuntimeTuningConfig
	cgroupParentDir       string
}

type sandboxRunResult struct {
	response model.RunResponse
	judgeOut []byte
}

func New() *Service {
	opts, err := platform.CurrentRuntimeOptions()
	if err != nil {
		return &Service{runtimeTuning: config.DefaultRuntimeTuningConfig()}
	}
	return &Service{deploymentTarget: opts.DeploymentTarget, runtimeTuning: config.DefaultRuntimeTuningConfig()}
}

func (s *Service) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}
	tuning := s.runtimeTuning.WithSafeDefaults()
	if req.RuntimeProfile != "" {
		if err := runtimepolicy.ValidateProfileName(req.RuntimeProfile); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid runtime_profile: " + err.Error()}
		}
		profileTuning, ok := s.runtimeTuningProfiles[req.RuntimeProfile]
		if !ok {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "unknown runtime_profile: " + req.RuntimeProfile}
		}
		tuning = profileTuning.WithSafeDefaults()
	}
	if runRequestUsesSteps(req) {
		return s.runStepPipeline(ctx, req, hooks, tuning)
	}
	return s.runOne(ctx, req, hooks, tuning, true).response
}

func (s *Service) runOne(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig, evaluateOutput bool) sandboxRunResult {
	startWall := timing.MonotonicNow()
	if req.EnableNetwork && s.deploymentTarget == platform.DeploymentTargetCloudRun {
		return sandboxRunResult{response: model.RunResponse{
			Status: model.RunStatusInitFail,
			Reason: "embedded helper execution on cloudrun does not support enable_network=true; use a self-hosted remote runner for networked workloads",
		}}
	}
	if len(req.FileOutputs) > 1 {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "at most one file output is supported"}}
	}
	if len(req.SidecarOutputs) > maxSidecarOutputSpecs {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("too many sidecar outputs: max %d", maxSidecarOutputSpecs)}}
	}
	capturedOutputLimit := outputLimitBytes(req)
	if len(req.Binaries) > maxBinaryFiles {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("too many binaries: max %d", maxBinaryFiles)}}
	}
	if len(req.Binaries) == 0 {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "no binaries"}}
	}

	workDir, err := createRunWorkDir()
	if err != nil {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "mkdtemp failed: " + err.Error()}}
	}
	defer os.RemoveAll(workDir)

	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "workspace prep failed: " + err.Error()}}
	}

	primaryPath, runLang, err := materializeFiles(ws, req)
	if err != nil {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "materialize failed: " + err.Error()}}
	}

	cmdArgs := buildCommandWithRuntimeTuning(primaryPath, runLang, req, tuning)
	if len(cmdArgs) == 0 {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "empty command"}}
	}

	res := runCommandWithSandbox(ctx, ws, cmdArgs, req, hooks, capturedOutputLimit, tuning, s.cgroupParentDir)
	if res.Status == model.RunStatusInitFail {
		wallMs := timing.SinceMillis(startWall)
		return sandboxRunResult{response: model.RunResponse{Status: res.Status, TimeMs: wallMs, WallTimeMs: wallMs, CPUTimeMs: 0, Reason: res.Reason, VerdictSource: res.VerdictSource}}
	}

	rawOut := res.Stdout
	judgeOut := rawOut
	judgeSource := "stdout"
	fullErr := res.Stderr

	if len(req.FileOutputs) > 0 {
		captured, err := captureFileOutput(ws, req.FileOutputs[0])
		if err != nil {
			if res.Status == "OK" {
				res.Status = model.RunStatusRE
				res.Reason = "file output capture failed: " + err.Error()
				res.VerdictSource = "file_output"
			}
		} else {
			judgeOut = captured
			judgeSource = "file_output"
		}
	}

	sidecarOutputs, sidecarErrors := captureSidecarOutputs(ws, req.SidecarOutputs)
	status, evalReason, verdictSource := classifyRunStatusWithoutOutput(req, res)
	var score *float64
	if evaluateOutput {
		status, score, evalReason, verdictSource = evaluateRunStatus(ctx, ws, req, res, judgeOut, judgeSource, tuning, s.cgroupParentDir)
	}
	reason := res.Reason
	if evalReason != "" {
		reason = evalReason
	}

	var outResp, errResp string
	if status == model.RunStatusWA || status == model.RunStatusRE || (status == model.RunStatusTLE && req.IgnoreTLE) {
		outResp = clipUTF8(judgeOut, capturedOutputLimit)
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		errResp = clipUTF8(fullErr, capturedOutputLimit)
	}

	if hooks.OnLog != nil {
		if len(rawOut) > 0 {
			hooks.OnLog("stdout", clipUTF8(rawOut, capturedOutputLimit))
		}
		if len(fullErr) > 0 {
			hooks.OnLog("stderr", clipUTF8(fullErr, capturedOutputLimit))
		}
	}

	return sandboxRunResult{
		response: model.RunResponse{
			Status:          status,
			TimeMs:          res.WallTimeMs,
			WallTimeMs:      res.WallTimeMs,
			CPUTimeMs:       res.CPUTimeMs,
			MemoryKB:        res.MemoryKB,
			ExitCode:        res.ExitCode,
			Stdout:          outResp,
			Stderr:          errResp,
			StdoutTruncated: res.StdoutTruncated,
			StderrTruncated: res.StderrTruncated,
			Reason:          reason,
			VerdictSource:   verdictSource,
			Score:           score,
			SidecarOutputs:  sidecarOutputs,
			SidecarErrors:   sidecarErrors,
		},
		judgeOut: append([]byte(nil), judgeOut...),
	}
}

func (s *Service) runStepPipeline(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig) model.RunResponse {
	if err := validateStepPipelineRequest(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	programs := make(map[string]model.RunProgram, len(req.Programs))
	for _, program := range req.Programs {
		programs[program.ID] = program
	}

	handoffs := map[string]string{}
	stepResults := make([]model.StepResult, 0, len(req.Steps))
	for i, step := range req.Steps {
		program := programs[step.ProgramID]
		stdin := step.Stdin
		if step.StdinFrom != "" {
			stdin = handoffs[step.StdinFrom]
		}
		stepReq := &model.RunRequest{
			Lang:           program.Lang,
			Binaries:       program.Binaries,
			Stdin:          stdin,
			Limits:         step.Limits,
			RuntimeProfile: req.RuntimeProfile,
			EnableNetwork:  program.EnableNetwork,
			EntryPoint:     program.EntryPoint,
		}
		if step.Handoff != nil {
			if strings.EqualFold(step.Handoff.From, "file") || strings.EqualFold(step.Handoff.From, "file_output") {
				stepReq.FileOutputs = []model.OutputFile{{Path: step.Handoff.Path}}
			}
			if step.Handoff.MaxBytes > 0 && stepReq.Limits.OutputBytes < int(step.Handoff.MaxBytes) {
				stepReq.Limits.OutputBytes = int(step.Handoff.MaxBytes)
			}
		}
		finalStep := i == len(req.Steps)-1
		if finalStep {
			stepReq.ExpectedStdout = req.ExpectedStdout
			stepReq.SPJ = req.SPJ
			stepReq.FileOutputs = req.FileOutputs
			stepReq.SidecarOutputs = req.SidecarOutputs
			stepReq.IgnoreTLE = req.IgnoreTLE
		}

		run := s.runOne(ctx, stepReq, hooks, tuning, finalStep)
		stepResult := stepResultFromResponse(step.ID, step.ProgramID, run.response)
		stepResults = append(stepResults, stepResult)

		if !finalStep {
			if run.response.Status != "OK" {
				return aggregateStepResponse(failedStepResponse(run.response, step.ID), stepResults)
			}
			maxBytes := step.Handoff.MaxBytes
			if maxBytes <= 0 {
				maxBytes = int64(outputLimitBytes(stepReq))
			}
			if int64(len(run.judgeOut)) > maxBytes || run.response.StdoutTruncated {
				stepResults[len(stepResults)-1].HandoffBytes = int64(len(run.judgeOut))
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusRE,
					Reason:        fmt.Sprintf("step %s handoff exceeded max_bytes", step.ID),
					VerdictSource: "step:" + step.ID + ":handoff",
				}, stepResults)
			}
			handoffs[step.Handoff.ID] = string(run.judgeOut)
			stepResults[len(stepResults)-1].HandoffBytes = int64(len(run.judgeOut))
			continue
		}

		run.response.VerdictSource = prefixStepVerdictSource(step.ID, run.response.VerdictSource)
		return aggregateStepResponse(run.response, stepResults)
	}

	return model.RunResponse{Status: model.RunStatusInitFail, Reason: "steps did not run"}
}

func runRequestUsesSteps(req *model.RunRequest) bool {
	return len(req.Programs) > 0 || len(req.Steps) > 0
}

func validateStepPipelineRequest(req *model.RunRequest) error {
	if len(req.Programs) == 0 || len(req.Steps) == 0 {
		return fmt.Errorf("programs and steps must be provided together")
	}
	if strings.TrimSpace(req.Lang) != "" || len(req.Binaries) > 0 || strings.TrimSpace(req.Stdin) != "" || strings.TrimSpace(req.EntryPoint) != "" || req.EnableNetwork || !limitsAreZero(req.Limits) {
		return fmt.Errorf("legacy execute fields cannot be combined with programs/steps")
	}
	if len(req.Programs) > maxRunPrograms {
		return fmt.Errorf("too many programs: max %d", maxRunPrograms)
	}
	if len(req.Steps) != maxRunSteps {
		return fmt.Errorf("exactly %d steps are supported", maxRunSteps)
	}
	if len(req.FileOutputs) > 1 {
		return fmt.Errorf("at most one file output is supported")
	}
	if len(req.SidecarOutputs) > maxSidecarOutputSpecs {
		return fmt.Errorf("too many sidecar outputs: max %d", maxSidecarOutputSpecs)
	}
	programs := make(map[string]struct{}, len(req.Programs))
	for _, program := range req.Programs {
		if strings.TrimSpace(program.ID) == "" {
			return fmt.Errorf("program id is required")
		}
		if _, exists := programs[program.ID]; exists {
			return fmt.Errorf("duplicate program id: %s", program.ID)
		}
		if len(program.Binaries) == 0 {
			return fmt.Errorf("program %s has no binaries", program.ID)
		}
		if len(program.Binaries) > maxBinaryFiles {
			return fmt.Errorf("program %s has too many binaries: max %d", program.ID, maxBinaryFiles)
		}
		programs[program.ID] = struct{}{}
	}
	seenSteps := map[string]struct{}{}
	handoffID := ""
	for i, step := range req.Steps {
		if strings.TrimSpace(step.ID) == "" {
			return fmt.Errorf("steps[%d].id is required", i)
		}
		if _, exists := seenSteps[step.ID]; exists {
			return fmt.Errorf("duplicate step id: %s", step.ID)
		}
		seenSteps[step.ID] = struct{}{}
		if _, ok := programs[step.ProgramID]; !ok {
			return fmt.Errorf("step %s references unknown program_id: %s", step.ID, step.ProgramID)
		}
		if step.Limits.TimeMs <= 0 || step.Limits.MemoryMB <= 0 {
			return fmt.Errorf("step %s limits.time_ms and limits.memory_mb are required", step.ID)
		}
		if i == 0 {
			if strings.TrimSpace(step.StdinFrom) != "" {
				return fmt.Errorf("first step cannot use stdin_from")
			}
			if step.Handoff == nil {
				return fmt.Errorf("first step handoff is required")
			}
			if strings.TrimSpace(step.Handoff.ID) == "" {
				return fmt.Errorf("first step handoff.id is required")
			}
			from := strings.ToLower(strings.TrimSpace(step.Handoff.From))
			if from == "" {
				from = "stdout"
			}
			if from != "stdout" && from != "file" && from != "file_output" {
				return fmt.Errorf("first step handoff.from must be stdout or file")
			}
			if (from == "file" || from == "file_output") && strings.TrimSpace(step.Handoff.Path) == "" {
				return fmt.Errorf("first step handoff.path is required for file handoff")
			}
			if step.Handoff.MaxBytes < 0 || step.Handoff.MaxBytes > maxStepHandoffBytes {
				return fmt.Errorf("first step handoff.max_bytes must be between 0 and %d", maxStepHandoffBytes)
			}
			handoffID = step.Handoff.ID
			continue
		}
		if strings.TrimSpace(step.StdinFrom) != handoffID {
			return fmt.Errorf("second step stdin_from must reference first step handoff id")
		}
		if step.Handoff != nil {
			return fmt.Errorf("second step handoff is not supported")
		}
	}
	return nil
}

func limitsAreZero(l model.Limits) bool {
	return l.TimeMs == 0 && l.MemoryMB == 0 && l.OutputBytes == 0 && l.WorkspaceBytes == 0
}

func stepResultFromResponse(id, programID string, resp model.RunResponse) model.StepResult {
	return model.StepResult{
		ID:              id,
		ProgramID:       programID,
		Status:          resp.Status,
		TimeMs:          resp.TimeMs,
		WallTimeMs:      resp.WallTimeMs,
		CPUTimeMs:       resp.CPUTimeMs,
		MemoryKB:        resp.MemoryKB,
		ExitCode:        resp.ExitCode,
		Stdout:          resp.Stdout,
		Stderr:          resp.Stderr,
		StdoutTruncated: resp.StdoutTruncated,
		StderrTruncated: resp.StderrTruncated,
		Reason:          resp.Reason,
		VerdictSource:   resp.VerdictSource,
	}
}

func failedStepResponse(resp model.RunResponse, stepID string) model.RunResponse {
	if strings.TrimSpace(resp.Reason) != "" {
		resp.Reason = fmt.Sprintf("step %s failed: %s", stepID, resp.Reason)
	} else {
		resp.Reason = fmt.Sprintf("step %s failed", stepID)
	}
	resp.VerdictSource = prefixStepVerdictSource(stepID, resp.VerdictSource)
	return resp
}

func prefixStepVerdictSource(stepID, source string) string {
	if strings.TrimSpace(source) == "" {
		return "step:" + stepID
	}
	return "step:" + stepID + ":" + source
}

func aggregateStepResponse(resp model.RunResponse, steps []model.StepResult) model.RunResponse {
	var wallMs int64
	var cpuMs int64
	var memoryKB int64
	for _, step := range steps {
		wallMs += step.WallTimeMs
		cpuMs += step.CPUTimeMs
		if step.MemoryKB > memoryKB {
			memoryKB = step.MemoryKB
		}
	}
	resp.TimeMs = wallMs
	resp.WallTimeMs = wallMs
	resp.CPUTimeMs = cpuMs
	if memoryKB > resp.MemoryKB {
		resp.MemoryKB = memoryKB
	}
	resp.Steps = steps
	return resp
}
