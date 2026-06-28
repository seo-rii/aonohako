package runvalidation

import (
	"encoding/base64"
	"fmt"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/util"
)

const (
	MaxTextFieldBytes   = 64 << 20
	MaxTimeMs           = 600_000
	MaxMemoryMB         = 4096
	MaxOutputBytes      = 64 << 20
	MaxWorkspaceBytes   = 1 << 30
	MaxBinaryFiles      = 512
	MaxPrograms         = 8
	MaxSteps            = 2
	MaxStdinParts       = 32
	MaxSidecarOutputs   = 64
	MaxStepHandoffBytes = MaxOutputBytes
	MaxBinaryFileBytes  = 16 << 20
	MaxBinaryTotalBytes = 48 << 20
)

var supportedRunLangs = func() map[string]struct{} {
	out := map[string]struct{}{}
	for _, profile := range profiles.All() {
		if profile.RunLang != "" {
			out[profile.RunLang] = struct{}{}
		}
	}
	return out
}()

func Validate(req *model.RunRequest) error {
	if len(req.Stdin) > MaxTextFieldBytes {
		return fmt.Errorf("stdin too large: max %d bytes", MaxTextFieldBytes)
	}
	if req.Stdin != "" && strings.TrimSpace(req.StdinURL) != "" {
		return fmt.Errorf("stdin cannot combine inline content with url")
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
		if err := ValidateRunLang("lang", req.Lang); err != nil {
			return err
		}
		if len(req.Binaries) > MaxBinaryFiles {
			return fmt.Errorf("too many binaries: max %d", MaxBinaryFiles)
		}
		if err := ValidateBinaries("binaries", req.Binaries); err != nil {
			return err
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
	if req.Interactor != nil {
		if err := ValidateInteractor(req); err != nil {
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
	if strings.TrimSpace(req.Lang) != "" || len(req.Binaries) > 0 || strings.TrimSpace(req.Stdin) != "" || strings.TrimSpace(req.StdinURL) != "" || strings.TrimSpace(req.EntryPoint) != "" || req.EnableNetwork || req.Interactor != nil || !LimitsAreZero(req.Limits) {
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
		if err := ValidateRunLang("program "+program.ID+" lang", program.Lang); err != nil {
			return err
		}
		if len(program.Binaries) == 0 {
			return fmt.Errorf("program %s has no binaries", program.ID)
		}
		if len(program.Binaries) > MaxBinaryFiles {
			return fmt.Errorf("program %s has too many binaries: max %d", program.ID, MaxBinaryFiles)
		}
		if err := ValidateBinaries("program "+program.ID+" binaries", program.Binaries); err != nil {
			return err
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
		if len(step.StdinParts) > 0 && (step.Stdin != "" || strings.TrimSpace(step.StdinURL) != "" || strings.TrimSpace(step.StdinFrom) != "") {
			return fmt.Errorf("step %s stdin_parts cannot be combined with stdin, stdin_url, or stdin_from", step.ID)
		}
		if step.Stdin != "" && strings.TrimSpace(step.StdinURL) != "" {
			return fmt.Errorf("step %s stdin cannot combine inline content with url", step.ID)
		}
		if len(step.Stdin) > MaxTextFieldBytes {
			return fmt.Errorf("step %s stdin too large: max %d bytes", step.ID, MaxTextFieldBytes)
		}
		stdinPartsReferenceHandoff, err := ValidateStepStdinParts(step, i == 0, handoffID)
		if err != nil {
			return err
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
		if len(step.StdinParts) > 0 {
			if !stdinPartsReferenceHandoff {
				return fmt.Errorf("second step stdin_parts must reference first step handoff id")
			}
			if step.Handoff != nil {
				return fmt.Errorf("second step handoff is not supported")
			}
			continue
		}
		if strings.TrimSpace(step.StdinFrom) != handoffID {
			return fmt.Errorf("second step stdin_from must reference first step handoff id")
		}
		if step.Stdin != "" || strings.TrimSpace(step.StdinURL) != "" {
			return fmt.Errorf("step %s stdin cannot be combined with stdin_from", step.ID)
		}
		if step.Handoff != nil {
			return fmt.Errorf("second step handoff is not supported")
		}
	}
	return nil
}

func ValidateStepStdinParts(step model.RunStep, firstStep bool, handoffID string) (bool, error) {
	if len(step.StdinParts) == 0 {
		return false, nil
	}
	if len(step.StdinParts) > MaxStdinParts {
		return false, fmt.Errorf("step %s has too many stdin_parts: max %d", step.ID, MaxStdinParts)
	}

	totalTextBytes := 0
	referencesHandoff := false
	for i, part := range step.StdinParts {
		partType := strings.ToLower(strings.TrimSpace(part.Type))
		switch partType {
		case "text":
			if strings.TrimSpace(part.ID) != "" || strings.TrimSpace(part.From) != "" {
				return false, fmt.Errorf("step %s stdin_parts[%d] text part cannot reference a handoff", step.ID, i)
			}
			if part.Data != "" && strings.TrimSpace(part.DataURL) != "" {
				return false, fmt.Errorf("step %s stdin_parts[%d] text part cannot combine data with data_url", step.ID, i)
			}
			totalTextBytes += len(part.Data)
			if totalTextBytes > MaxTextFieldBytes {
				return false, fmt.Errorf("step %s stdin_parts text too large: max %d bytes", step.ID, MaxTextFieldBytes)
			}
		case "handoff":
			if firstStep {
				return false, fmt.Errorf("first step cannot use handoff stdin_part")
			}
			if part.Data != "" || strings.TrimSpace(part.DataURL) != "" {
				return false, fmt.Errorf("step %s stdin_parts[%d] handoff part cannot include data", step.ID, i)
			}
			if stdinPartHandoffID(part) == "" {
				return false, fmt.Errorf("step %s stdin_parts[%d] handoff id is required", step.ID, i)
			}
			if stdinPartHandoffID(part) != handoffID {
				return false, fmt.Errorf("step %s stdin_parts[%d] handoff must reference first step handoff id", step.ID, i)
			}
			referencesHandoff = true
		default:
			return false, fmt.Errorf("step %s stdin_parts[%d].type must be text or handoff", step.ID, i)
		}
	}
	return referencesHandoff, nil
}

func stdinPartHandoffID(part model.StdinPart) string {
	if strings.TrimSpace(part.From) != "" {
		return strings.TrimSpace(part.From)
	}
	return strings.TrimSpace(part.ID)
}

func ValidateInteractor(req *model.RunRequest) error {
	if UsesSteps(req) {
		return fmt.Errorf("interactor cannot be combined with programs/steps")
	}
	if req.SPJ != nil {
		return fmt.Errorf("interactor cannot be combined with spj")
	}
	if len(req.FileOutputs) > 0 {
		return fmt.Errorf("interactor cannot be combined with file_outputs")
	}
	if req.IgnoreTLE {
		return fmt.Errorf("interactor cannot be combined with ignore_tle")
	}
	if err := ValidateRunLang("interactor.lang", req.Interactor.Lang); err != nil {
		return err
	}
	if len(req.Interactor.Binaries) == 0 {
		return fmt.Errorf("interactor.binaries is required")
	}
	if len(req.Interactor.Binaries) > MaxBinaryFiles {
		return fmt.Errorf("interactor has too many binaries: max %d", MaxBinaryFiles)
	}
	if err := ValidateBinaries("interactor.binaries", req.Interactor.Binaries); err != nil {
		return err
	}
	if req.Interactor.Limits != nil {
		if err := ValidateOptionalLimits("interactor.limits", *req.Interactor.Limits); err != nil {
			return err
		}
	}
	return nil
}

func ValidateBinaries(label string, binaries []model.Binary) error {
	seenPaths := make(map[string]struct{}, len(binaries))
	totalBytes := 0
	for i, b := range binaries {
		clean, err := util.ValidateRelativePath(b.Name)
		if err != nil {
			return fmt.Errorf("%s[%d].name: %w", label, i, err)
		}
		if _, ok := seenPaths[clean]; ok {
			return fmt.Errorf("duplicate binary path: %s", clean)
		}
		seenPaths[clean] = struct{}{}

		data, err := base64.StdEncoding.DecodeString(b.DataB64)
		if err != nil {
			return fmt.Errorf("%s[%d].data_b64 invalid base64: %w", label, i, err)
		}
		if len(data) > MaxBinaryFileBytes {
			return fmt.Errorf("binary too large: %s", clean)
		}
		totalBytes += len(data)
		if totalBytes > MaxBinaryTotalBytes {
			return fmt.Errorf("binaries total size exceeded")
		}
	}
	return nil
}

func ValidateRunLang(label, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%s is required", label)
	}
	lang := profiles.NormalizeRunLang(raw)
	if _, ok := supportedRunLangs[lang]; !ok {
		return fmt.Errorf("unsupported %s: %s", label, strings.TrimSpace(raw))
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
