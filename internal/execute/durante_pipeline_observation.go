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

type pipelineBatchReplay struct {
	request *model.RunRequest
	inputs  executionPlanStep
	outputs [2][sha256.Size]byte
}

// Compare stdout and the participant file independently, including binary bytes.
func pipelineOutputDigests(stdout, file []byte) [2][sha256.Size]byte {
	return [2][sha256.Size]byte{sha256.Sum256(stdout), sha256.Sum256(file)}
}

// Resolve just the batch contestant and its effective limits. The remote
// validator can use this without decoding or exposing private pipeline inputs.
func pipelineObservationRequest(req *model.RunRequest, step model.PipelineStep) *model.RunRequest {
	if req == nil || req.Pipeline == nil || !strings.EqualFold(strings.TrimSpace(step.Executor.Kind), "batch") {
		return nil
	}
	for _, program := range req.Pipeline.Programs {
		if program.ID == strings.TrimSpace(step.Executor.ProgramID) {
			single := pipelineBatchRequest(req, program, step.Limits, step.Outputs)
			if !runvalidation.UsesPython(single) {
				single.PythonLibraryMode = ""
			}
			return single
		}
	}
	return nil
}

func (s *Service) observePipelineSteps(ctx context.Context, req *model.RunRequest, steps []model.StepResult, replays map[string]pipelineBatchReplay, artifacts *pipelineArtifactStore, tuning config.RuntimeTuningConfig) *model.WorkMeasurementObservation {
	observation := &model.WorkMeasurementObservation{Version: 2, Authority: durante.Authority, State: "pipeline"}
	for i, step := range steps {
		row := model.PipelineStepObservation{StepID: step.ID, ProgramID: step.ProgramID, State: "unavailable"}
		replay, ready := replays[step.ID]
		switch {
		case ctx.Err() != nil:
			row.Reason = "pipeline observation cancelled"
		case !strings.EqualFold(strings.TrimSpace(req.Pipeline.Steps[i].Executor.Kind), "batch"):
			row.Reason = "interactive pipeline step is not replayed"
		case !ready:
			row.Reason = "pipeline step has no eligible native output"
		default:
			row.Report, row.Reason = s.replayPipelineBatch(ctx, replay, artifacts, tuning)
			if row.Report != nil {
				row.State, row.OutputCheck = "complete", "native_output"
			}
		}
		observation.Steps = append(observation.Steps, row)
	}
	return observation
}

func (s *Service) replayPipelineBatch(ctx context.Context, snapshot pipelineBatchReplay, artifacts *pipelineArtifactStore, tuning config.RuntimeTuningConfig) (*durante.Report, string) {
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
