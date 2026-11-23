package execute

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

type pipelineStepReplay struct {
	transcript *interactiveTranscript
	request    *model.RunRequest
	inputs     executionPlanStep
	outputs    [2][sha256.Size]byte
}

// Compare stdout and the participant file independently, including binary bytes.
func pipelineOutputDigests(stdout, file []byte) [2][sha256.Size]byte {
	return [2][sha256.Size]byte{sha256.Sum256(stdout), sha256.Sum256(file)}
}

type skipInteractiveReplayKey struct{}

// WithoutInteractivePipelineReplay preserves the batch-only allowance of older controllers.
func WithoutInteractivePipelineReplay(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipInteractiveReplayKey{}, true)
}

// Resolve only the contestant that will be measured. The interactor never runs
// in the replay. Keep the native stage's stdout and declared file limits.
func pipelineObservationRequest(req *model.RunRequest, step model.PipelineStep) *model.RunRequest {
	if req == nil || req.Pipeline == nil {
		return nil
	}
	id := step.Executor.ProgramID
	interactive := strings.EqualFold(strings.TrimSpace(step.Executor.Kind), "interactive")
	if interactive {
		id = step.Executor.ParticipantProgramID
	} else if !strings.EqualFold(strings.TrimSpace(step.Executor.Kind), "batch") {
		return nil
	}
	for _, program := range req.Pipeline.Programs {
		if program.ID == strings.TrimSpace(id) {
			single := pipelineBatchRequest(req, program, step.Limits, step.Outputs)
			if interactive {
				for _, output := range step.Outputs {
					if strings.EqualFold(strings.TrimSpace(output.Source.Kind), "participant_file") {
						single.Limits.OutputBytes = max(single.Limits.OutputBytes, int(output.MaxBytes))
					}
				}
			}
			if !runvalidation.UsesPython(single) {
				single.PythonLibraryMode = ""
			}
			return single
		}
	}
	return nil
}

func (s *Service) observePipelineSteps(ctx context.Context, req *model.RunRequest, steps []model.StepResult, replays map[string]pipelineStepReplay, artifacts *pipelineArtifactStore, tuning config.RuntimeTuningConfig) *model.WorkMeasurementObservation {
	observation := &model.WorkMeasurementObservation{Version: 2, Authority: durante.Authority, State: "pipeline"}
	for i, step := range steps {
		row := model.PipelineStepObservation{StepID: step.ID, ProgramID: step.ProgramID, State: "unavailable"}
		replay, ready := replays[step.ID]
		switch {
		case ctx.Err() != nil:
			row.Reason = "pipeline observation cancelled"
		case !ready:
			row.Reason = "pipeline step has no eligible native output"
			if strings.EqualFold(strings.TrimSpace(req.Pipeline.Steps[i].Executor.Kind), "interactive") {
				row.Reason = "native interactive dialogue is unavailable for replay"
			}
		default:
			check := "native_output"
			if replay.transcript != nil {
				check = "interactive_transcript"
				row.Report, row.Reason = s.replayInteractiveContestant(ctx, replay.request, replay.transcript, replay.outputs, tuning)
			} else {
				row.Report, row.Reason = s.replayPipelineBatch(ctx, replay, artifacts, tuning)
			}
			if row.Report != nil {
				row.State, row.OutputCheck = "complete", check
			}
		}
		observation.Steps = append(observation.Steps, row)
	}
	return observation
}

func (s *Service) replayPipelineBatch(ctx context.Context, snapshot pipelineStepReplay, artifacts *pipelineArtifactStore, tuning config.RuntimeTuningConfig) (*durante.Report, string) {
	// The compiled inputs reference the original resolved resource bytes and
	// immutable native handoffs. Materialize one replay input at a time.
	input, maxBytes, err := preparePipelineStdin(snapshot.inputs, artifacts)
	if err != nil {
		return nil, "pipeline observation input unavailable"
	}
	defer func() {
		_ = input.Close()
		_ = os.Remove(input.Name())
	}()
	return s.observeBatchWithStdin(ctx, snapshot.request, snapshot.outputs, input, maxBytes, tuning)
}

func (s *Service) observeBatchWithStdin(ctx context.Context, req *model.RunRequest, outputs [2][sha256.Size]byte, input io.Reader, maxBytes int64, tuning config.RuntimeTuningConfig) (*durante.Report, string) {
	policy := s.workMeasurement.ResourcePolicy
	if policy == nil || policy.ValidateRequest(policy.MaxLimits) != nil {
		return nil, "pipeline observation resource policy unavailable"
	}
	replay := observationRequest(req, policy.MaxLimits)
	hasFile := len(replay.FileOutputs) > 0
	run := s.runWorkMeasurementWithStdin(ctx, &replay, input, maxBytes, Hooks{}, tuning, hasFile)
	if ctx.Err() != nil {
		return nil, "pipeline observation cancelled"
	}
	return batchReplayReport(req, outputs, run, s.workMeasurement)
}

func batchReplayReport(req *model.RunRequest, outputs [2][sha256.Size]byte, run sandboxRunResult, cfg durante.Config) (*durante.Report, string) {
	envelope := run.response.WorkMeasurementExecution
	if envelope == nil || envelope.State != "complete" || run.stdoutLimitExceeded ||
		!observationReportMatches(req, cfg, run.response.WorkMeasurement) {
		return nil, "pipeline replay did not produce a complete compatible measurement"
	}
	stdout := run.judgeOut
	var file []byte
	if len(req.FileOutputs) > 0 {
		stdout, file = run.stdoutOut, run.judgeOut
	}
	if outputs != pipelineOutputDigests(stdout, file) {
		return nil, "pipeline replay output differs from native output"
	}
	return run.response.WorkMeasurement, ""
}
