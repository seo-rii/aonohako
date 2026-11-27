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
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/runtimeidentity"
	"aonohako/internal/runvalidation"
	"aonohako/internal/timing"
)

const (
	DefaultOutputBytes           = 64 << 10
	DefaultWorkspaceBytes        = 128 << 20
	MaxCapturedFileBytes         = runvalidation.MaxPipelineParticipantFileBytes
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
	shellSandboxThreadLimit      = 64
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

func sumRawCPUTime(values ...*int64) *int64 {
	var total int64
	found := false
	for _, value := range values {
		if value == nil {
			continue
		}
		total += *value
		found = true
	}
	if !found {
		return nil
	}
	return &total
}

type Service struct {
	workMeasurement              durante.Config
	workMeasurementPolicyError   error
	competitiveRuntimeIdentity   runtimeidentity.CompetitiveIdentity
	deploymentTarget             platform.DeploymentTarget
	cpuNormalizer                timing.CPUNormalizer
	shortCaseSampling            bool
	runtimeTuning                config.RuntimeTuningConfig
	runtimeTuningProfiles        map[string]config.RuntimeTuningConfig
	cgroupParentDir              string
	communicationEnabled         bool
	communicationMaxParticipants int
	communicationMemoryBudgetMB  int
	communicationCPUCount        int
	communicationWallBudgetMs    int
	maxActiveRuns                int
	workRootMaxBytes             int
	networkEgressIsolated        bool
	stdinURLTimeout              time.Duration
}

type sandboxRunResult struct {
	workClockProgress   *durante.WorkClockProgress
	response            model.RunResponse
	judgeOut            []byte
	stdoutOut           []byte
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
	competitiveIdentity, _ := runtimeidentity.Competitive(cfg.CompetitiveRuntimeFingerprint)
	profiles := make(map[string]config.RuntimeTuningConfig, len(cfg.Execution.RuntimeTuningProfiles))
	for name, tuning := range cfg.Execution.RuntimeTuningProfiles {
		profiles[name] = tuning.WithSafeDefaults()
	}
	communicationMaxParticipants := cfg.CommunicationMaxParticipants
	if communicationMaxParticipants <= 0 {
		communicationMaxParticipants = runvalidation.MaxCommunicationParticipants
	}
	measurement := snapshotWorkMeasurementConfig(cfg.Execution.WorkMeasurement)
	var measurementPolicyError error
	if measurement.ResourcePolicy != nil {
		measurementPolicyError = measurement.ResourcePolicy.ValidateAdmission(cfg.MaxActiveRuns, cfg.WorkRootMaxBytes)
	}
	return &Service{
		deploymentTarget:             cfg.Execution.Platform.DeploymentTarget,
		workMeasurement:              measurement,
		workMeasurementPolicyError:   measurementPolicyError,
		competitiveRuntimeIdentity:   competitiveIdentity,
		shortCaseSampling:            cfg.Execution.CPUNormalization.ShortCaseSampling,
		runtimeTuning:                cfg.Execution.RuntimeTuning.WithSafeDefaults(),
		runtimeTuningProfiles:        profiles,
		cgroupParentDir:              cfg.Execution.Cgroup.ParentDir,
		communicationEnabled:         cfg.CommunicationEnabled,
		communicationMaxParticipants: communicationMaxParticipants,
		communicationMemoryBudgetMB:  cfg.CommunicationMemoryBudgetMB,
		communicationCPUCount:        cfg.CommunicationCPUCount,
		communicationWallBudgetMs:    cfg.CommunicationWallBudgetMs,
		maxActiveRuns:                cfg.MaxActiveRuns,
		workRootMaxBytes:             cfg.WorkRootMaxBytes,
		networkEgressIsolated:        cfg.NetworkEgressIsolated,
		stdinURLTimeout:              stdinURLDownloadTimeout,
	}
}

func (s *Service) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}
	req = automaticJudgeRequest(req, s.workMeasurement, s.runtimeTuning, s.runtimeTuningProfiles)
	if err := validateJudgeCaseMode(req, s.workMeasurement); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if req.WorkMeasurementShadow {
		return s.runWithShadow(ctx, req, hooks)
	}
	if req.Pipeline == nil && !runvalidation.UsesSteps(req) && automaticObservationRequested(req, s.workMeasurement) {
		return s.runWithObservation(ctx, req, hooks, s.runRequest)
	}
	return s.runRequest(ctx, req, hooks)
}

func (s *Service) runRequest(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if err := runvalidation.ValidateDisallowedCPUFeatures(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	tuning, err := resolveRuntimeTuning(req, s.runtimeTuning, s.runtimeTuningProfiles)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if !isShadowJudge(ctx) {
		req, err = resolveJudgeWorkLimits(req, s.workMeasurement, tuning)
		if err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}
	if err := runvalidation.ValidateWorkMeasurementLimits(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := validateJudgeClockBinding(req, s.workMeasurement, tuning); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if req.WorkMeasurementTime {
		if _, _, err := ordinaryTimePolicy(req, s.workMeasurement, tuning); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}
	peerPlans, err := workMeasurementPeerTimePlans(req, s.workMeasurement, tuning)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if req.WorkMeasurementPeerTime != nil && req.WorkMeasurementPeerTime.Version == 2 {
		// The per-role safety envelopes are raw; all coordinators and their
		// response metadata must use that same CPU accounting.
		rawService := *s
		rawService.cpuNormalizer = timing.CPUNormalizer{}
		s = &rawService
		memoryMB := peakPeerMemoryMB(req, peerPlans)
		if err := s.workMeasurement.ResourcePolicy.ValidatePeerAdmission(s.maxActiveRuns, s.workRootMaxBytes, memoryMB); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}

	if req.WorkMeasurementLimits != nil {
		if s.workMeasurementPolicyError != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: s.workMeasurementPolicyError.Error()}
		}
		if err := s.workMeasurement.ResourcePolicy.ValidateRequest(*req.WorkMeasurementLimits); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}
	if req.WorkMeasurement {
		if err := validateWorkMeasurementRequest(req, s.workMeasurement); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}
	if err := validateJudgeRequest(ctx, req, s.workMeasurement); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := runvalidation.ValidateTextEncoding(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := runvalidation.ValidateSidecarOutputs(req.SidecarOutputs); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := runvalidation.ValidateEntry(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if req.EntryMode == "state" {
		// Hidden scenario-derived state must never reach response or SSE logs.
		hooks = Hooks{}
	}
	if _, err := runvalidation.ValidateBinaryBudget(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := pythonpolicy.ValidateOptionalLibraryMode(req.PythonLibraryMode); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid python_library_mode: " + err.Error()}
	}
	if req.PythonLibraryMode != "" && !runvalidation.UsesPython(req) {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "python_library_mode requires a Python or PyPy execution target"}
	}
	if req.Pipeline != nil {
		if err := runvalidation.ValidatePipelineV1(req); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}
	if req.SPJ != nil {
		if err := runvalidation.ValidateSPJ(req.SPJ); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}
	var stages *stagedWorkClocks
	if stagedPeerClocksRequested(req) {
		stages = newStagedWorkClocks(peerPlans, s.workMeasurement)
	}
	var response model.RunResponse
	switch {
	case req.WorkMeasurementLimits != nil:
		return s.runWorkMeasurementDiagnostic(ctx, req, hooks, tuning)
	case stages == nil && spjPeerClocksRequested(req):
		response = s.runSPJClockJudgment(ctx, req, hooks, tuning, peerPlans)
	case req.Communication != nil:
		response = s.runCommunication(ctx, req, hooks, tuning, peerPlans)
	case req.Pipeline != nil:
		response = s.runPipelineV1(ctx, req, hooks, tuning, stages)
	case req.Interactor != nil:
		response = s.runInteractive(ctx, req, hooks, tuning, peerPlans)
	case runvalidation.UsesSteps(req):
		response = s.runStepPipeline(ctx, req, hooks, tuning, stages)
	default:
		if s.shortCaseSampling && s.cpuNormalizer.Enabled() && !req.WorkMeasurement {
			response = runShortCaseBatch(ctx, req, hooks, func(ctx context.Context, req *model.RunRequest, stdin io.Reader, maxBytes int64, hooks Hooks) model.RunResponse {
				return s.runOneWithStdin(ctx, req, stdin, maxBytes, hooks, tuning, true, false).response
			})
		} else {
			response = s.runOne(ctx, req, hooks, tuning, true).response
		}
	}
	if stages != nil {
		if stages.err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: stages.err.Error()}
		}
		response.WorkMeasurements = stages.rows
	}
	response = finishWorkMeasurementPeerTime(req, response, s.workMeasurement, tuning)
	if stages != nil && req.Pipeline != nil {
		redactStagedClockRows(response.WorkMeasurements)
	}
	return s.decorateCPUTimeNormalization(response)
}

func (s *Service) decorateCPUTimeNormalization(response model.RunResponse) model.RunResponse {
	if info, ok := s.cpuNormalizer.Info(); ok {
		response.CPUTimeNormalization = &model.CPUTimeNormalization{
			Method:          info.Method,
			ScalePPM:        info.ScalePPM,
			ReferenceTimeNs: info.ReferenceTimeNs,
			ObservedTimeNs:  info.ObservedTimeNs,
		}
	}
	return response
}

func (s *Service) runOne(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig, evaluateOutput bool) sandboxRunResult {
	return s.runOneWithStdin(ctx, req, nil, 0, hooks, tuning, evaluateOutput, false)
}

func (s *Service) runOneWithStdin(ctx context.Context, req *model.RunRequest, stdin io.Reader, stdinMaxBytes int64, hooks Hooks, tuning config.RuntimeTuningConfig, evaluateOutput, captureRawStdout bool) sandboxRunResult {
	return s.runProgram(ctx, req, stdin, stdinMaxBytes, hooks, tuning, evaluateOutput, captureRawStdout, nil)
}

func (s *Service) runProgram(ctx context.Context, req *model.RunRequest, stdin io.Reader, stdinMaxBytes int64, hooks Hooks, tuning config.RuntimeTuningConfig, evaluateOutput, captureRawStdout bool, clocks *spjWorkClocks) sandboxRunResult {
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
		var stdinURLReader io.ReadCloser
		if snapshot, ok := ctx.Value(shadowStdinKey{}).(*stdinSnapshot); ok {
			stdinURLReader, err = snapshot.open(ctx, req.StdinURL, urlMaxBytes)
		} else {
			stdinURLReader, err = openStdinURL(ctx, req.StdinURL, urlMaxBytes, nil)
		}
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
		// chmod does not revoke write access from an already-open descriptor.
		// Pass only a read-only descriptor to the contestant so that the same
		// trusted input can subsequently be handed to the checker.
		if err := judgeInput.Close(); err != nil {
			slog.Warn("execute judge input close failed", "err", err)
			return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin materialization failed"}}
		}
		readonlyInput, err := os.Open(judgeInput.Name())
		if err != nil {
			slog.Warn("execute judge input reopen failed", "err", err)
			return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin materialization failed"}}
		}
		defer readonlyInput.Close()
		judgeInputPath = judgeInput.Name()
		stdin = &sandboxPreparedStdin{file: readonlyInput}
	}

	meter, err := s.prepareContestantDurante(ctx, ws, req)
	if err != nil {
		return sandboxRunResult{response: model.RunResponse{Status: model.RunStatusInitFail, Reason: "durante preparation failed: " + err.Error()}}
	}
	res := runCommandWithSandbox(ctx, ws, cmdArgs, req, stdin, stdinMaxBytes, hooks, capturedOutputLimit, tuning, s.cgroupParentDir, s.cpuNormalizer, meter)
	if clocks != nil {
		finishPeerWorkClock(req, meter, &res, peerWorkClockCause(ctx, ctx), "contestant")
		clocks.contestant = collectPeerWorkMeasurement(meter, res, false, "contestant", "contestant", nil)
	}
	if res.Status == model.RunStatusInitFail {
		wallMs := timing.SinceMillis(startWall)
		if clocks != nil {
			wallMs = res.WallTimeMs
		}
		return sandboxRunResult{response: initializationFailureResponse(res, wallMs)}
	}

	rawOut := res.Stdout
	judgeOut := rawOut
	judgeSource := "stdout"
	fullErr := res.Stderr

	if len(req.FileOutputs) > 0 {
		captured, err := captureFileOutput(ws, req.FileOutputs[0])
		if err != nil {
			judgeOut = nil
			if status, _, _ := classifyRunStatusWithoutOutput(req, res); status == "OK" {
				res.Status = model.RunStatusRE
				res.Reason = "file output capture failed: " + err.Error()
				res.VerdictSource = "file_output"
			}
		} else {
			judgeOut = captured
			judgeSource = "file_output"
		}
	}

	if capture, ok := ctx.Value(observationOutputKey{}).(*observationOutput); ok {
		capture.record(judgeOut)
	}
	sidecarOutputs, sidecarErrors := captureSidecarOutputs(ws, req.SidecarOutputs)
	status, evalReason, verdictSource := classifyRunStatusWithoutOutput(req, res)
	var score *float64
	if evaluateOutput {
		status, score, evalReason, verdictSource = evaluateRunStatusWithClocks(ctx, ws, req, res, judgeOut, judgeSource, judgeInputPath, sidecarOutputs, tuning, s.cgroupParentDir, s.cpuNormalizer, clocks)
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
	if req.EntryMode == "state" {
		outResp, errResp = "", ""
		stdoutResponseTruncated, stderrResponseTruncated = false, false
	}

	emitCapturedLog(hooks, "stdout", rawOut, stdoutResponseLimit)
	emitCapturedLog(hooks, "stderr", fullErr, stderrResponseLimit)

	var stdoutOut []byte
	if captureRawStdout {
		stdoutOut = append([]byte(nil), rawOut...)
	}
	return sandboxRunResult{
		response: model.RunResponse{
			Status:           status,
			TimeMs:           res.WallTimeMs,
			WallTimeMs:       res.WallTimeMs,
			CPUTimeMs:        res.CPUTimeMs,
			WorkMeasurement:  res.WorkMeasurement,
			CPUTimeNs:        res.CPUTimeNs,
			RawCPUTimeMs:     res.RawCPUTimeMs,
			RawCPUTimeNs:     res.RawCPUTimeNs,
			CPUAccounting:    res.CPUAccounting,
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
		workClockProgress:   res.workClockProgress,
		judgeOut:            append([]byte(nil), judgeOut...),
		stdoutOut:           stdoutOut,
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

func (s *Service) runStepPipeline(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig, clocks *stagedWorkClocks) (response model.RunResponse) {
	if err := runvalidation.ValidateStepPipeline(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	handoffDir, err := createRunWorkDir()
	if err != nil {
		slog.Warn("execute step handoff directory creation failed", "err", err)
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "handoff directory creation failed"}
	}
	defer os.RemoveAll(handoffDir)
	var replays map[string]legacyStepReplay
	if automaticObservationRequested(req, s.workMeasurement) {
		budget := stepObservationBudgetFromContext(ctx)
		if budget.allowsReplay() {
			replays = make(map[string]legacyStepReplay, len(req.Steps))
		}
		defer func() {
			replayCtx, cancel := budget.start(ctx)
			defer cancel()
			response.WorkMeasurementObservation = s.observeLegacySteps(replayCtx, response, replays, tuning)
		}()
	}

	handoffs := map[string]string{}
	stepResults := make([]model.StepResult, 0, len(req.Steps))
	stdinURLTimeout := s.stdinURLTimeout
	if stdinURLTimeout <= 0 {
		stdinURLTimeout = stdinURLDownloadTimeout
	}
	stdinURLBudget := &stdinURLDownloadBudget{remaining: stdinURLTimeout}
	for i, step := range req.Steps {
		_, stdinReader, closeStdin, stdinVerdictSource, stdinMaxBytes, err := prepareStepStdin(ctx, step, handoffs, handoffDir, stdinURLBudget)
		if err != nil {
			slog.Warn("execute step stdin preparation failed", "step", step.ID, "err", err)
			return aggregateStepResponse(model.RunResponse{
				Status:        model.RunStatusInitFail,
				Reason:        fmt.Sprintf("step %s %s", step.ID, err.Error()),
				VerdictSource: "step:" + step.ID + ":" + stdinVerdictSource,
			}, stepResults)
		}
		stepReq := legacyStepRequest(req, i)
		finalStep := i == len(req.Steps)-1
		observe := replays != nil && s.workMeasurementPolicyError == nil && observationEligible(stepReq, s.workMeasurement) == ""
		var replay legacyStepReplay
		if observe {
			replay, stdinReader = prepareLegacyReplay(stepReq, stdinReader, stdinMaxBytes)
		}
		captureStdout := observe && len(stepReq.FileOutputs) > 0
		run := s.runClockedStage(ctx, stepReq, stdinReader, stdinMaxBytes, hooks, tuning, finalStep, captureStdout, clocks, step.ID, step.ProgramID)
		if closeStdin != nil {
			_ = closeStdin()
		}
		if !finalStep {
			run.response.Status, run.response.Reason, run.response.VerdictSource = applyStdoutLimitVerdict(
				run.response.Status, run.response.Reason, run.response.VerdictSource, run.stdoutLimitExceeded,
			)
		}
		stepResult := stepResultFromResponse(step.ID, step.ProgramID, run.response)
		if !finalStep && (run.response.Status == "OK" || run.response.VerdictSource == "stdout_limit") {
			stepResult.HandoffBytes = int64(len(run.judgeOut))
		}
		stepResults = append(stepResults, stepResult)
		if observe && !run.stdoutLimitExceeded && legacyNativeStepOutput(stepReq, stepResult, finalStep) {
			if replay.url != nil {
				replay.url.source = nil // The native descriptor has closed.
			}
			stdout, file := run.judgeOut, []byte(nil)
			if captureStdout {
				stdout, file = run.stdoutOut, run.judgeOut
			}
			replay.outputs = pipelineOutputDigests(stdout, file)
			replays[step.ID] = replay
		}

		if !finalStep {
			if run.response.Status != "OK" {
				return aggregateStepResponse(failedStepResponse(run.response, step.ID), stepResults)
			}
			maxBytes := step.Handoff.MaxBytes
			if maxBytes <= 0 {
				maxBytes = int64(outputLimitBytes(stepReq))
			}
			handoffExceeded := int64(len(run.judgeOut)) > maxBytes
			if handoffExceeded {
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusRE,
					Reason:        fmt.Sprintf("step %s handoff exceeded max_bytes", step.ID),
					VerdictSource: "step:" + step.ID + ":handoff",
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
		CPUTimeNs:        measuredStepCPUTime(resp.CPUTimeNs, resp.CPUAccounting),
		RawCPUTimeNs:     resp.RawCPUTimeNs,
		RawCPUTimeMs:     resp.RawCPUTimeMs,
		ProcessCPUTimeMs: resp.ProcessCPUTimeMs,
		MemoryKB:         resp.MemoryKB,
		ExitCode:         resp.ExitCode,
		Stdout:           resp.Stdout,
		Stderr:           resp.Stderr,
		StdoutTruncated:  resp.StdoutTruncated,
		StderrTruncated:  resp.StderrTruncated,
		Reason:           resp.Reason,
		ReasonTruncated:  resp.ReasonTruncated,
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

// Preserve counter diagnostics even when accounting itself fails, while keeping
// initialization failures out of output judging and captured-output delivery.
func initializationFailureResponse(res execResult, wallMs int64) model.RunResponse {
	return model.RunResponse{
		Status: res.Status, TimeMs: wallMs, WallTimeMs: wallMs,
		CPUTimeMs: res.CPUTimeMs, CPUTimeNs: res.CPUTimeNs,
		WorkMeasurement: res.WorkMeasurement,
		RawCPUTimeMs:    res.RawCPUTimeMs, RawCPUTimeNs: res.RawCPUTimeNs,
		CPUAccounting: res.CPUAccounting, ProcessCPUTimeMs: res.ProcessCPUTimeMs,
		MemoryKB: res.MemoryKB, ExitCode: res.ExitCode,
		Reason: res.Reason, VerdictSource: res.VerdictSource,
	}
}

// An older runner or a setup failure can lack a precise CPU sample. Keep that
// distinct from a measured zero, including in sums over completed stages.
func measuredStepCPUTime(ns uint64, accounting *model.CPUAccounting) *uint64 {
	if ns == 0 && accounting == nil {
		return nil
	}
	return &ns
}

func sumStepNanoseconds(total, sample *uint64) *uint64 {
	if total == nil || sample == nil || ^uint64(0)-*total < *sample {
		return nil
	}
	value := *total + *sample
	return &value
}

func aggregateStepResponse(resp model.RunResponse, steps []model.StepResult) model.RunResponse {
	var wallMs int64
	var cpuMs int64
	cpuNs, rawNs := new(uint64), new(uint64)
	var processCPUTimeMs int64
	var rawCPUTimeMs *int64
	var memoryKB int64
	for _, step := range steps {
		wallMs += step.WallTimeMs
		cpuMs += step.CPUTimeMs
		cpuNs = sumStepNanoseconds(cpuNs, step.CPUTimeNs)
		rawNs = sumStepNanoseconds(rawNs, step.RawCPUTimeNs)
		rawCPUTimeMs = sumRawCPUTime(rawCPUTimeMs, step.RawCPUTimeMs)
		processCPUTimeMs += step.ProcessCPUTimeMs
		if step.MemoryKB > memoryKB {
			memoryKB = step.MemoryKB
		}
	}
	resp.TimeMs = wallMs
	resp.WallTimeMs = wallMs
	resp.CPUTimeMs = cpuMs
	resp.RawCPUTimeMs = rawCPUTimeMs
	// Each stage has its own accounting source. Report exact sums only when all
	// executed stages supplied the corresponding precise counter.
	resp.CPUTimeNs, resp.RawCPUTimeNs = 0, nil
	if len(steps) > 0 {
		if cpuNs != nil {
			resp.CPUTimeNs = *cpuNs
		}
		resp.RawCPUTimeNs = rawNs
	}
	resp.CPUAccounting = nil
	resp.ProcessCPUTimeMs = processCPUTimeMs
	if memoryKB > resp.MemoryKB {
		resp.MemoryKB = memoryKB
	}
	resp.Steps = steps
	return resp
}
