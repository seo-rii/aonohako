package execute

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

type pipelineResourceStore map[string][]byte

type pipelineArtifactStore struct {
	dir          string
	paths        map[string]string
	storedBytes  int64
	countedPaths map[string]struct{}
}

// executionPlan is the canonical, resolved representation consumed by the
// Pipeline V1 executor. Wire-level ids and resources are validated and bound
// once before any step starts.
type executionPlan struct {
	resources  pipelineResourceStore
	steps      []executionPlanStep
	finalJudge executionPlanFinalJudge
}

type executionPlanStep struct {
	id                  string
	kind                string
	program             model.RunProgram
	interactor          model.RunProgram
	interactorLimits    *model.Limits
	interactorAnswer    []byte
	hasInteractorAnswer bool
	stdin               []executionPlanInput
	outputs             []model.PipelineOutput
	limits              model.Limits
}

type executionPlanInput struct {
	resource   []byte
	artifactID string
}

type executionPlanFinalJudge struct {
	spec         model.PipelineFinalJudge
	input        []byte
	expected     []byte
	actualStepID string
	stepLimits   model.Limits
}

func newPipelineArtifactStore() (*pipelineArtifactStore, error) {
	dir, err := createRunWorkDir()
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &pipelineArtifactStore{
		dir:          dir,
		paths:        map[string]string{},
		countedPaths: map[string]struct{}{},
	}, nil
}

func (s *pipelineArtifactStore) close() {
	if s != nil {
		_ = os.RemoveAll(s.dir)
	}
}

func (s *pipelineArtifactStore) put(id string, data []byte, maxBytes int64) error {
	if int64(len(data)) > maxBytes {
		return fmt.Errorf("artifact %s exceeded max_bytes", id)
	}
	if s.storedBytes+int64(len(data)) > runvalidation.MaxPipelineArtifactTotalBytes {
		return fmt.Errorf("pipeline artifacts exceeded total byte limit")
	}
	file, err := os.CreateTemp(s.dir, "artifact-*")
	if err != nil {
		return err
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Chmod(0o400); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	s.paths[id] = path
	s.countedPaths[path] = struct{}{}
	s.storedBytes += int64(len(data))
	ok = true
	return nil
}

func (s *pipelineArtifactStore) putPath(id, path string, maxBytes int64) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("artifact %s is not a regular file", id)
	}
	if info.Size() > maxBytes {
		return 0, fmt.Errorf("artifact %s exceeded max_bytes", id)
	}
	if _, counted := s.countedPaths[path]; !counted {
		if s.storedBytes+info.Size() > runvalidation.MaxPipelineArtifactTotalBytes {
			return 0, fmt.Errorf("pipeline artifacts exceeded total byte limit")
		}
		s.countedPaths[path] = struct{}{}
		s.storedBytes += info.Size()
	}
	s.paths[id] = path
	return info.Size(), nil
}

func (s *pipelineArtifactStore) open(id string) (*os.File, error) {
	path := s.paths[id]
	if path == "" {
		return nil, fmt.Errorf("artifact %s not found", id)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("artifact %s is not a regular file", id)
	}
	return file, nil
}

func materializePipelineResources(pipeline *model.PipelineV1) (pipelineResourceStore, error) {
	resources := make(pipelineResourceStore, len(pipeline.Resources))
	var total int
	for id, resource := range pipeline.Resources {
		if strings.TrimSpace(resource.DataURL) != "" {
			return nil, fmt.Errorf("pipeline resource %s data_url was not resolved", id)
		}
		data, err := base64.StdEncoding.DecodeString(resource.DataB64)
		if err != nil {
			return nil, fmt.Errorf("pipeline resource %s decode failed", id)
		}
		if len(data) > runvalidation.MaxTextFieldBytes {
			return nil, fmt.Errorf("pipeline resource %s too large", id)
		}
		total += len(data)
		if total > runvalidation.MaxPipelineResourceTotalBytes {
			return nil, fmt.Errorf("pipeline resources total size exceeded")
		}
		resources[id] = append([]byte(nil), data...)
	}
	return resources, nil
}

func compileExecutionPlan(req *model.RunRequest) (*executionPlan, error) {
	if err := runvalidation.ValidatePipelineV1(req); err != nil {
		return nil, err
	}
	resources, err := materializePipelineResources(req.Pipeline)
	if err != nil {
		return nil, err
	}
	programs := make(map[string]model.RunProgram, len(req.Pipeline.Programs))
	for _, program := range req.Pipeline.Programs {
		programs[program.ID] = program
	}
	steps := make([]executionPlanStep, 0, len(req.Pipeline.Steps))
	for _, step := range req.Pipeline.Steps {
		kind := strings.ToLower(strings.TrimSpace(step.Executor.Kind))
		planned := executionPlanStep{
			id:               step.ID,
			kind:             kind,
			interactorLimits: step.Executor.InteractorLimits,
			outputs:          append([]model.PipelineOutput(nil), step.Outputs...),
			limits:           step.Limits,
		}
		for _, ref := range step.Stdin {
			if strings.EqualFold(strings.TrimSpace(ref.Type), "resource") {
				planned.stdin = append(planned.stdin, executionPlanInput{
					resource: resources[strings.TrimSpace(ref.ID)],
				})
			} else {
				planned.stdin = append(planned.stdin, executionPlanInput{artifactID: strings.TrimSpace(ref.ID)})
			}
		}
		if kind == "interactive" {
			planned.program = programs[strings.TrimSpace(step.Executor.ParticipantProgramID)]
			planned.interactor = programs[strings.TrimSpace(step.Executor.InteractorProgramID)]
			if answer := step.Executor.InteractorAnswer; answer != nil {
				planned.interactorAnswer = resources[strings.TrimSpace(answer.ID)]
				planned.hasInteractorAnswer = true
			}
		} else {
			planned.program = programs[strings.TrimSpace(step.Executor.ProgramID)]
		}
		steps = append(steps, planned)
	}
	judge := req.Pipeline.FinalJudge
	return &executionPlan{
		resources: resources,
		steps:     steps,
		finalJudge: executionPlanFinalJudge{
			spec:         judge,
			input:        resources[strings.TrimSpace(judge.Input.ID)],
			expected:     resources[strings.TrimSpace(judge.Expected.ID)],
			actualStepID: strings.TrimSpace(judge.Actual.StepID),
			stepLimits:   steps[len(steps)-1].limits,
		},
	}, nil
}

func preparePipelineStdin(step executionPlanStep, artifacts *pipelineArtifactStore) (*os.File, int64, error) {
	file, err := os.CreateTemp(artifacts.dir, "stdin-*")
	if err != nil {
		return nil, 0, err
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	maxBytes := stdinURLMaxBytes(step.limits)
	written := int64(0)
	for _, ref := range step.stdin {
		var reader io.Reader
		var closeReader func() error
		if ref.artifactID == "" {
			reader = bytes.NewReader(ref.resource)
		} else {
			artifact, err := artifacts.open(ref.artifactID)
			if err != nil {
				return nil, 0, err
			}
			reader = artifact
			closeReader = artifact.Close
		}
		remaining := maxBytes - written
		if remaining < 0 {
			remaining = 0
		}
		n, copyErr := io.Copy(file, io.LimitReader(reader, remaining+1))
		if closeReader != nil {
			_ = closeReader()
		}
		if copyErr != nil {
			return nil, 0, copyErr
		}
		written += n
		if written > maxBytes {
			return nil, 0, fmt.Errorf("stdin exceeded max bytes")
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	removeOnError = false
	return file, maxBytes, nil
}

func (s *Service) runPipelineV1(ctx context.Context, req *model.RunRequest, _ Hooks, tuning config.RuntimeTuningConfig) model.RunResponse {
	return RedactPipelineResponse(s.runPipelineV1Private(ctx, req, tuning))
}

func (s *Service) runPipelineV1Private(ctx context.Context, req *model.RunRequest, tuning config.RuntimeTuningConfig) model.RunResponse {
	plan, err := compileExecutionPlan(req)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error(), VerdictSource: "pipeline:resource"}
	}
	artifacts, err := newPipelineArtifactStore()
	if err != nil {
		slog.Warn("pipeline artifact store creation failed", "err", err)
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "pipeline artifact store creation failed", VerdictSource: "pipeline:artifact"}
	}
	defer artifacts.close()

	stepResults := make([]model.StepResult, 0, len(plan.steps))
	stepStdout := make(map[string][]byte, len(plan.steps))

	for _, step := range plan.steps {
		stdin, stdinMaxBytes, err := preparePipelineStdin(step, artifacts)
		if err != nil {
			return aggregateStepResponse(model.RunResponse{
				Status:        model.RunStatusInitFail,
				Reason:        fmt.Sprintf("step %s stdin preparation failed", step.id),
				VerdictSource: "step:" + step.id + ":stdin",
			}, stepResults)
		}

		var resp model.RunResponse
		var stdout []byte
		var participantFileOutput []byte
		var interactorOutputPath string
		var interactorOutputErr error
		if step.kind == "batch" {
			stepReq := pipelineProgramRequest(req, step.program, step.limits)
			for _, output := range step.outputs {
				source := strings.ToLower(strings.TrimSpace(output.Source.Kind))
				if source == "participant_file" {
					stepReq.FileOutputs = []model.OutputFile{{Path: output.Source.Path}}
				}
				if source == "participant_stdout" && stepReq.Limits.OutputBytes < int(output.MaxBytes) {
					stepReq.Limits.OutputBytes = int(output.MaxBytes)
				}
			}
			captureRawStdout := len(stepReq.FileOutputs) > 0
			run := s.runOneWithStdin(ctx, stepReq, stdin, stdinMaxBytes, Hooks{}, tuning, false, captureRawStdout)
			resp = run.response
			stdout = run.judgeOut
			if captureRawStdout {
				stdout = run.stdoutOut
			}
			if run.stdoutLimitExceeded {
				resp.Status = model.RunStatusRE
				resp.Reason = "stdout exceeded output limit"
				resp.VerdictSource = "stdout"
			}
			if len(stepReq.FileOutputs) > 0 {
				participantFileOutput = run.judgeOut
			}
		} else {
			stepReq := pipelineProgramRequest(req, step.program, step.limits)
			if step.hasInteractorAnswer {
				stepReq.ExpectedStdout = string(step.interactorAnswer)
			}
			stepReq.Interactor = &model.InteractorSpec{
				Lang:       step.interactor.Lang,
				Binaries:   step.interactor.Binaries,
				EntryPoint: step.interactor.EntryPoint,
				Limits:     step.interactorLimits,
			}
			interactorOutputMax := int64(0)
			for _, output := range step.outputs {
				sourceKind := strings.ToLower(strings.TrimSpace(output.Source.Kind))
				if sourceKind == "interactor_output" && output.MaxBytes > interactorOutputMax {
					interactorOutputMax = output.MaxBytes
				}
				if sourceKind == "participant_stdout" && stepReq.Limits.OutputBytes < int(output.MaxBytes) {
					stepReq.Limits.OutputBytes = int(output.MaxBytes)
				}
				if sourceKind == "participant_file" {
					stepReq.FileOutputs = []model.OutputFile{{Path: output.Source.Path}}
					if stepReq.Limits.OutputBytes < int(output.MaxBytes) {
						stepReq.Limits.OutputBytes = int(output.MaxBytes)
					}
				}
			}
			var interactorOutputFile *os.File
			if interactorOutputMax > 0 {
				interactorOutputFile, err = os.CreateTemp(artifacts.dir, "interactor-output-*")
				if err != nil {
					_ = stdin.Close()
					_ = os.Remove(stdin.Name())
					return aggregateStepResponse(model.RunResponse{Status: model.RunStatusInitFail, Reason: "interactor_output artifact creation failed", VerdictSource: "step:" + step.id + ":artifact"}, stepResults)
				}
				interactorOutputPath = interactorOutputFile.Name()
			}
			run := s.runInteractiveExecution(ctx, stepReq, stdin, stdinMaxBytes, interactorOutputFile, interactorOutputMax, true, Hooks{}, tuning)
			if interactorOutputFile != nil {
				if closeErr := interactorOutputFile.Close(); closeErr != nil && run.interactorOutputErr == nil {
					run.interactorOutputErr = closeErr
				}
				if run.interactorOutputErr == nil {
					if chmodErr := os.Chmod(interactorOutputPath, 0o400); chmodErr != nil {
						run.interactorOutputErr = chmodErr
					}
				}
			}
			resp = run.response
			stdout = run.participantStdout
			participantFileOutput = run.participantFileOutput
			interactorOutputErr = run.interactorOutputErr
			if run.participantFileErr != nil {
				interactorOutputErr = run.participantFileErr
			}
			if (resp.Status == "OK" || resp.Status == model.RunStatusAccepted) && resp.StdoutTruncated {
				resp.Status = model.RunStatusRE
				resp.Reason = "participant stdout exceeded output limit"
				resp.VerdictSource = "participant:stdout_limit"
			}
		}
		_ = stdin.Close()
		_ = os.Remove(stdin.Name())

		resp.Stdout = "" // Pipeline artifacts and interactive transcripts are private.
		stepResult := stepResultFromResponse(step.id, step.program.ID, resp)
		stepResult.Stdout = ""
		stepResults = append(stepResults, stepResult)
		if resp.Status != "OK" && resp.Status != model.RunStatusAccepted {
			return aggregateStepResponse(failedStepResponse(resp, step.id), stepResults)
		}
		if interactorOutputErr != nil {
			if interactorOutputPath != "" {
				_ = os.Remove(interactorOutputPath)
			}
			return aggregateStepResponse(model.RunResponse{
				Status:        model.RunStatusRE,
				Reason:        fmt.Sprintf("step %s artifact capture failed: %s", step.id, interactorOutputErr.Error()),
				VerdictSource: "step:" + step.id + ":artifact",
			}, stepResults)
		}

		var artifactBytes int64
		for _, output := range step.outputs {
			var data []byte
			sourceKind := strings.ToLower(strings.TrimSpace(output.Source.Kind))
			switch sourceKind {
			case "participant_stdout":
				data = stdout
			case "participant_file":
				data = participantFileOutput
			case "interactor_output":
				bytes, err := artifacts.putPath(output.ID, interactorOutputPath, output.MaxBytes)
				if err != nil {
					return aggregateStepResponse(model.RunResponse{
						Status:        model.RunStatusRE,
						Reason:        fmt.Sprintf("step %s artifact %s exceeded or could not be stored", step.id, output.ID),
						VerdictSource: "step:" + step.id + ":artifact",
					}, stepResults)
				}
				artifactBytes += bytes
				continue
			}
			if err := artifacts.put(output.ID, data, output.MaxBytes); err != nil {
				return aggregateStepResponse(model.RunResponse{
					Status:        model.RunStatusRE,
					Reason:        fmt.Sprintf("step %s artifact %s exceeded or could not be stored", step.id, output.ID),
					VerdictSource: "step:" + step.id + ":artifact",
				}, stepResults)
			}
			artifactBytes += int64(len(data))
		}
		stepResults[len(stepResults)-1].HandoffBytes = artifactBytes
		stepStdout[step.id] = append([]byte(nil), stdout...)
	}

	actual := stepStdout[plan.finalJudge.actualStepID]
	final := s.runPipelineFinalJudge(ctx, req, plan.finalJudge, actual, tuning)
	return aggregateStepResponse(final, stepResults)
}

// RedactPipelineResponse applies the public Pipeline V1 diagnostic boundary.
// Callers that expose a pipeline response over another transport should apply
// it defensively even when the underlying runner already enforces the contract.
func RedactPipelineResponse(resp model.RunResponse) model.RunResponse {
	resp.Stdout = ""
	resp.Stderr = ""
	resp.SidecarOutputs = nil
	resp.SidecarErrors = nil
	resp.Reason = pipelinePublicReason(resp.Status, resp.VerdictSource)
	for i := range resp.Steps {
		step := &resp.Steps[i]
		step.Stdout = ""
		step.Stderr = ""
		step.Reason = pipelinePublicReason(step.Status, step.VerdictSource)
	}
	return resp
}

func pipelinePublicReason(status, source string) string {
	switch status {
	case "", "OK", model.RunStatusAccepted:
		return ""
	case model.RunStatusWA:
		return "wrong answer"
	case model.RunStatusTLE:
		return "time limit exceeded"
	case model.RunStatusMLE:
		return "memory limit exceeded"
	case model.RunStatusWLE:
		return "workspace limit exceeded"
	case model.RunStatusInitFail:
		return "pipeline initialization failed"
	case model.RunStatusRE:
		if strings.Contains(source, ":artifact") || source == "pipeline:artifact" {
			return "pipeline artifact processing failed"
		}
		if strings.HasPrefix(source, "final:") {
			return "final judge failed"
		}
		return "runtime error"
	default:
		return "pipeline execution failed"
	}
}

func pipelineProgramRequest(req *model.RunRequest, program model.RunProgram, limits model.Limits) *model.RunRequest {
	return &model.RunRequest{
		Lang:              program.Lang,
		Binaries:          program.Binaries,
		Limits:            limits,
		CaptureLimits:     req.CaptureLimits,
		ProblemID:         req.ProblemID,
		RuntimeProfile:    req.RuntimeProfile,
		PythonLibraryMode: req.PythonLibraryMode,
		EnableNetwork:     program.EnableNetwork,
		EntryPoint:        program.EntryPoint,
	}
}

func (s *Service) runPipelineFinalJudge(ctx context.Context, req *model.RunRequest, judge executionPlanFinalJudge, actual []byte, tuning config.RuntimeTuningConfig) model.RunResponse {
	if strings.EqualFold(strings.TrimSpace(judge.spec.Kind), "diff") {
		status := model.RunStatusWA
		if compareOutputs(judge.expected, actual) {
			status = model.RunStatusAccepted
		}
		return model.RunResponse{Status: status, VerdictSource: "final:diff"}
	}

	workDir, err := createRunWorkDir()
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "final judge workspace creation failed", VerdictSource: "final:spj"}
	}
	defer os.RemoveAll(workDir)
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "final judge workspace preparation failed", VerdictSource: "final:spj"}
	}
	judgeReq := &model.RunRequest{
		Stdin:             string(judge.input),
		ExpectedStdout:    string(judge.expected),
		Limits:            judge.stepLimits,
		SPJ:               judge.spec.SPJ,
		PythonLibraryMode: req.PythonLibraryMode,
		RuntimeProfile:    req.RuntimeProfile,
	}
	ok, score, judgeErr := runSPJ(ctx, ws, judgeReq, string(actual), "", nil, tuning, s.cgroupParentDir)
	if judgeErr != nil {
		return model.RunResponse{Status: model.RunStatusRE, Reason: judgeErr.Error(), VerdictSource: "final:spj", Score: score}
	}
	status := model.RunStatusWA
	if ok {
		status = model.RunStatusAccepted
	}
	return model.RunResponse{Status: status, VerdictSource: "final:spj", Score: score}
}
