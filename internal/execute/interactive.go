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
	"aonohako/internal/platform"
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
	if req.EnableNetwork && s.deploymentTarget == platform.DeploymentTargetCloudRun {
		return model.RunResponse{
			Status: model.RunStatusInitFail,
			Reason: "embedded helper execution on cloudrun does not support enable_network=true; use a self-hosted remote runner for networked workloads",
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

	inputPath, err := writeStdinTempFile(ctx, filepath.Join(interactorWS.RootDir, ".tmp"), "interactive-input-*", req)
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

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var deadlineHit atomic.Bool
	var parentCanceled atomic.Bool
	limitMs := max(1, req.Limits.TimeMs)
	timer := time.AfterFunc(time.Duration(limitMs)*time.Millisecond, func() {
		deadlineHit.Store(true)
		cancel()
	})
	defer timer.Stop()

	go func() {
		select {
		case <-ctx.Done():
			parentCanceled.Store(true)
			cancel()
		case <-runCtx.Done():
		}
	}()

	contestantCh := make(chan execResult, 1)
	interactorCh := make(chan execResult, 1)
	go func() {
		contestantCh <- executeSandboxCommandWithStreams(
			runCtx,
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
	}()
	go func() {
		interactorCh <- executeSandboxCommandWithStreams(
			runCtx,
			interactorWS,
			interactorArgs,
			interactorReq,
			sandboxStreamConfig{
				stdin:        interactorInR,
				liveStdin:    true,
				stdout:       contestantIn,
				onStdoutDone: contestantIn.Close,
			},
			Hooks{},
			outputLimitBytes(req),
			tuning,
			s.cgroupParentDir,
		)
	}()

	var contestantRes execResult
	var interactorRes execResult
	gotContestant := false
	gotInteractor := false
	ignoreContestantCancelStatus := false
	parentDone := ctx.Done()
	for !gotContestant || !gotInteractor {
		select {
		case res := <-contestantCh:
			contestantRes = res
			gotContestant = true
			interactorIn.Close()
			if res.Status == model.RunStatusInitFail {
				cancel()
			}
		case res := <-interactorCh:
			interactorRes = res
			gotInteractor = true
			contestantIn.Close()
			if interactiveAccepted(res) {
				time.AfterFunc(interactiveAcceptedGrace, cancel)
				continue
			}
			if !gotContestant {
				ignoreContestantCancelStatus = true
			}
			cancel()
		case <-parentDone:
			parentCanceled.Store(true)
			cancel()
			parentDone = nil
		}
	}
	cancel()
	contestantIn.Close()
	interactorIn.Close()

	sidecarOutputs, sidecarErrors := captureSidecarOutputs(contestantWS, req.SidecarOutputs)
	resp := interactiveResponse(
		req,
		interactorReq,
		contestantRes,
		interactorRes,
		timing.SinceMillis(startWall),
		deadlineHit.Load() || parentCanceled.Load(),
		ignoreContestantCancelStatus,
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

func interactiveAccepted(res execResult) bool {
	return res.Status == "OK" && res.ExitCode != nil && *res.ExitCode == 0
}

func interactiveResponse(req, interactorReq *model.RunRequest, contestantRes, interactorRes execResult, wallMs int64, deadlineExceeded, ignoreContestantCancelStatus bool, outputLimit int) model.RunResponse {
	status := model.RunStatusRE
	reason := ""
	source := "interactive"
	scoreVal := 0.0

	contestantStatus, contestantReason, contestantSource := classifyRunStatusWithoutOutput(req, contestantRes)
	if contestantStatus == "OK" {
		contestantStatus = model.RunStatusAccepted
	}
	interactorStatus, interactorReason, interactorSource := classifyInteractorStatus(interactorReq, interactorRes)

	switch {
	case contestantRes.Status == model.RunStatusInitFail:
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
	case interactiveAccepted(interactorRes):
		status = model.RunStatusAccepted
		source = "interactor"
		scoreVal = 1
	case !ignoreContestantCancelStatus && contestantStatus != model.RunStatusAccepted:
		status = contestantStatus
		reason = firstNonEmptyString(contestantReason, contestantRes.Reason)
		source = prefixInteractiveSource("contestant", contestantSource)
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
		Status:          status,
		TimeMs:          wallMs,
		WallTimeMs:      wallMs,
		CPUTimeMs:       contestantRes.CPUTimeMs + interactorRes.CPUTimeMs,
		MemoryKB:        max(contestantRes.MemoryKB, interactorRes.MemoryKB),
		ExitCode:        contestantRes.ExitCode,
		Stdout:          stdout,
		Stderr:          stderr,
		StdoutTruncated: contestantRes.StdoutTruncated,
		StderrTruncated: contestantRes.StderrTruncated || interactorRes.StderrTruncated,
		Reason:          reason,
		VerdictSource:   source,
		Score:           score,
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
		ID:              "contestant",
		ProgramID:       "contestant",
		Status:          status,
		TimeMs:          res.WallTimeMs,
		WallTimeMs:      res.WallTimeMs,
		CPUTimeMs:       res.CPUTimeMs,
		MemoryKB:        res.MemoryKB,
		ExitCode:        res.ExitCode,
		Stdout:          clipUTF8(res.Stdout, responseOutputLimitBytes(req)),
		Stderr:          clipUTF8(res.Stderr, responseOutputLimitBytes(req)),
		StdoutTruncated: res.StdoutTruncated,
		StderrTruncated: res.StderrTruncated,
		Reason:          firstNonEmptyString(reason, res.Reason),
		VerdictSource:   prefixInteractiveSource("contestant", source),
	}
}

func interactiveInteractorStepResult(req *model.RunRequest, res execResult) model.StepResult {
	status, reason, source := classifyInteractorStatus(req, res)
	return model.StepResult{
		ID:              "interactor",
		ProgramID:       "interactor",
		Status:          status,
		TimeMs:          res.WallTimeMs,
		WallTimeMs:      res.WallTimeMs,
		CPUTimeMs:       res.CPUTimeMs,
		MemoryKB:        res.MemoryKB,
		ExitCode:        res.ExitCode,
		Stderr:          clipUTF8(res.Stderr, responseOutputLimitBytes(req)),
		StdoutTruncated: res.StdoutTruncated,
		StderrTruncated: res.StderrTruncated,
		Reason:          firstNonEmptyString(reason, res.Reason),
		VerdictSource:   prefixInteractiveSource("interactor", source),
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
