package runvalidation

import (
	"fmt"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/runtimepolicy"
)

const (
	MaxTextFieldBytes   = 16 << 20
	MaxTimeMs           = 60_000
	MaxMemoryMB         = 4096
	MaxOutputBytes      = 8 << 20
	MaxWorkspaceBytes   = 1 << 30
	MaxBinaryFiles      = 512
	MaxPrograms         = 8
	MaxSteps            = 2
	MaxSidecarOutputs   = 64
	MaxStepHandoffBytes = MaxOutputBytes
)

func Validate(req *model.RunRequest) error {
	if len(req.Stdin) > MaxTextFieldBytes {
		return fmt.Errorf("stdin too large: max %d bytes", MaxTextFieldBytes)
	}
	if len(req.ExpectedStdout) > MaxTextFieldBytes {
		return fmt.Errorf("expected_stdout too large: max %d bytes", MaxTextFieldBytes)
	}
	if err := runtimepolicy.ValidateProfileName(req.RuntimeProfile); err != nil {
		return fmt.Errorf("invalid runtime_profile: %w", err)
	}
	if err := runtimepolicy.ValidateProblemID(req.ProblemID); err != nil {
		return fmt.Errorf("invalid problem_id: %w", err)
	}
	if UsesSteps(req) {
		if err := ValidateStepPipeline(req); err != nil {
			return err
		}
	} else {
		if len(req.Binaries) > MaxBinaryFiles {
			return fmt.Errorf("too many binaries: max %d", MaxBinaryFiles)
		}
		if len(req.FileOutputs) > 1 {
			return fmt.Errorf("at most one file output is supported")
		}
		if len(req.SidecarOutputs) > MaxSidecarOutputs {
			return fmt.Errorf("too many sidecar outputs: max %d", MaxSidecarOutputs)
		}
		if err := ValidateRequiredLimits("limits", req.Limits); err != nil {
			return err
		}
	}
	if req.SPJ != nil && req.SPJ.Limits != nil {
		if err := ValidateOptionalLimits("spj.limits", *req.SPJ.Limits); err != nil {
			return err
		}
	}
	return nil
}

func ValidateStepPipeline(req *model.RunRequest) error {
	if len(req.Programs) == 0 || len(req.Steps) == 0 {
		return fmt.Errorf("programs and steps must be provided together")
	}
	if strings.TrimSpace(req.Lang) != "" || len(req.Binaries) > 0 || strings.TrimSpace(req.Stdin) != "" || strings.TrimSpace(req.EntryPoint) != "" || req.EnableNetwork || !LimitsAreZero(req.Limits) {
		return fmt.Errorf("legacy execute fields cannot be combined with programs/steps")
	}
	if len(req.Programs) > MaxPrograms {
		return fmt.Errorf("too many programs: max %d", MaxPrograms)
	}
	if len(req.Steps) != MaxSteps {
		return fmt.Errorf("exactly %d steps are supported", MaxSteps)
	}
	if len(req.FileOutputs) > 1 {
		return fmt.Errorf("at most one file output is supported")
	}
	if len(req.SidecarOutputs) > MaxSidecarOutputs {
		return fmt.Errorf("too many sidecar outputs: max %d", MaxSidecarOutputs)
	}

	programs := make(map[string]struct{}, len(req.Programs))
	for i, program := range req.Programs {
		if strings.TrimSpace(program.ID) == "" {
			return fmt.Errorf("programs[%d].id is required", i)
		}
		if _, exists := programs[program.ID]; exists {
			return fmt.Errorf("duplicate program id: %s", program.ID)
		}
		if strings.TrimSpace(program.Lang) == "" {
			return fmt.Errorf("program %s lang is required", program.ID)
		}
		if len(program.Binaries) == 0 {
			return fmt.Errorf("program %s has no binaries", program.ID)
		}
		if len(program.Binaries) > MaxBinaryFiles {
			return fmt.Errorf("program %s has too many binaries: max %d", program.ID, MaxBinaryFiles)
		}
		programs[program.ID] = struct{}{}
	}

	handoffID := ""
	seenSteps := map[string]struct{}{}
	for i, step := range req.Steps {
		if strings.TrimSpace(step.ID) == "" {
			return fmt.Errorf("steps[%d].id is required", i)
		}
		if _, exists := seenSteps[step.ID]; exists {
			return fmt.Errorf("duplicate step id: %s", step.ID)
		}
		seenSteps[step.ID] = struct{}{}
		if _, ok := programs[step.ProgramID]; !ok {
			return fmt.Errorf("step %s references unknown program_id: %s", step.ID, step.ProgramID)
		}
		if len(step.Stdin) > MaxTextFieldBytes {
			return fmt.Errorf("step %s stdin too large: max %d bytes", step.ID, MaxTextFieldBytes)
		}
		if err := ValidateRequiredLimits("step "+step.ID+" limits", step.Limits); err != nil {
			return err
		}
		if i == 0 {
			if strings.TrimSpace(step.StdinFrom) != "" {
				return fmt.Errorf("first step cannot use stdin_from")
			}
			if step.Handoff == nil {
				return fmt.Errorf("first step handoff is required")
			}
			if strings.TrimSpace(step.Handoff.ID) == "" {
				return fmt.Errorf("first step handoff.id is required")
			}
			from := strings.ToLower(strings.TrimSpace(step.Handoff.From))
			if from == "" {
				from = "stdout"
			}
			if from != "stdout" && from != "file" && from != "file_output" {
				return fmt.Errorf("first step handoff.from must be stdout or file")
			}
			if (from == "file" || from == "file_output") && strings.TrimSpace(step.Handoff.Path) == "" {
				return fmt.Errorf("first step handoff.path is required for file handoff")
			}
			if step.Handoff.MaxBytes < 0 || step.Handoff.MaxBytes > MaxStepHandoffBytes {
				return fmt.Errorf("first step handoff.max_bytes must be between 0 and %d", MaxStepHandoffBytes)
			}
			handoffID = step.Handoff.ID
			continue
		}
		if strings.TrimSpace(step.StdinFrom) != handoffID {
			return fmt.Errorf("second step stdin_from must reference first step handoff id")
		}
		if step.Stdin != "" {
			return fmt.Errorf("step %s stdin cannot be combined with stdin_from", step.ID)
		}
		if step.Handoff != nil {
			return fmt.Errorf("second step handoff is not supported")
		}
	}
	return nil
}

func ValidateRequiredLimits(name string, limits model.Limits) error {
	if limits.TimeMs <= 0 || limits.TimeMs > MaxTimeMs {
		return fmt.Errorf("%s.time_ms must be between 1 and %d", name, MaxTimeMs)
	}
	if limits.MemoryMB <= 0 || limits.MemoryMB > MaxMemoryMB {
		return fmt.Errorf("%s.memory_mb must be between 1 and %d", name, MaxMemoryMB)
	}
	return ValidateOptionalLimits(name, limits)
}

func ValidateOptionalLimits(name string, limits model.Limits) error {
	if limits.TimeMs < 0 || limits.TimeMs > MaxTimeMs {
		return fmt.Errorf("%s.time_ms must be between 0 and %d", name, MaxTimeMs)
	}
	if limits.MemoryMB < 0 || limits.MemoryMB > MaxMemoryMB {
		return fmt.Errorf("%s.memory_mb must be between 0 and %d", name, MaxMemoryMB)
	}
	if limits.OutputBytes < 0 || limits.OutputBytes > MaxOutputBytes {
		return fmt.Errorf("%s.output_bytes must be between 0 and %d", name, MaxOutputBytes)
	}
	if limits.WorkspaceBytes < 0 || limits.WorkspaceBytes > MaxWorkspaceBytes {
		return fmt.Errorf("%s.workspace_bytes must be between 0 and %d", name, MaxWorkspaceBytes)
	}
	return nil
}

func UsesSteps(req *model.RunRequest) bool {
	return len(req.Programs) > 0 || len(req.Steps) > 0
}

func ProgramsEnableNetwork(req *model.RunRequest) bool {
	for _, program := range req.Programs {
		if program.EnableNetwork {
			return true
		}
	}
	return false
}

func LimitsAreZero(l model.Limits) bool {
	return l.TimeMs == 0 && l.MemoryMB == 0 && l.OutputBytes == 0 && l.WorkspaceBytes == 0
}
