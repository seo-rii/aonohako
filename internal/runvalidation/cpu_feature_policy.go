package runvalidation

import (
	"fmt"
	"strings"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

// ValidateDisallowedCPUFeatures validates the request-wide contestant policy.
// Trusted interactors and judges keep their own execution environment. The
// worker separately verifies the executable format and enforcement backend;
// an API frontend need not run on the worker's architecture.
func ValidateDisallowedCPUFeatures(req *model.RunRequest) error {
	if req == nil {
		return fmt.Errorf("nil request")
	}
	if err := cpupolicy.Validate(req.DisallowedCPUFeatures); err != nil {
		return fmt.Errorf("invalid disallowed_cpu_features: %w", err)
	}
	if len(req.DisallowedCPUFeatures) == 0 {
		return nil
	}
	if req.Communication != nil {
		return fmt.Errorf("disallowed_cpu_features does not support communication")
	}
	if req.EnableNetwork {
		return fmt.Errorf("disallowed_cpu_features does not support contestant enable_network")
	}
	if len(req.FileOutputs) != 0 || len(req.SidecarOutputs) != 0 {
		return fmt.Errorf("disallowed_cpu_features supports contestant stdin/stdout only; file_outputs and sidecar_outputs are not supported")
	}
	validateContestant := func(label, lang string, enableNetwork bool) error {
		if profiles.NormalizeRunLang(lang) != "binary" {
			return fmt.Errorf("disallowed_cpu_features requires a native binary contestant: %s", label)
		}
		if enableNetwork {
			return fmt.Errorf("disallowed_cpu_features does not support contestant enable_network: %s", label)
		}
		return nil
	}
	if req.Pipeline != nil {
		programs := make(map[string]model.RunProgram, len(req.Pipeline.Programs))
		for _, program := range req.Pipeline.Programs {
			programs[strings.TrimSpace(program.ID)] = program
		}
		if len(req.Pipeline.Steps) == 0 {
			return fmt.Errorf("disallowed_cpu_features requires a contestant pipeline step")
		}
		for _, step := range req.Pipeline.Steps {
			var programID string
			switch strings.ToLower(strings.TrimSpace(step.Executor.Kind)) {
			case "batch":
				programID = strings.TrimSpace(step.Executor.ProgramID)
			case "interactive":
				programID = strings.TrimSpace(step.Executor.ParticipantProgramID)
			default:
				return fmt.Errorf("disallowed_cpu_features requires a batch or interactive pipeline step: %s", step.ID)
			}
			program, ok := programs[programID]
			if !ok {
				return fmt.Errorf("disallowed_cpu_features references unknown contestant program: %s", programID)
			}
			if err := validateContestant("pipeline program "+programID, program.Lang, program.EnableNetwork); err != nil {
				return err
			}
			for _, output := range step.Outputs {
				if strings.EqualFold(strings.TrimSpace(output.Source.Kind), "participant_file") {
					return fmt.Errorf("disallowed_cpu_features does not support participant_file output: %s", step.ID)
				}
			}
		}
		return nil
	}
	if UsesSteps(req) {
		if len(req.Programs) == 0 {
			return fmt.Errorf("disallowed_cpu_features requires contestant step programs")
		}
		for _, program := range req.Programs {
			if err := validateContestant("program "+program.ID, program.Lang, program.EnableNetwork); err != nil {
				return err
			}
		}
		for _, step := range req.Steps {
			if step.Handoff != nil {
				from := strings.ToLower(strings.TrimSpace(step.Handoff.From))
				if from == "file" || from == "file_output" {
					return fmt.Errorf("disallowed_cpu_features does not support contestant file handoff: %s", step.ID)
				}
			}
		}
		return nil
	}
	return validateContestant("lang", req.Lang, req.EnableNetwork)
}
