package execute

import (
	"fmt"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func stagedPeerClocksRequested(req *model.RunRequest) bool {
	return req != nil && (req.Pipeline != nil || len(req.Steps) != 0) && req.WorkMeasurementPeerTime != nil && req.WorkMeasurementPeerTime.Version == 2
}

func stageRolePlan(plans map[peerTimeIdentity]peerTimePlan, stepID, role, programID string) peerTimePlan {
	return plans[peerTimeKey(role, programID, nil, stepID)]
}

func stageOrStandaloneSPJ(req *model.RunRequest) *model.SPJSpec {
	if req.Pipeline != nil {
		return req.Pipeline.FinalJudge.SPJ
	}
	return req.SPJ
}

func pipelineClockInteractiveRequest(req *model.RunRequest, program, interactor model.RunProgram, step model.PipelineStep) *model.RunRequest {
	single := pipelineBatchRequest(req, program, step.Limits, step.Outputs)
	single.Interactor = &model.InteractorSpec{Lang: interactor.Lang, Binaries: interactor.Binaries, EntryPoint: interactor.EntryPoint, Limits: step.Executor.InteractorLimits}
	for _, output := range step.Outputs {
		if strings.EqualFold(strings.TrimSpace(output.Source.Kind), "participant_file") {
			single.Limits.OutputBytes = max(single.Limits.OutputBytes, int(output.MaxBytes))
		}
	}
	return single
}

// Resolve launcher and raw limits using the same per-step requests as execution.
// Admission does not download resources or open artifacts.
func stageWorkMeasurementRoles(req *model.RunRequest) ([]workMeasurementRole, error) {
	var roles []workMeasurementRole
	var last *model.RunRequest
	add := func(step, role, id string, interactive bool, single *model.RunRequest) error {
		if single == nil || id == "" || !durante.SupportsRuntimeOptions(single.Lang) {
			return fmt.Errorf("stage %q %s requires a registered source-language launcher", step, role)
		}
		roles = append(roles, workMeasurementRole{stepID: step, role: role, programID: id, language: single.Lang, interactive: interactive, stageRequest: single})
		return nil
	}
	if p := req.Pipeline; p != nil {
		if p.Version != 1 || len(p.Steps) == 0 || len(p.Steps) > runvalidation.MaxPipelineSteps {
			return nil, fmt.Errorf("unsupported stage clock pipeline")
		}
		programs := make(map[string]model.RunProgram, len(p.Programs))
		for _, program := range p.Programs {
			programs[program.ID] = program
		}
		for _, step := range p.Steps {
			switch strings.ToLower(strings.TrimSpace(step.Executor.Kind)) {
			case "batch":
				program := programs[strings.TrimSpace(step.Executor.ProgramID)]
				last = pipelineBatchRequest(req, program, step.Limits, step.Outputs)
				if err := add(step.ID, "contestant", program.ID, false, last); err != nil {
					return nil, err
				}
			case "interactive":
				program, interactor := programs[strings.TrimSpace(step.Executor.ParticipantProgramID)], programs[strings.TrimSpace(step.Executor.InteractorProgramID)]
				last = pipelineClockInteractiveRequest(req, program, interactor, step)
				if err := add(step.ID, "contestant", program.ID, true, last); err != nil {
					return nil, err
				}
				if err := add(step.ID, "interactor", interactor.ID, true, interactorRunRequest(last)); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unknown stage clock executor")
			}
		}
	} else {
		if len(req.Steps) == 0 || len(req.Steps) > runvalidation.MaxSteps {
			return nil, fmt.Errorf("invalid legacy stage count")
		}
		for i, step := range req.Steps {
			last = legacyStepRequest(req, i)
			if err := add(step.ID, "contestant", step.ProgramID, false, last); err != nil {
				return nil, err
			}
		}
	}
	if spj := stageOrStandaloneSPJ(req); spj != nil {
		limits := last.Limits
		if req.Pipeline != nil {
			limits = req.Pipeline.Steps[len(req.Pipeline.Steps)-1].Limits
		}
		checker := spjRunRequest(&model.RunRequest{SPJ: spj, Limits: limits, PythonLibraryMode: req.PythonLibraryMode, RuntimeProfile: req.RuntimeProfile})
		if err := add("", "checker", "checker", false, checker); err != nil {
			return nil, err
		}
	}
	return roles, nil
}

// ValidateStagedClockAdmission resolves all stage models before the HTTP
// endpoint buffers private URL resources or passes the request to a runner.
func ValidateStagedClockAdmission(req *model.RunRequest, cfg config.ExecutionConfig) error {
	if !stagedPeerClocksRequested(req) {
		return nil
	}
	tuning, err := resolveRuntimeTuning(req, cfg.RuntimeTuning, cfg.RuntimeTuningProfiles)
	if err != nil {
		return err
	}
	_, err = workMeasurementPeerTimePlans(req, cfg.WorkMeasurement, tuning)
	return err
}
