package execute

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/runvalidation"
	"aonohako/internal/timing"
)

const interactiveAcceptedGrace = 100 * time.Millisecond

type interactiveRunResult struct {
	response              model.RunResponse
	participantStdout     []byte
	participantFileOutput []byte
	participantFileErr    error
	interactorOutputBytes int64
	interactorOutputErr   error
}

type interactivePipeWriter struct {
	w      *io.PipeWriter
	closed atomic.Bool
	errMu  sync.Mutex
	err    error
}

func (w *interactivePipeWriter) Write(p []byte) (int, error) {
	if w == nil || w.w == nil || w.closed.Load() {
		if w != nil {
			w.recordError(io.ErrClosedPipe)
		}
		return 0, io.ErrClosedPipe
	}
	n, err := w.w.Write(p)
	w.recordError(err)
	return n, err
}

func (w *interactivePipeWriter) Close() {
	if w == nil || w.w == nil {
		return
	}
	if w.closed.CompareAndSwap(false, true) {
		w.recordError(w.w.Close())
	}
}

func (w *interactivePipeWriter) recordError(err error) {
	if w == nil || err == nil {
		return
	}
	w.errMu.Lock()
	defer w.errMu.Unlock()
	if w.err == nil {
		w.err = err
	}
}

func (w *interactivePipeWriter) Err() error {
	if w == nil {
		return nil
	}
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.err
}

func (s *Service) runInteractive(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig) model.RunResponse {
	return s.runInteractiveExecution(ctx, req, nil, 0, nil, 0, false, hooks, tuning).response
}

func (s *Service) runInteractiveExecution(ctx context.Context, req *model.RunRequest, stdin io.Reader, stdinMaxBytes int64, interactorOutputDest io.Writer, interactorOutputMaxBytes int64, captureParticipantStdout bool, hooks Hooks, tuning config.RuntimeTuningConfig) interactiveRunResult {
	startWall := timing.MonotonicNow()
	if req.Interactor == nil {
		return interactiveRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "interactor is required"}}
	}
	if reason := s.networkRequestRejection(req.EnableNetwork); reason != "" {
		return interactiveRunResult{response: model.RunResponse{
			Status: model.RunStatusInitFail,
			Reason: reason,
		}}
	}
	if len(req.SidecarOutputs) > maxSidecarOutputSpecs {
		return interactiveRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("too many sidecar outputs: max %d", maxSidecarOutputSpecs)}}
	}
	if len(req.Binaries) > maxBinaryFiles {
		return interactiveRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("too many binaries: max %d", maxBinaryFiles)}}
	}
	if len(req.Binaries) == 0 {
		return interactiveRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "no binaries"}}
	}

	contestantWorkDir, contestantWS, contestantArgs, contestantInit := prepareInteractiveCommand(req, tuning, "contestant")
	if contestantInit != nil {
		return interactiveRunResult{response: *contestantInit}
	}
	defer os.RemoveAll(contestantWorkDir)

	interactorReq := interactorRunRequest(req)
	interactorWorkDir, interactorWS, interactorArgs, interactorInit := prepareInteractiveCommand(interactorReq, tuning, "interactor")
	if interactorInit != nil {
		return interactiveRunResult{response: *interactorInit}
	}
	defer os.RemoveAll(interactorWorkDir)
	if err := validateInteractivePeerRuntimeIsolation(req.Lang, interactorReq.Lang); err != nil {
		return interactiveRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}}
	}

	var inputPath string
	var err error
	if stdin != nil {
		if stdinMaxBytes <= 0 {
			stdinMaxBytes = runvalidation.MaxTextFieldBytes
		}
		inputPath, err = writeTempFileFromReader(filepath.Join(interactorWS.RootDir, ".tmp"), "interactive-input-*", stdin, stdinMaxBytes)
	} else {
		inputPath, err = writeStdinTempFile(ctx, filepath.Join(interactorWS.RootDir, ".tmp"), "interactive-input-*", req, "")
	}
	if err != nil {
		return interactiveRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "interactive input materialization failed: " + err.Error()}}
	}
	answerPath, err := writeTempFile(filepath.Join(interactorWS.RootDir, ".tmp"), "interactive-answer-*", req.ExpectedStdout)
	if err != nil {
		return interactiveRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "interactive answer materialization failed: " + err.Error()}}
	}
	outputPath := filepath.Join(interactorWS.RootDir, ".tmp", "interactive-output")
	interactorArgs = append(interactorArgs, inputPath, outputPath, answerPath)

	contestantInR, contestantInW := io.Pipe()
	interactorInR, interactorInW := io.Pipe()
	contestantIn := &interactivePipeWriter{w: contestantInW}
	interactorIn := &interactivePipeWriter{w: interactorInW}
	defer contestantInR.Close()
	defer interactorInR.Close()
	defer contestantIn.Close()
	defer interactorIn.Close()

	contestantCtx, cancelContestant := context.WithCancel(ctx)
	interactorCtx, cancelInteractor := context.WithCancel(ctx)
	defer cancelContestant()
	defer cancelInteractor()

	var contestantDeadlineHit atomic.Bool
	var contestantFinished atomic.Bool
	var interactorFinished atomic.Bool
	var contestantCanceledByInteractor atomic.Bool
	contestantLimitMs := max(1, s.cpuNormalizer.WallLimitMillis(req.Limits.TimeMs))
	contestantTimer := time.AfterFunc(time.Duration(contestantLimitMs)*time.Millisecond, func() {
		if contestantFinished.Load() {
			return
		}
		contestantDeadlineHit.Store(true)
		cancelContestant()
		cancelInteractor()
	})
	defer contestantTimer.Stop()
	interactorLimitMs := max(1, s.cpuNormalizer.WallLimitMillis(interactorReq.Limits.TimeMs))
	interactorTimer := time.AfterFunc(time.Duration(interactorLimitMs)*time.Millisecond, func() {
		if interactorFinished.Load() {
			return
		}
		if !contestantFinished.Load() {
			contestantCanceledByInteractor.Store(true)
		}
		cancelInteractor()
		cancelContestant()
	})
	defer interactorTimer.Stop()

	contestantCh := make(chan execResult, 1)
	interactorCh := make(chan execResult, 1)
	go func() {
		res := executeSandboxCommandWithStreams(
			contestantCtx,
			contestantWS,
			contestantArgs,
			req,
			sandboxStreamConfig{
				stdin:         contestantInR,
				liveStdin:     true,
				stdout:        interactorIn,
				onStdoutDone:  interactorIn.Close,
				cpuNormalizer: s.cpuNormalizer,
			},
			hooks,
			outputLimitBytes(req),
			tuning,
			s.cgroupParentDir,
		)
		contestantFinished.Store(true)
		contestantCh <- res
	}()
	go func() {
		res := executeSandboxCommandWithStreams(
			interactorCtx,
			interactorWS,
			interactorArgs,
			interactorReq,
			sandboxStreamConfig{
				stdin:        interactorInR,
				liveStdin:    true,
				stdout:       contestantIn,
				onStdoutDone: contestantIn.Close,
				identity: sandboxIdentity{
					uid: interactiveJudgeSandboxUID,
					gid: interactiveJudgeSandboxGID,
				},
				cpuNormalizer: s.cpuNormalizer,
			},
			Hooks{},
			outputLimitBytes(interactorReq),
			tuning,
			s.cgroupParentDir,
		)
		interactorFinished.Store(true)
		interactorCh <- res
	}()

	var contestantRes execResult
	var interactorRes execResult
	gotContestant := false
	gotInteractor := false
	for !gotContestant || !gotInteractor {
		select {
		case res := <-contestantCh:
			contestantRes = res
			gotContestant = true
			contestantTimer.Stop()
			contestantIn.Close()
			contestantStatus, _, _ := classifyRunStatusWithoutOutput(req, res)
			if contestantStatus != "OK" {
				cancelInteractor()
			}
		case res := <-interactorCh:
			interactorRes = res
			gotInteractor = true
			interactorTimer.Stop()
			interactorIn.Close()
			interactorStatus, _, _ := classifyInteractorStatus(interactorReq, res)
			if interactorStatus == model.RunStatusAccepted {
				time.AfterFunc(interactiveAcceptedGrace, func() {
					if !contestantFinished.Load() {
						contestantCanceledByInteractor.Store(true)
						cancelContestant()
					}
				})
				continue
			}
			if !contestantFinished.Load() {
				contestantCanceledByInteractor.Store(true)
			}
			cancelContestant()
		}
	}
	cancelContestant()
	cancelInteractor()
	contestantIn.Close()
	interactorIn.Close()
	if err := interactorIn.Err(); err != nil && contestantRes.Status == "OK" {
		contestantRes.Status = model.RunStatusRE
		contestantRes.Reason = "interactive relay to interactor failed: " + err.Error()
		contestantRes.VerdictSource = "stream_io"
	}
	if err := contestantIn.Err(); err != nil && interactorRes.Status == "OK" {
		interactorRes.Status = model.RunStatusRE
		interactorRes.Reason = "interactive relay to contestant failed: " + err.Error()
		interactorRes.VerdictSource = "stream_io"
	}

	sidecarOutputs, sidecarErrors := captureSidecarOutputs(contestantWS, req.SidecarOutputs)
	resp := interactiveResponse(
		req,
		interactorReq,
		contestantRes,
		interactorRes,
		timing.SinceMillis(startWall),
		contestantDeadlineHit.Load() || ctx.Err() != nil,
		contestantCanceledByInteractor.Load(),
	)
	resp.SidecarOutputs = sidecarOutputs
	resp.SidecarErrors = sidecarErrors
	var participantFileOutput []byte
	var participantFileErr error
	if len(req.FileOutputs) > 0 {
		participantFileOutput, participantFileErr = captureFileOutput(contestantWS, req.FileOutputs[0])
	}
	var interactorOutputBytes int64
	var interactorOutputErr error
	if interactorOutputMaxBytes > 0 {
		interactorOutputBytes, interactorOutputErr = captureInteractorOutput(outputPath, interactorOutputDest, interactorOutputMaxBytes)
	}

	emitCapturedLog(hooks, "stdout", contestantRes.Stdout, responseStdoutLimitBytes(req))
	emitCapturedLog(
		hooks,
		"stderr",
		firstNonEmptyBytes(interactorRes.Stderr, contestantRes.Stderr),
		responseStderrLimitBytes(req),
	)

	var participantStdout []byte
	if captureParticipantStdout {
		participantStdout = append([]byte(nil), contestantRes.Stdout...)
	}
	return interactiveRunResult{
		response:              resp,
		participantStdout:     participantStdout,
		participantFileOutput: participantFileOutput,
		participantFileErr:    participantFileErr,
		interactorOutputBytes: interactorOutputBytes,
		interactorOutputErr:   interactorOutputErr,
	}
}

func captureInteractorOutput(path string, dest io.Writer, maxBytes int64) (int64, error) {
	if dest == nil {
		return 0, fmt.Errorf("interactor_output destination is unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, fmt.Errorf("required interactor_output was not produced")
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("interactor_output is not a regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return 0, fmt.Errorf("interactor_output must not be a hard link")
	}
	if info.Size() > maxBytes {
		return 0, fmt.Errorf("interactor_output exceeded max_bytes")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("interactor_output open failed")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return 0, fmt.Errorf("interactor_output changed during capture")
	}
	written, err := io.Copy(dest, io.LimitReader(file, maxBytes+1))
	if err != nil {
		return 0, fmt.Errorf("interactor_output copy failed")
	}
	if written > maxBytes {
		return 0, fmt.Errorf("interactor_output exceeded max_bytes")
	}
	return written, nil
}

func validateInteractivePeerRuntimeIsolation(contestantLang, interactorLang string) error {
	dotnetPeers := 0
	for _, lang := range []string{contestantLang, interactorLang} {
		switch profiles.NormalizeRunLang(lang) {
		case "csharp", "fsharp", "vbnet":
			dotnetPeers++
		}
	}
	if dotnetPeers > 1 {
		return fmt.Errorf("concurrent .NET contestant and interactor runtimes are unsupported: CoreCLR shared state cannot cross the isolated peer identities")
	}
	return nil
}

func prepareInteractiveCommand(req *model.RunRequest, tuning config.RuntimeTuningConfig, label string) (string, Workspace, []string, *model.RunResponse) {
	workDir, err := createRunWorkDir()
	if err != nil {
		slog.Warn("interactive execute work directory creation failed", "label", label, "err", err)
		resp := model.RunResponse{Status: model.RunStatusInitFail, Reason: label + " work directory creation failed"}
		return "", Workspace{}, nil, &resp
	}

	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		_ = os.RemoveAll(workDir)
		slog.Warn("interactive execute workspace preparation failed", "label", label, "err", err)
		resp := model.RunResponse{Status: model.RunStatusInitFail, Reason: label + " workspace preparation failed"}
		return "", Workspace{}, nil, &resp
	}

	primaryPath, runLang, err := materializeFiles(ws, req)
	if err != nil {
		_ = os.RemoveAll(workDir)
		slog.Warn("interactive execute file materialization failed", "label", label, "err", err)
		resp := model.RunResponse{Status: model.RunStatusInitFail, Reason: label + " file materialization failed"}
		return "", Workspace{}, nil, &resp
	}

	cmdArgs := buildCommandWithRuntimeTuning(primaryPath, runLang, req, tuning)
	if len(cmdArgs) == 0 {
		_ = os.RemoveAll(workDir)
		resp := model.RunResponse{Status: model.RunStatusInitFail, Reason: label + " command is empty"}
		return "", Workspace{}, nil, &resp
	}
	return workDir, ws, cmdArgs, nil
}

func interactorRunRequest(req *model.RunRequest) *model.RunRequest {
	limits := req.Limits
	if limits.TimeMs <= 0 {
		limits.TimeMs = defaultSPJTimeMs
	}
	if limits.MemoryMB <= 0 {
		limits.MemoryMB = defaultSPJMemoryMB
	}
	if limits.WorkspaceBytes <= 0 {
		limits.WorkspaceBytes = defaultWorkspaceBytes
	}
	if req.Interactor.Limits != nil {
		limits = *req.Interactor.Limits
		if limits.TimeMs <= 0 {
			limits.TimeMs = req.Limits.TimeMs
			if limits.TimeMs <= 0 {
				limits.TimeMs = defaultSPJTimeMs
			}
		}
		if limits.MemoryMB <= 0 {
			limits.MemoryMB = defaultSPJMemoryMB
		}
		if limits.OutputBytes <= 0 {
			limits.OutputBytes = req.Limits.OutputBytes
		}
		if limits.WorkspaceBytes <= 0 {
			limits.WorkspaceBytes = defaultWorkspaceBytes
		}
	}
	return &model.RunRequest{
		Lang:              req.Interactor.Lang,
		Binaries:          req.Interactor.Binaries,
		Limits:            limits,
		CaptureLimits:     req.CaptureLimits,
		RuntimeProfile:    req.RuntimeProfile,
		PythonLibraryMode: req.PythonLibraryMode,
		EntryPoint:        req.Interactor.EntryPoint,
	}
}

func interactiveResponse(req, interactorReq *model.RunRequest, contestantRes, interactorRes execResult, wallMs int64, deadlineExceeded, contestantCanceledByInteractor bool) model.RunResponse {
	status := model.RunStatusRE
	reason := ""
	source := "interactive"
	scoreVal := 0.0
	stderrResponseLimit := responseStderrLimitBytes(req)

	contestantStatus, contestantReason, contestantSource := classifyRunStatusWithoutOutput(req, contestantRes)
	if contestantStatus == "OK" {
		contestantStatus = model.RunStatusAccepted
	}
	interactorStatus, interactorReason, interactorSource := classifyInteractorStatus(interactorReq, interactorRes)
	ignoreContestantStatus := contestantCanceledByInteractor && (contestantRes.Status == model.RunStatusTLE && contestantRes.VerdictSource == "wall_time" ||
		contestantRes.Status == model.RunStatusInitFail && strings.Contains(strings.ToLower(contestantRes.Reason), "context canceled"))

	switch {
	case contestantRes.Status == model.RunStatusInitFail && !ignoreContestantStatus:
		status = model.RunStatusInitFail
		reason = firstNonEmptyString(contestantRes.Reason, "contestant initialization failed")
		source = prefixInteractiveSource("contestant", contestantRes.VerdictSource)
	case interactorRes.Status == model.RunStatusInitFail:
		status = model.RunStatusInitFail
		reason = firstNonEmptyString(interactorRes.Reason, "interactor initialization failed")
		source = prefixInteractiveSource("interactor", interactorRes.VerdictSource)
	case deadlineExceeded:
		status = model.RunStatusTLE
		reason = "wall time limit exceeded"
		source = "interactive:wall_time"
	case !ignoreContestantStatus && contestantStatus != model.RunStatusAccepted:
		status = contestantStatus
		reason = firstNonEmptyString(contestantReason, contestantRes.Reason)
		source = prefixInteractiveSource("contestant", contestantSource)
	case interactorStatus == model.RunStatusAccepted:
		status = model.RunStatusAccepted
		source = "interactor"
		scoreVal = 1
	case interactorStatus == model.RunStatusRE:
		status = model.RunStatusRE
		reason = firstNonEmptyString(interactorReason, interactiveFailureMessage(contestantRes, interactorRes, stderrResponseLimit), "interactor failed")
		source = prefixInteractiveSource("interactor", interactorSource)
	case interactorStatus != model.RunStatusAccepted:
		status = interactorStatus
		reason = firstNonEmptyString(interactorReason, interactiveFailureMessage(contestantRes, interactorRes, stderrResponseLimit))
		source = prefixInteractiveSource("interactor", interactorSource)
	default:
		status = model.RunStatusRE
		reason = "interactive execution failed"
		source = "interactive"
	}

	if status == model.RunStatusWA && strings.TrimSpace(reason) == "" {
		reason = "wrong answer"
	}
	if status == model.RunStatusRE && strings.TrimSpace(reason) == "" {
		reason = interactiveFailureMessage(contestantRes, interactorRes, stderrResponseLimit)
		if strings.TrimSpace(reason) == "" {
			reason = "interactive execution failed"
		}
	}

	stdout := ""
	stderr := ""
	stdoutResponseTruncated := false
	stderrResponseTruncated := false
	if status != model.RunStatusAccepted {
		stdout, stdoutResponseTruncated = capturedOutputValue(
			contestantRes.Stdout,
			responseStdoutLimitBytes(req),
		)
		stderr, stderrResponseTruncated = capturedOutputValue(
			firstNonEmptyBytes(interactorRes.Stderr, contestantRes.Stderr),
			responseStderrLimitBytes(req),
		)
	}
	score := &scoreVal
	return model.RunResponse{
		Status:           status,
		TimeMs:           wallMs,
		WallTimeMs:       wallMs,
		CPUTimeMs:        contestantRes.CPUTimeMs + interactorRes.CPUTimeMs,
		RawCPUTimeMs:     sumRawCPUTime(contestantRes.RawCPUTimeMs, interactorRes.RawCPUTimeMs),
		ProcessCPUTimeMs: contestantRes.ProcessCPUTimeMs + interactorRes.ProcessCPUTimeMs,
		MemoryKB:         max(contestantRes.MemoryKB, interactorRes.MemoryKB),
		ExitCode:         contestantRes.ExitCode,
		Stdout:           stdout,
		Stderr:           stderr,
		StdoutTruncated:  contestantRes.StdoutTruncated || stdoutResponseTruncated,
		StderrTruncated:  contestantRes.StderrTruncated || interactorRes.StderrTruncated || stderrResponseTruncated,
		Reason:           reason,
		VerdictSource:    source,
		Score:            score,
		Steps: []model.StepResult{
			interactiveContestantStepResult(req, contestantRes),
			interactiveInteractorStepResult(interactorReq, interactorRes),
		},
	}
}

func classifyInteractorStatus(req *model.RunRequest, res execResult) (string, string, string) {
	status := res.Status
	reason := res.Reason
	source := res.VerdictSource
	status, reason, source = applyFinalCPUTimeStatus(status, reason, source, res.CPUTimeMs, req.Limits.TimeMs, strings.HasPrefix(source, "cpu_time_cgroup"))
	if status != "OK" {
		if status == model.RunStatusTLE || status == model.RunStatusMLE || status == model.RunStatusWLE {
			return model.RunStatusRE, firstNonEmptyString(reason, "interactor failed: "+status), source
		}
		return status, reason, source
	}
	if res.ExitCode == nil {
		return model.RunStatusRE, "interactor did not report an exit code", "interactor"
	}
	switch *res.ExitCode {
	case 0:
		return model.RunStatusAccepted, "", "exit_code"
	case 3:
		return model.RunStatusRE, firstNonEmptyString(clipUTF8(res.Stderr, responseStderrLimitBytes(req)), "interactor failed"), "exit_code"
	default:
		return model.RunStatusWA, clipUTF8(res.Stderr, responseStderrLimitBytes(req)), "exit_code"
	}
}

func interactiveContestantStepResult(req *model.RunRequest, res execResult) model.StepResult {
	status, reason, source := classifyRunStatusWithoutOutput(req, res)
	if status == "OK" {
		status = model.RunStatusAccepted
	}
	stdout, stdoutResponseTruncated := capturedOutputValue(
		res.Stdout,
		responseStdoutLimitBytes(req),
	)
	stderr, stderrResponseTruncated := capturedOutputValue(
		res.Stderr,
		responseStderrLimitBytes(req),
	)
	return model.StepResult{
		ID:               "contestant",
		ProgramID:        "contestant",
		Status:           status,
		TimeMs:           res.WallTimeMs,
		WallTimeMs:       res.WallTimeMs,
		CPUTimeMs:        res.CPUTimeMs,
		RawCPUTimeMs:     res.RawCPUTimeMs,
		ProcessCPUTimeMs: res.ProcessCPUTimeMs,
		MemoryKB:         res.MemoryKB,
		ExitCode:         res.ExitCode,
		Stdout:           stdout,
		Stderr:           stderr,
		StdoutTruncated:  res.StdoutTruncated || stdoutResponseTruncated,
		StderrTruncated:  res.StderrTruncated || stderrResponseTruncated,
		Reason:           firstNonEmptyString(reason, res.Reason),
		VerdictSource:    prefixInteractiveSource("contestant", source),
	}
}

func interactiveInteractorStepResult(req *model.RunRequest, res execResult) model.StepResult {
	status, reason, source := classifyInteractorStatus(req, res)
	stderr, stderrResponseTruncated := capturedOutputValue(
		res.Stderr,
		responseStderrLimitBytes(req),
	)
	return model.StepResult{
		ID:               "interactor",
		ProgramID:        "interactor",
		Status:           status,
		TimeMs:           res.WallTimeMs,
		WallTimeMs:       res.WallTimeMs,
		CPUTimeMs:        res.CPUTimeMs,
		RawCPUTimeMs:     res.RawCPUTimeMs,
		ProcessCPUTimeMs: res.ProcessCPUTimeMs,
		MemoryKB:         res.MemoryKB,
		ExitCode:         res.ExitCode,
		Stderr:           stderr,
		StdoutTruncated:  res.StdoutTruncated,
		StderrTruncated:  res.StderrTruncated || stderrResponseTruncated,
		Reason:           firstNonEmptyString(reason, res.Reason),
		VerdictSource:    prefixInteractiveSource("interactor", source),
	}
}

func interactiveFailureMessage(contestantRes, interactorRes execResult, outputLimit int) string {
	return firstNonEmptyString(
		clipUTF8(interactorRes.Stderr, outputLimit),
		clipUTF8(contestantRes.Stderr, outputLimit),
		interactorRes.Reason,
		contestantRes.Reason,
	)
}

func prefixInteractiveSource(prefix, source string) string {
	if strings.TrimSpace(source) == "" {
		return prefix
	}
	return prefix + ":" + source
}

func firstNonEmptyBytes(values ...[]byte) []byte {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
