package execute

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/runvalidation"
	"aonohako/internal/timing"
)

const (
	defaultMaxOutputBytes        = 64 << 10
	hardMaxOutputBytes           = 64 << 20
	maxResponseOutputBytes       = defaultMaxOutputBytes
	defaultWorkspaceBytes        = 128 << 20
	hardMaxWorkspaceBytes        = 1 << 30
	maxBinaryFiles               = runvalidation.MaxBinaryFiles
	maxSidecarOutputSpecs        = 64
	addressSpaceSlackKB          = 8 << 10
	sandboxThreadLimit           = 128
	maxBinaryFileBytes           = runvalidation.MaxBinaryFileBytes
	maxBinaryTotalBytes          = runvalidation.MaxBinaryTotalBytes
	maxCapturedFileBytes         = 8 << 20
	maxCapturedSidecarTotalBytes = 16 << 20
	maxImageStreamBytes          = 8 << 20
	maxImageReadChunkBytes       = 256 << 10
	maxImageEventBytes           = 1 << 20
	maxImageEventsPerRead        = 8
	defaultSPJTimeMs             = 1000
	defaultSPJMemoryMB           = 256
	ocamlRunParam                = "s=32k"
)

var errStepStdinPartsTooLarge = errors.New("stdin_parts exceeded max bytes")

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

func NewWithConfig(cfg config.Config) *Service {
	profiles := make(map[string]config.RuntimeTuningConfig, len(cfg.Execution.RuntimeTuningProfiles))
	for name, tuning := range cfg.Execution.RuntimeTuningProfiles {
		profiles[name] = tuning.WithSafeDefaults()
	}
	return &Service{
		deploymentTarget:      cfg.Execution.Platform.DeploymentTarget,
		runtimeTuning:         cfg.Execution.RuntimeTuning.WithSafeDefaults(),
		runtimeTuningProfiles: profiles,
		cgroupParentDir:       cfg.Execution.Cgroup.ParentDir,
	}
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
	if req.Interactor != nil {
		return s.runInteractive(ctx, req, hooks, tuning)
	}
	if runvalidation.UsesSteps(req) {
		return s.runStepPipeline(ctx, req, hooks, tuning)
	}
	return s.runOne(ctx, req, hooks, tuning, true).response
}

func (s *Service) runOne(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig, evaluateOutput bool) sandboxRunResult {
	return s.runOneWithStdin(ctx, req, nil, 0, hooks, tuning, evaluateOutput)
}

func (s *Service) runOneWithStdin(ctx context.Context, req *model.RunRequest, stdin io.Reader, stdinMaxBytes int64, hooks Hooks, tuning config.RuntimeTuningConfig, evaluateOutput bool) sandboxRunResult {
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
		slog.Warn("execute work directory creation failed", "err", err)
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "work directory creation failed"}}
	}
	defer os.RemoveAll(workDir)

	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		slog.Warn("execute workspace preparation failed", "err", err)
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "workspace preparation failed"}}
	}

	primaryPath, runLang, err := materializeFiles(ws, req)
	if err != nil {
		slog.Warn("execute file materialization failed", "err", err)
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "file materialization failed"}}
	}

	cmdArgs := buildCommandWithRuntimeTuning(primaryPath, runLang, req, tuning)
	if len(cmdArgs) == 0 {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "empty command"}}
	}

	if stdin == nil && strings.TrimSpace(req.StdinURL) != "" {
		urlMaxBytes := stdinURLMaxBytes(req.Limits)
		stdinURLReader, err := openStdinURL(ctx, req.StdinURL, urlMaxBytes)
		if err != nil {
			return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: " + err.Error()}}
		}
		defer stdinURLReader.Close()
		stdin = stdinURLReader
		stdinMaxBytes = urlMaxBytes
	}
	if stdinMaxBytes <= 0 {
		stdinMaxBytes = runvalidation.MaxTextFieldBytes
	}

	res := runCommandWithSandbox(ctx, ws, cmdArgs, req, stdin, stdinMaxBytes, hooks, capturedOutputLimit, tuning, s.cgroupParentDir)
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

	responseOutputLimit := responseOutputLimitBytes(req)
	var outResp, errResp string
	if status == model.RunStatusWA || status == model.RunStatusRE || (status == model.RunStatusTLE && req.IgnoreTLE) {
		outResp = clipUTF8(judgeOut, responseOutputLimit)
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		errResp = clipUTF8(fullErr, responseOutputLimit)
	}

	if hooks.OnLog != nil {
		if len(rawOut) > 0 {
			hooks.OnLog("stdout", clipUTF8(rawOut, responseOutputLimit))
		}
		if len(fullErr) > 0 {
			hooks.OnLog("stderr", clipUTF8(fullErr, responseOutputLimit))
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
	if err := runvalidation.ValidateStepPipeline(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	programs := make(map[string]model.RunProgram, len(req.Programs))
	for _, program := range req.Programs {
		programs[program.ID] = program
	}

	handoffDir, err := createRunWorkDir()
	if err != nil {
		slog.Warn("execute step handoff directory creation failed", "err", err)
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "handoff directory creation failed"}
	}
	defer os.RemoveAll(handoffDir)

	handoffs := map[string]string{}
	stepResults := make([]model.StepResult, 0, len(req.Steps))
	for i, step := range req.Steps {
		program := programs[step.ProgramID]
		stdin, stdinReader, closeStdin, stdinVerdictSource, stdinMaxBytes, err := prepareStepStdin(ctx, step, handoffs, handoffDir)
		if err != nil {
			slog.Warn("execute step stdin preparation failed", "step", step.ID, "err", err)
			return aggregateStepResponse(model.RunResponse{
				Status:        model.RunStatusInitFail,
				Reason:        fmt.Sprintf("step %s %s", step.ID, err.Error()),
				VerdictSource: "step:" + step.ID + ":" + stdinVerdictSource,
			}, stepResults)
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
		handoffFromFile := false
		if step.Handoff != nil {
			if strings.EqualFold(step.Handoff.From, "file") || strings.EqualFold(step.Handoff.From, "file_output") {
				handoffFromFile = true
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

		run := s.runOneWithStdin(ctx, stepReq, stdinReader, stdinMaxBytes, hooks, tuning, finalStep)
		if closeStdin != nil {
			_ = closeStdin()
		}
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
			stepResults[len(stepResults)-1].HandoffBytes = int64(len(run.judgeOut))
			handoffExceeded := int64(len(run.judgeOut)) > maxBytes
			if !handoffFromFile && run.response.StdoutTruncated {
				handoffExceeded = true
			}
			if handoffExceeded {
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusRE,
					Reason:        fmt.Sprintf("step %s handoff exceeded max_bytes", step.ID),
					VerdictSource: "step:" + step.ID + ":handoff",
				}, stepResults)
			}
			if handoffFromFile && run.response.StdoutTruncated {
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusRE,
					Reason:        fmt.Sprintf("step %s stdout exceeded output limit", step.ID),
					VerdictSource: "step:" + step.ID + ":stdout",
				}, stepResults)
			}
			handoffFile, err := os.CreateTemp(handoffDir, "handoff-*")
			if err != nil {
				slog.Warn("execute step handoff file creation failed", "step", step.ID, "err", err)
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusInitFail,
					Reason:        fmt.Sprintf("step %s handoff creation failed", step.ID),
					VerdictSource: "step:" + step.ID + ":handoff",
				}, stepResults)
			}
			if _, err := handoffFile.Write(run.judgeOut); err != nil {
				_ = handoffFile.Close()
				slog.Warn("execute step handoff write failed", "step", step.ID, "err", err)
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusInitFail,
					Reason:        fmt.Sprintf("step %s handoff write failed", step.ID),
					VerdictSource: "step:" + step.ID + ":handoff",
				}, stepResults)
			}
			if err := handoffFile.Close(); err != nil {
				slog.Warn("execute step handoff close failed", "step", step.ID, "err", err)
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusInitFail,
					Reason:        fmt.Sprintf("step %s handoff close failed", step.ID),
					VerdictSource: "step:" + step.ID + ":handoff",
				}, stepResults)
			}
			handoffs[step.Handoff.ID] = handoffFile.Name()
			continue
		}

		run.response.VerdictSource = prefixStepVerdictSource(step.ID, run.response.VerdictSource)
		return aggregateStepResponse(run.response, stepResults)
	}

	return model.RunResponse{Status: model.RunStatusInitFail, Reason: "steps did not run"}
}

func prepareStepStdin(ctx context.Context, step model.RunStep, handoffs map[string]string, scratchDir string) (string, io.Reader, func() error, string, int64, error) {
	if strings.TrimSpace(step.StdinURL) != "" {
		maxBytes := stdinURLMaxBytes(step.Limits)
		stdinURLReader, err := openStdinURL(ctx, step.StdinURL, maxBytes)
		if err != nil {
			return "", nil, nil, "stdin_url", 0, err
		}
		return "", stdinURLReader, stdinURLReader.Close, "stdin_url", maxBytes, nil
	}
	if len(step.StdinParts) > 0 {
		maxBytes := stdinURLMaxBytes(step.Limits)
		stdinFile, err := assembleStepStdinParts(ctx, step, handoffs, scratchDir, maxBytes)
		if err != nil {
			return "", nil, nil, "stdin_parts", 0, err
		}
		return "", stdinFile, stdinFile.Close, "stdin_parts", maxBytes, nil
	}
	if step.StdinFrom == "" {
		return step.Stdin, nil, nil, "", runvalidation.MaxTextFieldBytes, nil
	}

	handoffPath := handoffs[step.StdinFrom]
	if handoffPath == "" {
		return "", nil, nil, "handoff", 0, fmt.Errorf("stdin handoff not found")
	}
	stdinFile, err := os.Open(handoffPath)
	if err != nil {
		return "", nil, nil, "handoff", 0, fmt.Errorf("handoff open failed")
	}
	return "", stdinFile, stdinFile.Close, "handoff", runvalidation.MaxTextFieldBytes, nil
}

func assembleStepStdinParts(ctx context.Context, step model.RunStep, handoffs map[string]string, scratchDir string, maxBytes int64) (*os.File, error) {
	stdinFile, err := os.CreateTemp(scratchDir, "stdin-*")
	if err != nil {
		return nil, fmt.Errorf("stdin_parts file creation failed")
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = stdinFile.Close()
			_ = os.Remove(stdinFile.Name())
		}
	}()

	writer := &byteLimitWriter{
		w:     stdinFile,
		limit: maxBytes,
	}
	for _, part := range step.StdinParts {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text":
			if strings.TrimSpace(part.DataURL) != "" {
				stdinURLReader, err := openStdinURL(ctx, part.DataURL, maxBytes-writer.written)
				if err != nil {
					return nil, err
				}
				_, copyErr := io.Copy(writer, stdinURLReader)
				closeErr := stdinURLReader.Close()
				if copyErr != nil {
					return nil, copyErr
				}
				if closeErr != nil {
					return nil, fmt.Errorf("stdin_url close failed")
				}
			} else {
				if _, err := writer.Write([]byte(part.Data)); err != nil {
					return nil, err
				}
			}
		case "handoff":
			handoffID := stdinPartHandoffID(part)
			handoffPath := handoffs[handoffID]
			if handoffPath == "" {
				return nil, fmt.Errorf("stdin handoff not found")
			}
			handoffFile, err := os.Open(handoffPath)
			if err != nil {
				return nil, fmt.Errorf("handoff open failed")
			}
			_, copyErr := io.Copy(writer, handoffFile)
			closeErr := handoffFile.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, fmt.Errorf("handoff close failed")
			}
		default:
			return nil, fmt.Errorf("stdin_parts contains unsupported part type")
		}
	}
	if _, err := stdinFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("stdin_parts file rewind failed")
	}
	removeOnError = false
	return stdinFile, nil
}

func stdinURLMaxBytes(limits model.Limits) int64 {
	limit := limits.WorkspaceBytes
	if limit <= 0 {
		limit = defaultWorkspaceBytes
	}
	if limit > hardMaxWorkspaceBytes {
		limit = hardMaxWorkspaceBytes
	}
	return limit
}

type byteLimitWriter struct {
	w       io.Writer
	limit   int64
	written int64
}

func (w *byteLimitWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.written
	if remaining <= 0 {
		return 0, errStepStdinPartsTooLarge
	}
	if int64(len(p)) > remaining {
		allowed := int(remaining)
		n, err := w.w.Write(p[:allowed])
		w.written += int64(n)
		if err != nil {
			return n, err
		}
		return n, errStepStdinPartsTooLarge
	}
	n, err := w.w.Write(p)
	w.written += int64(n)
	return n, err
}

func stdinPartHandoffID(part model.StdinPart) string {
	if strings.TrimSpace(part.From) != "" {
		return strings.TrimSpace(part.From)
	}
	return strings.TrimSpace(part.ID)
}

func stepResultFromResponse(id, programID string, resp model.RunResponse) model.StepResult {
	status := resp.Status
	if status == "OK" {
		status = model.RunStatusAccepted
	}
	return model.StepResult{
		ID:              id,
		ProgramID:       programID,
		Status:          status,
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
