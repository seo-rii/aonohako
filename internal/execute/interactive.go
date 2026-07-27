package execute

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/timing"
)

const interactiveAcceptedGrace = 100 * time.Millisecond

type forgivingPipeWriter struct {
	w      *io.PipeWriter
	closed atomic.Bool
}

func (w *forgivingPipeWriter) Write(p []byte) (int, error) {
	if w == nil || w.w == nil || w.closed.Load() {
		return len(p), nil
	}
	if _, err := w.w.Write(p); err != nil {
		return len(p), nil
	}
	return len(p), nil
}

func (w *forgivingPipeWriter) Close() {
	if w == nil || w.w == nil {
		return
	}
	if w.closed.CompareAndSwap(false, true) {
		_ = w.w.Close()
	}
}

func (s *Service) runInteractive(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig) model.RunResponse {
	startWall := timing.MonotonicNow()
	if req.Interactor == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "interactor is required"}
	}
	if reason := s.networkRequestRejection(req.EnableNetwork); reason != "" {
		return model.RunResponse{
			Status: model.RunStatusInitFail,
			Reason: reason,
		}
	}
	if len(req.SidecarOutputs) > maxSidecarOutputSpecs {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("too many sidecar outputs: max %d", maxSidecarOutputSpecs)}
	}
	if len(req.Binaries) > maxBinaryFiles {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("too many binaries: max %d", maxBinaryFiles)}
	}
	if len(req.Binaries) == 0 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "no binaries"}
	}

	contestantWorkDir, contestantWS, contestantArgs, contestantInit := prepareInteractiveCommand(req, tuning, "contestant")
	if contestantInit != nil {
		return *contestantInit
	}
	defer os.RemoveAll(contestantWorkDir)

	interactorReq := interactorRunRequest(req)
	interactorWorkDir, interactorWS, interactorArgs, interactorInit := prepareInteractiveCommand(interactorReq, tuning, "interactor")
	if interactorInit != nil {
		return *interactorInit
	}
	defer os.RemoveAll(interactorWorkDir)
	if err := validateInteractivePeerRuntimeIsolation(req.Lang, interactorReq.Lang); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}

	inputPath, err := writeStdinTempFile(ctx, filepath.Join(interactorWS.RootDir, ".tmp"), "interactive-input-*", req, "")
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "interactive input materialization failed: " + err.Error()}
	}
	answerPath, err := writeTempFile(filepath.Join(interactorWS.RootDir, ".tmp"), "interactive-answer-*", req.ExpectedStdout)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "interactive answer materialization failed: " + err.Error()}
	}
	outputPath := filepath.Join(interactorWS.RootDir, ".tmp", "interactive-output")
	interactorArgs = append(interactorArgs, inputPath, outputPath, answerPath)

	contestantInR, contestantInW := io.Pipe()
	interactorInR, interactorInW := io.Pipe()
	contestantIn := &forgivingPipeWriter{w: contestantInW}
	interactorIn := &forgivingPipeWriter{w: interactorInW}
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
	contestantLimitMs := max(1, req.Limits.TimeMs)
	contestantTimer := time.AfterFunc(time.Duration(contestantLimitMs)*time.Millisecond, func() {
		if contestantFinished.Load() {
			return
		}
		contestantDeadlineHit.Store(true)
		cancelContestant()
		cancelInteractor()
	})
	defer contestantTimer.Stop()
	interactorLimitMs := max(1, interactorReq.Limits.TimeMs)
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
				stdin:        contestantInR,
				liveStdin:    true,
				stdout:       interactorIn,
				onStdoutDone: interactorIn.Close,
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
			interactorIn.Close()
			contestantStatus, _, _ := classifyRunStatusWithoutOutput(req, res)
			if contestantStatus != "OK" {
				cancelInteractor()
			}
		case res := <-interactorCh:
			interactorRes = res
			gotInteractor = true
			interactorTimer.Stop()
			contestantIn.Close()
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

	sidecarOutputs, sidecarErrors := captureSidecarOutputs(contestantWS, req.SidecarOutputs)
	resp := interactiveResponse(
		req,
		interactorReq,
		contestantRes,
		interactorRes,
		timing.SinceMillis(startWall),
		contestantDeadlineHit.Load() || ctx.Err() != nil,
		contestantCanceledByInteractor.Load(),
		responseOutputLimitBytes(req),
	)
	resp.SidecarOutputs = sidecarOutputs
	resp.SidecarErrors = sidecarErrors

	if hooks.OnLog != nil {
		if len(contestantRes.Stdout) > 0 {
			hooks.OnLog("stdout", clipUTF8(contestantRes.Stdout, responseOutputLimitBytes(req)))
		}
		if stderr := firstNonEmptyBytes(interactorRes.Stderr, contestantRes.Stderr); len(stderr) > 0 {
			hooks.OnLog("stderr", clipUTF8(stderr, responseOutputLimitBytes(req)))
		}
	}

	return resp
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
		Lang:           req.Interactor.Lang,
		Binaries:       req.Interactor.Binaries,
		Limits:         limits,
		RuntimeProfile: req.RuntimeProfile,
		EntryPoint:     req.Interactor.EntryPoint,
	}
}

func interactiveResponse(req, interactorReq *model.RunRequest, contestantRes, interactorRes execResult, wallMs int64, deadlineExceeded, contestantCanceledByInteractor bool, outputLimit int) model.RunResponse {
	status := model.RunStatusRE
	reason := ""
	source := "interactive"
	scoreVal := 0.0

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
		reason = firstNonEmptyString(interactorReason, interactiveFailureMessage(contestantRes, interactorRes, outputLimit), "interactor failed")
		source = prefixInteractiveSource("interactor", interactorSource)
	case interactorStatus != model.RunStatusAccepted:
		status = interactorStatus
		reason = firstNonEmptyString(interactorReason, interactiveFailureMessage(contestantRes, interactorRes, outputLimit))
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
		reason = interactiveFailureMessage(contestantRes, interactorRes, outputLimit)
		if strings.TrimSpace(reason) == "" {
			reason = "interactive execution failed"
		}
	}

	stdout := ""
	stderr := ""
	if status != model.RunStatusAccepted {
		stdout = clipUTF8(contestantRes.Stdout, outputLimit)
		stderr = clipUTF8(firstNonEmptyBytes(interactorRes.Stderr, contestantRes.Stderr), outputLimit)
	}
	score := &scoreVal
	return model.RunResponse{
		Status:           status,
		TimeMs:           wallMs,
		WallTimeMs:       wallMs,
		CPUTimeMs:        contestantRes.CPUTimeMs + interactorRes.CPUTimeMs,
		ProcessCPUTimeMs: contestantRes.ProcessCPUTimeMs + interactorRes.ProcessCPUTimeMs,
		MemoryKB:         max(contestantRes.MemoryKB, interactorRes.MemoryKB),
		ExitCode:         contestantRes.ExitCode,
		Stdout:           stdout,
		Stderr:           stderr,
		StdoutTruncated:  contestantRes.StdoutTruncated,
		StderrTruncated:  contestantRes.StderrTruncated || interactorRes.StderrTruncated,
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
		return model.RunStatusRE, firstNonEmptyString(clipUTF8(res.Stderr, responseOutputLimitBytes(req)), "interactor failed"), "exit_code"
	default:
		return model.RunStatusWA, clipUTF8(res.Stderr, responseOutputLimitBytes(req)), "exit_code"
	}
}

func interactiveContestantStepResult(req *model.RunRequest, res execResult) model.StepResult {
	status, reason, source := classifyRunStatusWithoutOutput(req, res)
	if status == "OK" {
		status = model.RunStatusAccepted
	}
	return model.StepResult{
		ID:               "contestant",
		ProgramID:        "contestant",
		Status:           status,
		TimeMs:           res.WallTimeMs,
		WallTimeMs:       res.WallTimeMs,
		CPUTimeMs:        res.CPUTimeMs,
		ProcessCPUTimeMs: res.ProcessCPUTimeMs,
		MemoryKB:         res.MemoryKB,
		ExitCode:         res.ExitCode,
		Stdout:           clipUTF8(res.Stdout, responseOutputLimitBytes(req)),
		Stderr:           clipUTF8(res.Stderr, responseOutputLimitBytes(req)),
		StdoutTruncated:  res.StdoutTruncated,
		StderrTruncated:  res.StderrTruncated,
		Reason:           firstNonEmptyString(reason, res.Reason),
		VerdictSource:    prefixInteractiveSource("contestant", source),
	}
}

func interactiveInteractorStepResult(req *model.RunRequest, res execResult) model.StepResult {
	status, reason, source := classifyInteractorStatus(req, res)
	return model.StepResult{
		ID:               "interactor",
		ProgramID:        "interactor",
		Status:           status,
		TimeMs:           res.WallTimeMs,
		WallTimeMs:       res.WallTimeMs,
		CPUTimeMs:        res.CPUTimeMs,
		ProcessCPUTimeMs: res.ProcessCPUTimeMs,
		MemoryKB:         res.MemoryKB,
		ExitCode:         res.ExitCode,
		Stderr:           clipUTF8(res.Stderr, responseOutputLimitBytes(req)),
		StdoutTruncated:  res.StdoutTruncated,
		StderrTruncated:  res.StderrTruncated,
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
