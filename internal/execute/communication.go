package execute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/isolation/cgroup"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/runvalidation"
	"aonohako/internal/security"
	"aonohako/internal/timing"
)

const (
	communicationManagerMemoryMB = 512
	communicationResultMaxBytes  = 64 << 10
	communicationMessageMaxBytes = 8 << 10
	communicationDiagnosticBytes = 8 << 10
	communicationStartupTimeout  = 10 * time.Second
	communicationExitGrace       = 100 * time.Millisecond
)

type communicationManagerResult struct {
	Verdict string   `json:"verdict"`
	Score   *float64 `json:"score"`
	Message string   `json:"message"`
}

type communicationProcessResult struct {
	manager     bool
	participant int
	request     *model.RunRequest
	result      execResult
	completedAt time.Time
}

type communicationPreparedProcess struct {
	workDir string
	ws      Workspace
	args    []string
	request *model.RunRequest
}

type communicationOutputWriter struct {
	target    io.Writer
	remaining int64
	onLimit   func()
	once      sync.Once
	exceeded  atomic.Bool
}

func (w *communicationOutputWriter) Write(p []byte) (int, error) {
	originalLength := len(p)
	allowed := originalLength
	if int64(allowed) > w.remaining {
		allowed = int(w.remaining)
	}
	if allowed > 0 {
		n, err := w.target.Write(p[:allowed])
		w.remaining -= int64(n)
		if err != nil {
			return n, err
		}
		if n != allowed {
			return n, io.ErrShortWrite
		}
	}
	if allowed < originalLength {
		w.exceeded.Store(true)
		if w.onLimit != nil {
			w.once.Do(w.onLimit)
		}
	}
	return originalLength, nil
}

func (s *Service) supportsCommunicationV1() bool {
	return s.deploymentTarget == platform.DeploymentTargetSelfHosted && strings.TrimSpace(s.cgroupParentDir) != ""
}

func (s *Service) runCommunication(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig) model.RunResponse {
	startedAt := timing.MonotonicNow()
	if !s.supportsCommunicationV1() {
		return communicationFailure("CommunicationJudge unsupported: communication-v1 requires a self-hosted cgroup runner", "communication:capability", 0)
	}
	if err := runvalidation.ValidateCommunication(req); err != nil {
		return communicationFailure("invalid communication request: "+err.Error(), "communication:request", 0)
	}

	participantProgram, managerProgram := communicationPrograms(req)
	participantReq := communicationProgramRequest(req, participantProgram, req.Limits)
	managerReq := communicationProgramRequest(req, managerProgram, communicationManagerLimits(req.Limits))

	participantTemplate, initFailure := prepareCommunicationProcess(participantReq, tuning, "participant")
	if initFailure != nil {
		return communicationFailure(initFailure.Reason, "communication:participant:init", 0)
	}
	prepared := []communicationPreparedProcess{participantTemplate}
	defer func() {
		for _, process := range prepared {
			_ = os.RemoveAll(process.workDir)
		}
	}()

	participants := make([]communicationPreparedProcess, req.Communication.ParticipantCount)
	participants[0] = participantTemplate
	for i := 1; i < len(participants); i++ {
		process, err := cloneCommunicationProcess(participantTemplate, participantReq)
		if err != nil {
			return communicationFailure("participant workspace preparation failed", "communication:participant:init", 0)
		}
		participants[i] = process
		prepared = append(prepared, process)
	}

	manager, managerInit := prepareCommunicationProcess(managerReq, tuning, "manager")
	if managerInit != nil {
		return communicationFailure(managerInit.Reason, "communication:manager:init", 0)
	}
	prepared = append(prepared, manager)

	inputPath, err := writeCommunicationDataFile(ctx, filepath.Join(manager.ws.RootDir, ".tmp"), "communication-input-*", req.Communication.Input, req.Communication.InputURL, req.Limits)
	if err != nil {
		return communicationFailure("communication input materialization failed", "communication:input", 0)
	}
	answerPath, err := writeCommunicationDataFile(ctx, filepath.Join(manager.ws.RootDir, ".tmp"), "communication-answer-*", req.Communication.Answer, req.Communication.AnswerURL, req.Limits)
	if err != nil {
		return communicationFailure("communication answer materialization failed", "communication:answer", 0)
	}

	aggregate, err := createCommunicationCgroup(s.cgroupParentDir, req.Communication.ParticipantCount, req.Limits)
	if err != nil {
		slog.Warn("communication cgroup creation failed", "err", err)
		return communicationFailure("communication aggregate cgroup setup failed", "communication:cgroup", 0)
	}
	defer cleanupSandboxCgroup("communication", aggregate)

	communicationCtx, cancelCommunication := context.WithCancel(ctx)
	killDone := make(chan struct{})
	go func() {
		defer close(killDone)
		<-communicationCtx.Done()
		if err := aggregate.Kill(); err != nil {
			slog.Warn("communication aggregate cgroup kill failed", "path", aggregate.Path, "err", err)
		}
	}()
	defer func() {
		cancelCommunication()
		<-killDone
	}()

	type participantPipe struct {
		stdinRead    *os.File
		managerWrite *os.File
		managerRead  *os.File
		stdoutWrite  *os.File
	}
	pipes := make([]participantPipe, len(participants))
	managerFiles := make([]*os.File, 0, len(participants)*2+1)
	closePipes := func() {
		for _, pipe := range pipes {
			for _, file := range []*os.File{pipe.stdinRead, pipe.managerWrite, pipe.managerRead, pipe.stdoutWrite} {
				if file != nil {
					_ = file.Close()
				}
			}
		}
		for _, file := range managerFiles {
			if file != nil {
				_ = file.Close()
			}
		}
	}
	defer closePipes()

	for i := range pipes {
		stdinRead, managerWrite, pipeErr := os.Pipe()
		if pipeErr != nil {
			return communicationFailure("communication pipe setup failed", "communication:pipe", 0)
		}
		managerRead, stdoutWrite, pipeErr := os.Pipe()
		if pipeErr != nil {
			_ = stdinRead.Close()
			_ = managerWrite.Close()
			return communicationFailure("communication pipe setup failed", "communication:pipe", 0)
		}
		pipes[i] = participantPipe{stdinRead: stdinRead, managerWrite: managerWrite, managerRead: managerRead, stdoutWrite: stdoutWrite}
		managerFiles = append(managerFiles, managerRead, managerWrite)
	}
	resultRead, resultWrite, err := os.Pipe()
	if err != nil {
		return communicationFailure("manager result pipe setup failed", "communication:result_protocol", 0)
	}
	defer resultRead.Close()
	managerFiles = append(managerFiles, resultWrite)

	resultFD := 6 + len(managerFiles) - 1
	manager.args = append(manager.args, inputPath, answerPath, "/proc/self/fd/"+strconv.Itoa(resultFD), strconv.Itoa(len(participants)))
	for i := range participants {
		readFD := 6 + i*2
		writeFD := readFD + 1
		manager.args = append(manager.args, "/proc/self/fd/"+strconv.Itoa(readFD), "/proc/self/fd/"+strconv.Itoa(writeFD))
	}

	managerResultCh := make(chan struct {
		result communicationManagerResult
		err    error
	}, 1)
	go func() {
		managerResult, readErr := readCommunicationManagerResult(resultRead)
		managerResultCh <- struct {
			result communicationManagerResult
			err    error
		}{result: managerResult, err: readErr}
	}()

	totalProcesses := len(participants) + 1
	readyCh := make(chan bool, totalProcesses)
	releaseTargets := make(chan struct{})
	processCh := make(chan communicationProcessResult, totalProcesses)
	participantCancels := make([]context.CancelFunc, len(participants))
	participantTimers := make([]*time.Timer, len(participants))
	var startedParticipants atomic.Int32
	var firstParticipantFailure *communicationProcessResult
	var managerCompletedAt time.Time
	cancellationTriggered := false

	for i := range participants {
		participantCtx, cancelParticipant := context.WithCancel(communicationCtx)
		participantCancels[i] = cancelParticipant
		process := participants[i]
		process.args = append(process.args, strconv.Itoa(i))
		pipe := pipes[i]
		participantOutput := &communicationOutputWriter{
			target:    pipe.stdoutWrite,
			remaining: int64(outputLimitBytes(process.request)),
			onLimit:   cancelParticipant,
		}
		go func(index int, prepared communicationPreparedProcess) {
			res := executeSandboxCommandWithStreams(
				participantCtx,
				prepared.ws,
				prepared.args,
				prepared.request,
				sandboxStreamConfig{
					stdin:         pipe.stdinRead,
					liveStdin:     true,
					stdout:        participantOutput,
					onStdoutDone:  func() { _ = pipe.stdoutWrite.Close() },
					identity:      communicationParticipantIdentity(index),
					onTargetReady: func() { readyCh <- false },
					onTargetStarted: func() {
						startedParticipants.Add(1)
					},
					targetRelease: releaseTargets,
				},
				Hooks{},
				communicationDiagnosticBytes,
				tuning,
				aggregate.Path,
			)
			if participantOutput.exceeded.Load() {
				res.Status = model.RunStatusRE
				res.Reason = "participant output limit exceeded"
				res.VerdictSource = "output_limit"
			}
			processCh <- communicationProcessResult{
				participant: index,
				request:     prepared.request,
				result:      res,
				completedAt: time.Now(),
			}
		}(i, process)
	}

	managerCtx, cancelManager := context.WithCancel(communicationCtx)
	defer cancelManager()
	go func() {
		res := executeSandboxCommandWithStreams(
			managerCtx,
			manager.ws,
			manager.args,
			manager.request,
			sandboxStreamConfig{
				identity: sandboxIdentity{
					uid: security.CommunicationManagerUID,
					gid: security.CommunicationManagerGID,
				},
				extraFiles:                managerFiles,
				closeExtraFilesAfterStart: true,
				onTargetReady:             func() { readyCh <- true },
				targetRelease:             releaseTargets,
			},
			Hooks{},
			communicationDiagnosticBytes,
			tuning,
			aggregate.Path,
		)
		for _, file := range managerFiles {
			if file != nil {
				_ = file.Close()
			}
		}
		processCh <- communicationProcessResult{
			manager:     true,
			participant: -1,
			request:     manager.request,
			result:      res,
			completedAt: time.Now(),
		}
	}()

	readyProcesses := 0
	results := make([]communicationProcessResult, 0, totalProcesses)
	released := false
	startupTimer := time.NewTimer(communicationStartupTimeout)
	defer startupTimer.Stop()
	for readyProcesses < totalProcesses {
		select {
		case managerReady := <-readyCh:
			readyProcesses++
			_ = managerReady
		case process := <-processCh:
			results = append(results, process)
			if !process.manager && communicationProcessFailed(process) {
				failure := process
				firstParticipantFailure = &failure
			}
			cancellationTriggered = true
			cancelCommunication()
			if !released {
				close(releaseTargets)
				released = true
			}
			readyProcesses = totalProcesses
		case <-ctx.Done():
			cancellationTriggered = true
			cancelCommunication()
			if !released {
				close(releaseTargets)
				released = true
			}
			readyProcesses = totalProcesses
		case <-startupTimer.C:
			cancellationTriggered = true
			cancelCommunication()
			if !released {
				close(releaseTargets)
				released = true
			}
			readyProcesses = totalProcesses
		}
	}
	if !released {
		close(releaseTargets)
		released = true
		for i := range participantTimers {
			cancel := participantCancels[i]
			participantTimers[i] = time.AfterFunc(time.Duration(max(1, req.Limits.TimeMs))*time.Millisecond, cancel)
		}
		managerTimer := time.AfterFunc(time.Duration(max(1, managerReq.Limits.TimeMs))*time.Millisecond, cancelManager)
		defer managerTimer.Stop()
	}
	defer func() {
		for _, timer := range participantTimers {
			if timer != nil {
				timer.Stop()
			}
		}
		for _, cancel := range participantCancels {
			if cancel != nil {
				cancel()
			}
		}
	}()

	var managerProtocol struct {
		result communicationManagerResult
		err    error
	}
	managerProtocolReceived := false
	var managerExitTimer *time.Timer
	for len(results) < totalProcesses {
		process := <-processCh
		results = append(results, process)
		if process.manager {
			managerCompletedAt = process.completedAt
			managerProtocol = <-managerResultCh
			managerProtocolReceived = true
			if communicationProcessFailed(process) || managerProtocol.err != nil {
				cancellationTriggered = true
				cancelCommunication()
			} else {
				managerExitTimer = time.AfterFunc(communicationExitGrace, cancelCommunication)
			}
			continue
		}
		if communicationProcessFailed(process) &&
			firstParticipantFailure == nil &&
			!cancellationTriggered &&
			ctx.Err() == nil &&
			(managerCompletedAt.IsZero() || process.completedAt.Before(managerCompletedAt)) {
			failure := process
			firstParticipantFailure = &failure
			cancellationTriggered = true
			cancelCommunication()
		}
	}
	if managerExitTimer != nil {
		managerExitTimer.Stop()
	}
	cancelCommunication()
	if !managerProtocolReceived {
		managerProtocol = <-managerResultCh
	}

	response := buildCommunicationResponse(
		results,
		managerProtocol.result,
		managerProtocol.err,
		int(startedParticipants.Load()),
		timing.SinceMillis(startedAt),
		firstParticipantFailure,
	)
	if stats, statErr := cgroup.ReadStats(aggregate.Path); statErr == nil {
		if peakKB := (stats.MemoryPeakBytes + 1023) / 1024; peakKB > response.MemoryKB {
			response.MemoryKB = peakKB
		}
		if response.Status == model.RunStatusAccepted || response.Status == model.RunStatusWA {
			if stats.OOMEvents() > 0 || stats.MemoryMaxEvents() > 0 || stats.PidsMaxEvents() > 0 {
				response.Status = model.RunStatusRE
				response.Reason = "communication aggregate resource limit exceeded"
				response.VerdictSource = "communication:aggregate"
				zero := 0.0
				response.Score = &zero
			}
		}
	}
	if response.Status != model.RunStatusAccepted {
		for _, process := range results {
			if process.manager || len(process.result.Stderr) == 0 {
				continue
			}
			response.Stderr, response.StderrTruncated = capturedOutputValue(process.result.Stderr, responseStderrLimitBytes(req))
			break
		}
	}
	emitCapturedLog(hooks, "stderr", []byte(response.Stderr), responseStderrLimitBytes(req))
	return response
}

func communicationPrograms(req *model.RunRequest) (model.RunProgram, model.RunProgram) {
	var participant model.RunProgram
	var manager model.RunProgram
	for _, program := range req.Programs {
		switch program.ID {
		case req.Communication.ParticipantProgramID:
			participant = program
		case req.Communication.ManagerProgramID:
			manager = program
		}
	}
	return participant, manager
}

func communicationProgramRequest(req *model.RunRequest, program model.RunProgram, limits model.Limits) *model.RunRequest {
	return &model.RunRequest{
		Lang:           program.Lang,
		Binaries:       program.Binaries,
		EntryPoint:     program.EntryPoint,
		Limits:         limits,
		CaptureLimits:  req.CaptureLimits,
		ProblemID:      req.ProblemID,
		RuntimeProfile: req.RuntimeProfile,
	}
}

func communicationManagerLimits(participant model.Limits) model.Limits {
	managerTime := participant.TimeMs + 1000
	if managerTime < defaultSPJTimeMs {
		managerTime = defaultSPJTimeMs
	}
	if managerTime > runvalidation.MaxTimeMs {
		managerTime = runvalidation.MaxTimeMs
	}
	return model.Limits{
		TimeMs:         managerTime,
		MemoryMB:       communicationManagerMemoryMB,
		OutputBytes:    participant.OutputBytes,
		WorkspaceBytes: defaultWorkspaceBytes,
	}
}

func prepareCommunicationProcess(req *model.RunRequest, tuning config.RuntimeTuningConfig, label string) (communicationPreparedProcess, *model.RunResponse) {
	workDir, ws, args, failure := prepareInteractiveCommand(req, tuning, label)
	if failure != nil {
		return communicationPreparedProcess{}, failure
	}
	return communicationPreparedProcess{workDir: workDir, ws: ws, args: args, request: req}, nil
}

func cloneCommunicationProcess(template communicationPreparedProcess, req *model.RunRequest) (communicationPreparedProcess, error) {
	workDir, err := createRunWorkDir()
	if err != nil {
		return communicationPreparedProcess{}, err
	}
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return communicationPreparedProcess{}, err
	}
	if err := hardlinkReadOnlyArtifacts(template.ws.BoxDir, ws.BoxDir); err != nil {
		_ = os.RemoveAll(workDir)
		return communicationPreparedProcess{}, err
	}
	args := make([]string, len(template.args))
	for i, arg := range template.args {
		args[i] = strings.Replace(arg, template.ws.RootDir, ws.RootDir, 1)
	}
	return communicationPreparedProcess{workDir: workDir, ws: ws, args: args, request: req}, nil
}

func hardlinkReadOnlyArtifacts(sourceRoot, destinationRoot string) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil || relative == "." {
			return err
		}
		destination := filepath.Join(destinationRoot, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("participant artifact contains a symlink: %s", relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o777|os.ModeSticky)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 {
			return fmt.Errorf("participant artifact is not a read-only regular file: %s", relative)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("participant artifact is not owned by executor uid %d: %s", os.Geteuid(), relative)
		}
		return os.Link(path, destination)
	})
}

func writeCommunicationDataFile(ctx context.Context, dir, pattern, inline, rawURL string, limits model.Limits) (string, error) {
	if strings.TrimSpace(rawURL) == "" {
		return writeTempFile(dir, pattern, inline)
	}
	reader, err := openStdinURL(ctx, rawURL, stdinURLMaxBytes(limits), nil)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	return writeTempFileFromReader(dir, pattern, reader, stdinURLMaxBytes(limits))
}

func createCommunicationCgroup(parent string, participantCount int, limits model.Limits) (cgroup.Group, error) {
	if err := cgroup.EnableControllers(parent, []string{"cpu", "memory", "pids"}); err != nil {
		return cgroup.Group{}, err
	}
	memoryBytes := (int64(participantCount)*int64(limits.MemoryMB) + communicationManagerMemoryMB) * 1024 * 1024
	pids := (participantCount + 1) * (sandboxThreadLimit + 16)
	group, err := cgroup.CreateRunGroup(parent, cgroup.RunName("communication"), cgroup.Limits{
		MemoryMaxBytes:  memoryBytes,
		PidsMax:         pids,
		CPUQuotaMicros:  int64(participantCount+1) * cgroup.SingleCPUQuotaMicros,
		CPUPeriodMicros: cgroup.DefaultCPUPeriodMicros,
	})
	if err != nil {
		return cgroup.Group{}, err
	}
	if err := cgroup.EnableControllers(group.Path, []string{"cpu", "memory", "pids"}); err != nil {
		cleanupSandboxCgroup("communication-setup", group)
		return cgroup.Group{}, err
	}
	return group, nil
}

func communicationParticipantIdentity(index int) sandboxIdentity {
	id, ok := security.CommunicationParticipantUIDForIndex(index)
	if !ok {
		panic("communication participant identity index is out of range")
	}
	return sandboxIdentity{uid: id, gid: id}
}

func readCommunicationManagerResult(reader io.Reader) (communicationManagerResult, error) {
	data, err := io.ReadAll(io.LimitReader(reader, communicationResultMaxBytes+1))
	if err != nil {
		return communicationManagerResult{}, err
	}
	if len(data) > communicationResultMaxBytes {
		return communicationManagerResult{}, fmt.Errorf("manager result exceeds %d bytes", communicationResultMaxBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var result communicationManagerResult
	if err := decoder.Decode(&result); err != nil {
		return communicationManagerResult{}, fmt.Errorf("invalid manager result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return communicationManagerResult{}, fmt.Errorf("manager result must contain exactly one JSON object")
	}
	if result.Verdict != "accepted" && result.Verdict != "wrong_answer" {
		return communicationManagerResult{}, fmt.Errorf("invalid manager verdict")
	}
	if result.Score == nil || math.IsNaN(*result.Score) || math.IsInf(*result.Score, 0) || *result.Score < 0 || *result.Score > 1 {
		return communicationManagerResult{}, fmt.Errorf("manager score must be between 0 and 1")
	}
	if len(result.Message) > communicationMessageMaxBytes {
		return communicationManagerResult{}, fmt.Errorf("manager message exceeds %d bytes", communicationMessageMaxBytes)
	}
	return result, nil
}

func communicationProcessFailed(process communicationProcessResult) bool {
	status, _, _ := classifyRunStatusWithoutOutput(process.request, process.result)
	if status != "OK" {
		return true
	}
	return process.result.ExitCode == nil || *process.result.ExitCode != 0
}

func buildCommunicationResponse(processes []communicationProcessResult, managerResult communicationManagerResult, managerResultErr error, startedParticipants int, wallTimeMs int64, firstParticipantFailure *communicationProcessResult) model.RunResponse {
	response := model.RunResponse{
		Status:              model.RunStatusRE,
		TimeMs:              wallTimeMs,
		WallTimeMs:          wallTimeMs,
		VerdictSource:       "communication",
		StartedParticipants: startedParticipants,
	}
	zero := 0.0
	response.Score = &zero
	var manager *communicationProcessResult
	for i := range processes {
		process := processes[i]
		response.CPUTimeMs += process.result.CPUTimeMs
		response.ProcessCPUTimeMs += process.result.ProcessCPUTimeMs
		response.MemoryKB += process.result.MemoryKB
		if process.manager {
			manager = &process
		}
	}

	if firstParticipantFailure != nil {
		process := *firstParticipantFailure
		status, reason, source := classifyRunStatusWithoutOutput(process.request, process.result)
		if status == "OK" && (process.result.ExitCode == nil || *process.result.ExitCode != 0) {
			status = model.RunStatusRE
			source = "exit_code"
		}
		if status == model.RunStatusInitFail || status == "OK" {
			status = model.RunStatusRE
		}
		response.Status = status
		response.Reason = firstNonEmptyString(
			reason,
			process.result.Reason,
			fmt.Sprintf("participant %d failed", process.participant),
		)
		response.VerdictSource = fmt.Sprintf(
			"participant:%d:%s",
			process.participant,
			firstNonEmptyString(source, "runtime"),
		)
		return response
	}

	if manager == nil {
		response.Reason = "communication manager did not start"
		response.VerdictSource = "manager:init"
		return response
	}
	managerStatus, managerReason, managerSource := classifyRunStatusWithoutOutput(manager.request, manager.result)
	if managerStatus != "OK" || manager.result.ExitCode == nil || *manager.result.ExitCode != 0 {
		response.Reason = firstNonEmptyString(managerReason, manager.result.Reason, "communication manager failed")
		response.VerdictSource = "manager:" + firstNonEmptyString(managerSource, "runtime")
		return response
	}
	if managerResultErr != nil {
		response.Reason = "communication manager did not produce a valid manager-result-v1 result"
		response.VerdictSource = "manager:result_protocol"
		return response
	}
	response.Score = managerResult.Score
	response.Reason = managerResult.Message
	response.VerdictSource = "manager"
	if managerResult.Verdict == "accepted" {
		response.Status = model.RunStatusAccepted
	} else {
		response.Status = model.RunStatusWA
		if strings.TrimSpace(response.Reason) == "" {
			response.Reason = "wrong answer"
		}
	}
	return response
}

func communicationFailure(reason, source string, startedParticipants int) model.RunResponse {
	zero := 0.0
	return model.RunResponse{
		Status:              model.RunStatusRE,
		Reason:              reason,
		VerdictSource:       source,
		Score:               &zero,
		StartedParticipants: startedParticipants,
	}
}
