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
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/runvalidation"
	"aonohako/internal/timing"
)

const (
	DefaultOutputBytes           = 64 << 10
	DefaultWorkspaceBytes        = 128 << 20
	MaxCapturedFileBytes         = 8 << 20
	MaxCapturedSidecarTotalBytes = 16 << 20

	defaultMaxOutputBytes        = DefaultOutputBytes
	hardMaxOutputBytes           = runvalidation.MaxOutputBytes
	maxResponseOutputBytes       = DefaultOutputBytes
	maxResponseCaptureBytes      = runvalidation.MaxCaptureBytes
	defaultWorkspaceBytes        = DefaultWorkspaceBytes
	hardMaxWorkspaceBytes        = runvalidation.MaxWorkspaceBytes
	maxBinaryFiles               = runvalidation.MaxBinaryFiles
	maxSidecarOutputSpecs        = 64
	addressSpaceSlackKB          = 8 << 10
	sandboxThreadLimit           = 128
	maxBinaryFileBytes           = runvalidation.MaxBinaryFileBytes
	maxBinaryTotalBytes          = runvalidation.MaxBinaryTotalBytes
	maxCapturedFileBytes         = MaxCapturedFileBytes
	maxCapturedSidecarTotalBytes = MaxCapturedSidecarTotalBytes
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

func capturedOutputValue(output []byte, limit int) (string, bool) {
	value := clipUTF8(output, limit)
	return value, len(value) < len(output)
}

func emitCapturedLog(hooks Hooks, stream string, output []byte, limit int) {
	if hooks.OnLog == nil || len(output) == 0 {
		return
	}
	value, _ := capturedOutputValue(output, limit)
	if value != "" {
		hooks.OnLog(stream, value)
	}
}

type Service struct {
	deploymentTarget            platform.DeploymentTarget
	runtimeTuning               config.RuntimeTuningConfig
	runtimeTuningProfiles       map[string]config.RuntimeTuningConfig
	cgroupParentDir             string
	communicationEnabled        bool
	communicationMemoryBudgetMB int
	networkEgressIsolated       bool
	stdinURLTimeout             time.Duration
}

type sandboxRunResult struct {
	response            model.RunResponse
	judgeOut            []byte
	stdoutLimitExceeded bool
}

func New() *Service {
	opts, err := platform.CurrentRuntimeOptions()
	if err != nil {
		return &Service{runtimeTuning: config.DefaultRuntimeTuningConfig(), stdinURLTimeout: stdinURLDownloadTimeout}
	}
	return &Service{deploymentTarget: opts.DeploymentTarget, runtimeTuning: config.DefaultRuntimeTuningConfig(), stdinURLTimeout: stdinURLDownloadTimeout}
}

func NewWithConfig(cfg config.Config) *Service {
	profiles := make(map[string]config.RuntimeTuningConfig, len(cfg.Execution.RuntimeTuningProfiles))
	for name, tuning := range cfg.Execution.RuntimeTuningProfiles {
		profiles[name] = tuning.WithSafeDefaults()
	}
	return &Service{
		deploymentTarget:            cfg.Execution.Platform.DeploymentTarget,
		runtimeTuning:               cfg.Execution.RuntimeTuning.WithSafeDefaults(),
		runtimeTuningProfiles:       profiles,
		cgroupParentDir:             cfg.Execution.Cgroup.ParentDir,
		communicationEnabled:        cfg.CommunicationEnabled,
		communicationMemoryBudgetMB: cfg.CommunicationMemoryBudgetMB,
		networkEgressIsolated:       cfg.NetworkEgressIsolated,
		stdinURLTimeout:             stdinURLDownloadTimeout,
	}
}

func (s *Service) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}
	if _, err := runvalidation.ValidateBinaryBudget(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := pythonpolicy.ValidateOptionalLibraryMode(req.PythonLibraryMode); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid python_library_mode: " + err.Error()}
	}
	if req.PythonLibraryMode != "" && !runvalidation.UsesPython(req) {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "python_library_mode requires a Python execution target"}
	}
	if req.SPJ != nil {
		if err := runvalidation.ValidateSPJ(req.SPJ); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
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
	if req.Communication != nil {
		return s.runCommunication(ctx, req, hooks, tuning)
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
	if reason := s.networkRequestRejection(req.EnableNetwork); reason != "" {
		return sandboxRunResult{response: model.RunResponse{
			Status: model.RunStatusInitFail,
			Reason: reason,
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
		stdinURLReader, err := openStdinURL(ctx, req.StdinURL, urlMaxBytes, nil)
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
	judgeInputPath := ""
	if hasSPJ(req) {
		inputReader := stdin
		if inputReader == nil {
			inputReader = strings.NewReader(req.Stdin)
		}
		judgeInput, err := os.CreateTemp(ws.RootDir, ".judge-input-*")
		if err != nil {
			return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin materialization failed"}}
		}
		defer func() {
			_ = judgeInput.Close()
			_ = os.Remove(judgeInput.Name())
		}()
		written, copyErr := io.Copy(judgeInput, io.LimitReader(inputReader, stdinMaxBytes+1))
		if copyErr != nil {
			slog.Warn("execute judge input materialization failed", "err", copyErr)
			return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin materialization failed"}}
		}
		if written > stdinMaxBytes {
			return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin too large"}}
		}
		if err := judgeInput.Chmod(0o400); err != nil {
			slog.Warn("execute judge input hardening failed", "err", err)
			return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin materialization failed"}}
		}
		if _, err := judgeInput.Seek(0, io.SeekStart); err != nil {
			slog.Warn("execute judge input rewind failed", "err", err)
			return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin materialization failed"}}
		}
		judgeInputPath = judgeInput.Name()
		stdin = &sandboxPreparedStdin{file: judgeInput}
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
		status, score, evalReason, verdictSource = evaluateRunStatus(ctx, ws, req, res, judgeOut, judgeSource, judgeInputPath, sidecarOutputs, tuning, s.cgroupParentDir)
	}
	reason := res.Reason
	if evalReason != "" {
		reason = evalReason
	}

	stdoutResponseLimit := responseStdoutLimitBytes(req)
	stderrResponseLimit := responseStderrLimitBytes(req)
	var outResp, errResp string
	stdoutResponseTruncated := false
	stderrResponseTruncated := false
	if status == model.RunStatusWA || status == model.RunStatusRE || (status == model.RunStatusTLE && req.IgnoreTLE) {
		outResp, stdoutResponseTruncated = capturedOutputValue(judgeOut, stdoutResponseLimit)
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		errResp, stderrResponseTruncated = capturedOutputValue(fullErr, stderrResponseLimit)
	}

	emitCapturedLog(hooks, "stdout", rawOut, stdoutResponseLimit)
	emitCapturedLog(hooks, "stderr", fullErr, stderrResponseLimit)

	return sandboxRunResult{
		response: model.RunResponse{
			Status:           status,
			TimeMs:           res.WallTimeMs,
			WallTimeMs:       res.WallTimeMs,
			CPUTimeMs:        res.CPUTimeMs,
			ProcessCPUTimeMs: res.ProcessCPUTimeMs,
			MemoryKB:         res.MemoryKB,
			ExitCode:         res.ExitCode,
			Stdout:           outResp,
			Stderr:           errResp,
			StdoutTruncated:  res.StdoutTruncated || stdoutResponseTruncated,
			StderrTruncated:  res.StderrTruncated || stderrResponseTruncated,
			Reason:           reason,
			VerdictSource:    verdictSource,
			Score:            score,
			SidecarOutputs:   sidecarOutputs,
			SidecarErrors:    sidecarErrors,
		},
		judgeOut:            append([]byte(nil), judgeOut...),
		stdoutLimitExceeded: res.StdoutTruncated,
	}
}

func (s *Service) networkRequestRejection(enabled bool) string {
	if !enabled {
		return ""
	}
	if s.deploymentTarget == platform.DeploymentTargetCloudRun {
		return "embedded helper execution on cloudrun does not support enable_network=true; use an egress-isolated self-hosted remote runner for networked workloads"
	}
	if s.deploymentTarget != platform.DeploymentTargetDev && !s.networkEgressIsolated {
		return "enable_network=true outside dev requires an egress-isolated runner"
	}
	return ""
}

func (s *Service) runStepPipeline(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig) model.RunResponse {
	if err := runvalidation.ValidateStepPipeline(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	programs := make(map[string]model.RunProgram, len(req.Programs))
	for _, program := range req.Programs {
		programs[strings.TrimSpace(program.ID)] = program
	}

	handoffDir, err := createRunWorkDir()
	if err != nil {
		slog.Warn("execute step handoff directory creation failed", "err", err)
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "handoff directory creation failed"}
	}
	defer os.RemoveAll(handoffDir)

	handoffs := map[string]string{}
	stepResults := make([]model.StepResult, 0, len(req.Steps))
	stdinURLTimeout := s.stdinURLTimeout
	if stdinURLTimeout <= 0 {
		stdinURLTimeout = stdinURLDownloadTimeout
	}
	stdinURLBudget := &stdinURLDownloadBudget{remaining: stdinURLTimeout}
	for i, step := range req.Steps {
		program := programs[strings.TrimSpace(step.ProgramID)]
		stdin, stdinReader, closeStdin, stdinVerdictSource, stdinMaxBytes, err := prepareStepStdin(ctx, step, handoffs, handoffDir, stdinURLBudget)
		if err != nil {
			slog.Warn("execute step stdin preparation failed", "step", step.ID, "err", err)
			return aggregateStepResponse(model.RunResponse{
				Status:        model.RunStatusInitFail,
				Reason:        fmt.Sprintf("step %s %s", step.ID, err.Error()),
				VerdictSource: "step:" + step.ID + ":" + stdinVerdictSource,
			}, stepResults)
		}
		stepReq := &model.RunRequest{
			Lang:              program.Lang,
			Binaries:          program.Binaries,
			Stdin:             stdin,
			Limits:            step.Limits,
			CaptureLimits:     req.CaptureLimits,
			RuntimeProfile:    req.RuntimeProfile,
			PythonLibraryMode: req.PythonLibraryMode,
			EnableNetwork:     program.EnableNetwork,
			EntryPoint:        program.EntryPoint,
		}
		handoffFromFile := false
		if step.Handoff != nil {
			handoffFrom := strings.ToLower(strings.TrimSpace(step.Handoff.From))
			if handoffFrom == "file" || handoffFrom == "file_output" {
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
			if !handoffFromFile && run.stdoutLimitExceeded {
				handoffExceeded = true
			}
			if handoffExceeded {
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusRE,
					Reason:        fmt.Sprintf("step %s handoff exceeded max_bytes", step.ID),
					VerdictSource: "step:" + step.ID + ":handoff",
				}, stepResults)
			}
			if handoffFromFile && run.stdoutLimitExceeded {
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
			handoffs[strings.TrimSpace(step.Handoff.ID)] = handoffFile.Name()
			continue
		}

		run.response.VerdictSource = prefixStepVerdictSource(step.ID, run.response.VerdictSource)
		return aggregateStepResponse(run.response, stepResults)
	}

	return model.RunResponse{Status: model.RunStatusInitFail, Reason: "steps did not run"}
}

func prepareStepStdin(ctx context.Context, step model.RunStep, handoffs map[string]string, scratchDir string, budget *stdinURLDownloadBudget) (string, io.Reader, func() error, string, int64, error) {
	if strings.TrimSpace(step.StdinURL) != "" {
		maxBytes := stdinURLMaxBytes(step.Limits)
		stdinURLReader, err := openStdinURL(ctx, step.StdinURL, maxBytes, budget)
		if err != nil {
			return "", nil, nil, "stdin_url", 0, err
		}
		return "", stdinURLReader, stdinURLReader.Close, "stdin_url", maxBytes, nil
	}
	if len(step.StdinParts) > 0 {
		maxBytes := stdinURLMaxBytes(step.Limits)
		stdinFile, err := assembleStepStdinParts(ctx, step, handoffs, scratchDir, maxBytes, budget)
		if err != nil {
			return "", nil, nil, "stdin_parts", 0, err
		}
		return "", stdinFile, stdinFile.Close, "stdin_parts", maxBytes, nil
	}
	stdinFrom := strings.TrimSpace(step.StdinFrom)
	if stdinFrom == "" {
		return step.Stdin, nil, nil, "", runvalidation.MaxTextFieldBytes, nil
	}

	handoffPath := handoffs[stdinFrom]
	if handoffPath == "" {
		return "", nil, nil, "handoff", 0, fmt.Errorf("stdin handoff not found")
	}
	stdinFile, err := os.Open(handoffPath)
	if err != nil {
		return "", nil, nil, "handoff", 0, fmt.Errorf("handoff open failed")
	}
	return "", stdinFile, stdinFile.Close, "handoff", runvalidation.MaxTextFieldBytes, nil
}

func assembleStepStdinParts(ctx context.Context, step model.RunStep, handoffs map[string]string, scratchDir string, maxBytes int64, budget *stdinURLDownloadBudget) (*os.File, error) {
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
				stdinURLReader, err := openStdinURL(ctx, part.DataURL, maxBytes-writer.written, budget)
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
		ID:               id,
		ProgramID:        programID,
		Status:           status,
		TimeMs:           resp.TimeMs,
		WallTimeMs:       resp.WallTimeMs,
		CPUTimeMs:        resp.CPUTimeMs,
		ProcessCPUTimeMs: resp.ProcessCPUTimeMs,
		MemoryKB:         resp.MemoryKB,
		ExitCode:         resp.ExitCode,
		Stdout:           resp.Stdout,
		Stderr:           resp.Stderr,
		StdoutTruncated:  resp.StdoutTruncated,
		StderrTruncated:  resp.StderrTruncated,
		Reason:           resp.Reason,
		VerdictSource:    resp.VerdictSource,
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
	var processCPUTimeMs int64
	var memoryKB int64
	for _, step := range steps {
		wallMs += step.WallTimeMs
		cpuMs += step.CPUTimeMs
		processCPUTimeMs += step.ProcessCPUTimeMs
		if step.MemoryKB > memoryKB {
			memoryKB = step.MemoryKB
		}
	}
	resp.TimeMs = wallMs
	resp.WallTimeMs = wallMs
	resp.CPUTimeMs = cpuMs
	resp.ProcessCPUTimeMs = processCPUTimeMs
	if memoryKB > resp.MemoryKB {
		resp.MemoryKB = memoryKB
	}
	resp.Steps = steps
	return resp
}
