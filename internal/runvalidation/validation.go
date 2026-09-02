package runvalidation

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/payloadurl"
	"aonohako/internal/profiles"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/util"
)

const (
	MaxTextFieldBytes               = 64 << 20
	MaxTimeMs                       = 600_000
	MaxMemoryMB                     = 4096
	MaxOutputBytes                  = 64 << 20
	MaxCaptureBytes                 = 8 << 20
	MaxWorkspaceBytes               = 1 << 30
	MaxBinaryFiles                  = 512
	MaxPrograms                     = 8
	MaxSteps                        = 2 // Legacy two-step request contract.
	MaxPipelineSteps                = 2 // Pipeline V1 deployment policy, not a schema invariant.
	MaxStdinParts                   = 32
	MaxSidecarOutputs               = 64
	MaxStepHandoffBytes             = MaxOutputBytes
	MaxBinaryFileBytes              = 16 << 20
	MaxBinaryTotalBytes             = 48 << 20
	MaxPipelineResources            = 16
	MaxPipelineOutputs              = 16
	MaxPipelineResourceTotalBytes   = 128 << 20
	MaxPipelineArtifactTotalBytes   = 128 << 20
	MaxPipelineParticipantFileBytes = 8 << 20
	MinCommunicationParticipants    = 2
	MaxCommunicationParticipants    = 64
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
	if req.Communication != nil {
		if len(req.Communication.Input) > MaxTextFieldBytes {
			return fmt.Errorf("communication.input too large: max %d bytes", MaxTextFieldBytes)
		}
		if len(req.Communication.Answer) > MaxTextFieldBytes {
			return fmt.Errorf("communication.answer too large: max %d bytes", MaxTextFieldBytes)
		}
	}
	if err := runtimepolicy.ValidateProfileName(req.RuntimeProfile); err != nil {
		return fmt.Errorf("invalid runtime_profile: %w", err)
	}
	if err := runtimepolicy.ValidateProblemID(req.ProblemID); err != nil {
		return fmt.Errorf("invalid problem_id: %w", err)
	}
	if err := pythonpolicy.ValidateOptionalLibraryMode(req.PythonLibraryMode); err != nil {
		return fmt.Errorf("invalid python_library_mode: %w", err)
	}
	if req.PythonLibraryMode != "" && !UsesPython(req) {
		return fmt.Errorf("python_library_mode requires a Python or PyPy contestant, step program, interactor, or spj")
	}
	if err := ValidateCaptureLimits(req.CaptureLimits); err != nil {
		return err
	}
	if req.Communication != nil {
		if err := ValidateCommunication(req); err != nil {
			return err
		}
	} else if req.Pipeline != nil {
		if err := ValidatePipelineV1(req); err != nil {
			return err
		}
	} else if UsesSteps(req) {
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
	if req.SPJ != nil {
		if err := ValidateSPJ(req.SPJ); err != nil {
			return err
		}
	}
	if _, err := ValidateBinaryBudget(req); err != nil {
		return err
	}
	return nil
}

func ValidateCommunication(req *model.RunRequest) error {
	spec := req.Communication
	if spec == nil {
		return nil
	}
	if spec.Version != 1 {
		return fmt.Errorf("communication.version must be 1")
	}
	if req.Pipeline != nil {
		return fmt.Errorf("communication cannot be combined with pipeline")
	}
	if spec.ResultProtocol != "manager-result-v1" {
		return fmt.Errorf("communication.result_protocol must be manager-result-v1")
	}
	if spec.ParticipantCount < MinCommunicationParticipants || spec.ParticipantCount > MaxCommunicationParticipants {
		return fmt.Errorf("communication.participant_count must be between %d and %d", MinCommunicationParticipants, MaxCommunicationParticipants)
	}
	participantID := strings.TrimSpace(spec.ParticipantProgramID)
	managerID := strings.TrimSpace(spec.ManagerProgramID)
	if participantID == "" || managerID == "" {
		return fmt.Errorf("communication participant_program_id and manager_program_id are required")
	}
	if participantID != spec.ParticipantProgramID || managerID != spec.ManagerProgramID {
		return fmt.Errorf("communication program ids must not contain surrounding whitespace")
	}
	if participantID == managerID {
		return fmt.Errorf("communication participant and manager program ids must differ")
	}
	if len(req.Programs) != 2 || len(req.Steps) != 0 {
		return fmt.Errorf("communication requires exactly two programs and no steps")
	}
	if req.Lang != "" || len(req.Binaries) > 0 || req.Stdin != "" || req.StdinURL != "" || req.ExpectedStdout != "" || req.ExpectedStdoutURL != "" || req.EntryPoint != "" || req.EnableNetwork {
		return fmt.Errorf("communication cannot be combined with legacy execute fields")
	}
	if req.Interactor != nil || req.SPJ != nil || len(req.FileOutputs) > 0 || len(req.SidecarOutputs) > 0 || req.IgnoreTLE {
		return fmt.Errorf("communication cannot be combined with interactor, spj, outputs, or ignore_tle")
	}
	if spec.Input != "" && strings.TrimSpace(spec.InputURL) != "" {
		return fmt.Errorf("communication.input cannot combine inline content with url")
	}
	if spec.Answer != "" && strings.TrimSpace(spec.AnswerURL) != "" {
		return fmt.Errorf("communication.answer cannot combine inline content with url")
	}
	if err := ValidateRequiredLimits("limits", req.Limits); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(req.Programs))
	for i, program := range req.Programs {
		id := strings.TrimSpace(program.ID)
		if id == "" {
			return fmt.Errorf("programs[%d].id is required", i)
		}
		if id != program.ID {
			return fmt.Errorf("programs[%d].id must not contain surrounding whitespace", i)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate program id: %s", id)
		}
		seen[id] = struct{}{}
		if id != participantID && id != managerID {
			return fmt.Errorf("communication contains unreferenced program: %s", id)
		}
		if program.EnableNetwork {
			return fmt.Errorf("communication programs cannot enable network")
		}
		if err := ValidateRunLang("program "+id+" lang", program.Lang); err != nil {
			return err
		}
		if profiles.NormalizeRunLang(program.Lang) != "binary" {
			return fmt.Errorf("communication-v1 supports native binary programs only: %s", id)
		}
		if len(program.Binaries) == 0 {
			return fmt.Errorf("program %s has no binaries", id)
		}
		if err := ValidateBinaries("program "+id+" binaries", program.Binaries); err != nil {
			return err
		}
	}
	if _, ok := seen[participantID]; !ok {
		return fmt.Errorf("communication references unknown participant program: %s", participantID)
	}
	if _, ok := seen[managerID]; !ok {
		return fmt.Errorf("communication references unknown manager program: %s", managerID)
	}
	return nil
}

func ValidateBinaryBudget(req *model.RunRequest) (int, error) {
	if req == nil {
		return 0, nil
	}
	type binaryGroup struct {
		label      string
		binaries   []model.Binary
		singleItem bool
	}
	binaryGroups := []binaryGroup{{label: "binaries", binaries: req.Binaries}}
	for i := range req.Programs {
		binaryGroups = append(binaryGroups, binaryGroup{label: fmt.Sprintf("programs[%d].binaries", i), binaries: req.Programs[i].Binaries})
	}
	if req.Pipeline != nil {
		for i := range req.Pipeline.Programs {
			binaryGroups = append(binaryGroups, binaryGroup{label: fmt.Sprintf("pipeline.programs[%d].binaries", i), binaries: req.Pipeline.Programs[i].Binaries})
		}
		if req.Pipeline.FinalJudge.SPJ != nil && req.Pipeline.FinalJudge.SPJ.Binary != nil {
			binaryGroups = append(binaryGroups, binaryGroup{label: "pipeline.final_judge.spj.binary", binaries: []model.Binary{*req.Pipeline.FinalJudge.SPJ.Binary}, singleItem: true})
		}
	}
	if req.SPJ != nil && req.SPJ.Binary != nil {
		binaryGroups = append(binaryGroups, binaryGroup{label: "spj.binary", binaries: []model.Binary{*req.SPJ.Binary}, singleItem: true})
	}
	if req.Interactor != nil {
		binaryGroups = append(binaryGroups, binaryGroup{label: "interactor.binaries", binaries: req.Interactor.Binaries})
	}

	var decodedBytes int64
	var binaryFiles int
	for _, group := range binaryGroups {
		for i := range group.binaries {
			binaryFiles++
			if binaryFiles > MaxBinaryFiles {
				return 0, fmt.Errorf("too many binaries: max %d", MaxBinaryFiles)
			}
			decoded, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(group.binaries[i].DataB64)))
			if err != nil {
				field := fmt.Sprintf("%s[%d]", group.label, i)
				if group.singleItem {
					field = group.label
				}
				return 0, fmt.Errorf("%s.data_b64 invalid base64: %w", field, err)
			}
			decodedBytes += decoded
			if decodedBytes > int64(MaxBinaryTotalBytes) {
				return 0, fmt.Errorf("binaries total size exceeded: max %d bytes", MaxBinaryTotalBytes)
			}
		}
	}
	return int(decodedBytes), nil
}

func ValidatePipelineV1(req *model.RunRequest) error {
	if req == nil || req.Pipeline == nil {
		return fmt.Errorf("pipeline is required")
	}
	pipeline := req.Pipeline
	if pipeline.Version != 1 {
		return fmt.Errorf("unsupported pipeline version: %d", pipeline.Version)
	}
	if req.Lang != "" || len(req.Binaries) > 0 || len(req.Programs) > 0 || len(req.Steps) > 0 || req.Stdin != "" || strings.TrimSpace(req.StdinURL) != "" || req.ExpectedStdout != "" || strings.TrimSpace(req.ExpectedStdoutURL) != "" || req.EntryPoint != "" || req.EnableNetwork || req.Interactor != nil || req.SPJ != nil || req.Communication != nil || len(req.FileOutputs) > 0 || !LimitsAreZero(req.Limits) {
		return fmt.Errorf("legacy execute fields cannot be combined with pipeline")
	}
	if len(req.SidecarOutputs) > 0 {
		return fmt.Errorf("sidecar_outputs are not supported with pipeline v1")
	}
	if req.IgnoreTLE {
		return fmt.Errorf("ignore_tle is not supported with pipeline v1")
	}
	if len(pipeline.Resources) == 0 {
		return fmt.Errorf("pipeline.resources is required")
	}
	if len(pipeline.Resources) > MaxPipelineResources {
		return fmt.Errorf("too many pipeline resources: max %d", MaxPipelineResources)
	}
	resources := make(map[string]struct{}, len(pipeline.Resources))
	resourceBytes := 0
	for rawID, resource := range pipeline.Resources {
		id := strings.TrimSpace(rawID)
		if id == "" || id != rawID {
			return fmt.Errorf("pipeline resource id must be non-empty and canonical: %q", rawID)
		}
		if resource.DataB64 != "" && strings.TrimSpace(resource.DataURL) != "" {
			return fmt.Errorf("pipeline resource %s cannot combine data_b64 with data_url", id)
		}
		decoded, err := base64.StdEncoding.DecodeString(resource.DataB64)
		if err != nil {
			return fmt.Errorf("pipeline resource %s data_b64 invalid base64: %w", id, err)
		}
		if len(decoded) > MaxTextFieldBytes {
			return fmt.Errorf("pipeline resource %s too large: max %d bytes", id, MaxTextFieldBytes)
		}
		resourceBytes += len(decoded)
		if resourceBytes > MaxPipelineResourceTotalBytes {
			return fmt.Errorf("pipeline resources total size exceeded: max %d bytes", MaxPipelineResourceTotalBytes)
		}
		resources[id] = struct{}{}
	}
	if len(pipeline.Programs) == 0 {
		return fmt.Errorf("pipeline.programs is required")
	}
	if len(pipeline.Programs) > MaxPrograms {
		return fmt.Errorf("too many pipeline programs: max %d", MaxPrograms)
	}
	programs := make(map[string]model.RunProgram, len(pipeline.Programs))
	for i, program := range pipeline.Programs {
		id := strings.TrimSpace(program.ID)
		if id == "" || id != program.ID {
			return fmt.Errorf("pipeline.programs[%d].id must be non-empty and canonical", i)
		}
		if _, exists := programs[id]; exists {
			return fmt.Errorf("duplicate pipeline program id: %s", id)
		}
		if err := ValidateRunLang("pipeline program "+id+" lang", program.Lang); err != nil {
			return err
		}
		if len(program.Binaries) == 0 {
			return fmt.Errorf("pipeline program %s has no binaries", id)
		}
		if err := ValidateBinaries("pipeline program "+id+" binaries", program.Binaries); err != nil {
			return err
		}
		programs[id] = program
	}
	if len(pipeline.Steps) == 0 {
		return fmt.Errorf("pipeline.steps is required")
	}
	if len(pipeline.Steps) > MaxPipelineSteps {
		return fmt.Errorf("too many pipeline steps: deployment max is %d", MaxPipelineSteps)
	}
	artifacts := map[string]int{}
	var artifactMaxBytes int64
	steps := map[string]int{}
	usedPrograms := map[string]struct{}{}
	for i, step := range pipeline.Steps {
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" || stepID != step.ID {
			return fmt.Errorf("pipeline.steps[%d].id must be non-empty and canonical", i)
		}
		if _, exists := steps[stepID]; exists {
			return fmt.Errorf("duplicate pipeline step id: %s", stepID)
		}
		steps[stepID] = i
		if err := ValidateRequiredLimits("pipeline step "+stepID+" limits", step.Limits); err != nil {
			return err
		}
		kind := strings.ToLower(strings.TrimSpace(step.Executor.Kind))
		switch kind {
		case "batch":
			programID, err := canonicalPipelineRefID("pipeline step "+stepID+" program_id", step.Executor.ProgramID)
			if err != nil {
				return err
			}
			if _, ok := programs[programID]; !ok {
				return fmt.Errorf("pipeline step %s references unknown program_id: %s", stepID, step.Executor.ProgramID)
			}
			if step.Executor.ParticipantProgramID != "" || step.Executor.InteractorProgramID != "" {
				return fmt.Errorf("pipeline batch step %s cannot set interactive program ids", stepID)
			}
			if step.Executor.InteractorLimits != nil {
				return fmt.Errorf("pipeline batch step %s cannot set interactor_limits", stepID)
			}
			if step.Executor.InteractorAnswer != nil {
				return fmt.Errorf("pipeline batch step %s cannot set interactor_answer", stepID)
			}
			usedPrograms[programID] = struct{}{}
		case "interactive":
			participantID, err := canonicalPipelineRefID("pipeline interactive step "+stepID+" participant_program_id", step.Executor.ParticipantProgramID)
			if err != nil {
				return err
			}
			interactorID, err := canonicalPipelineRefID("pipeline interactive step "+stepID+" interactor_program_id", step.Executor.InteractorProgramID)
			if err != nil {
				return err
			}
			if _, ok := programs[participantID]; !ok {
				return fmt.Errorf("pipeline interactive step %s references unknown participant_program_id: %s", stepID, step.Executor.ParticipantProgramID)
			}
			if _, ok := programs[interactorID]; !ok {
				return fmt.Errorf("pipeline interactive step %s references unknown interactor_program_id: %s", stepID, step.Executor.InteractorProgramID)
			}
			if programs[interactorID].EnableNetwork {
				return fmt.Errorf("pipeline interactive step %s interactor cannot enable_network", stepID)
			}
			if step.Executor.ProgramID != "" {
				return fmt.Errorf("pipeline interactive step %s cannot set program_id", stepID)
			}
			if step.Executor.InteractorLimits != nil {
				if err := ValidateRequiredLimits("pipeline step "+stepID+" executor.interactor_limits", *step.Executor.InteractorLimits); err != nil {
					return err
				}
			}
			if answer := step.Executor.InteractorAnswer; answer != nil {
				if strings.ToLower(strings.TrimSpace(answer.Type)) != "resource" || answer.StepID != "" {
					return fmt.Errorf("pipeline interactive step %s interactor_answer must reference a resource", stepID)
				}
				answerID, err := canonicalPipelineRefID("pipeline interactive step "+stepID+" interactor_answer.id", answer.ID)
				if err != nil {
					return err
				}
				if _, ok := resources[answerID]; !ok {
					return fmt.Errorf("pipeline interactive step %s interactor_answer references unknown resource: %s", stepID, answer.ID)
				}
			}
			usedPrograms[participantID] = struct{}{}
			usedPrograms[interactorID] = struct{}{}
		default:
			return fmt.Errorf("pipeline step %s executor.kind must be batch or interactive", stepID)
		}
		if len(step.Stdin) > MaxStdinParts {
			return fmt.Errorf("pipeline step %s has too many stdin refs: max %d", stepID, MaxStdinParts)
		}
		for j, ref := range step.Stdin {
			typ := strings.ToLower(strings.TrimSpace(ref.Type))
			id, err := canonicalPipelineRefID(fmt.Sprintf("pipeline step %s stdin[%d].id", stepID, j), ref.ID)
			if err != nil {
				return err
			}
			if ref.StepID != "" {
				return fmt.Errorf("pipeline step %s stdin[%d] cannot set step_id", stepID, j)
			}
			switch typ {
			case "resource":
				if _, ok := resources[id]; !ok {
					return fmt.Errorf("pipeline step %s stdin[%d] references unknown resource: %s", stepID, j, ref.ID)
				}
			case "artifact":
				producer, ok := artifacts[id]
				if !ok || producer >= i {
					return fmt.Errorf("pipeline step %s stdin[%d] must reference an artifact from an earlier step: %s", stepID, j, ref.ID)
				}
			default:
				return fmt.Errorf("pipeline step %s stdin[%d].type must be resource or artifact", stepID, j)
			}
		}
		if len(step.Outputs) > MaxPipelineOutputs {
			return fmt.Errorf("pipeline step %s has too many outputs: max %d", stepID, MaxPipelineOutputs)
		}
		participantFiles := 0
		for j, output := range step.Outputs {
			id := strings.TrimSpace(output.ID)
			if id == "" || id != output.ID {
				return fmt.Errorf("pipeline step %s outputs[%d].id must be non-empty and canonical", stepID, j)
			}
			if _, exists := artifacts[id]; exists {
				return fmt.Errorf("duplicate pipeline artifact id: %s", id)
			}
			if output.MaxBytes <= 0 || output.MaxBytes > MaxStepHandoffBytes {
				return fmt.Errorf("pipeline step %s output %s max_bytes must be between 1 and %d", stepID, id, MaxStepHandoffBytes)
			}
			artifactMaxBytes += output.MaxBytes
			if artifactMaxBytes > MaxPipelineArtifactTotalBytes {
				return fmt.Errorf("pipeline artifact max_bytes total exceeds %d bytes", MaxPipelineArtifactTotalBytes)
			}
			sourceKind := strings.ToLower(strings.TrimSpace(output.Source.Kind))
			switch sourceKind {
			case "participant_stdout":
				if strings.TrimSpace(output.Source.Path) != "" {
					return fmt.Errorf("pipeline output %s participant_stdout cannot set path", id)
				}
			case "participant_file":
				participantFiles++
				if output.MaxBytes > MaxPipelineParticipantFileBytes {
					return fmt.Errorf("pipeline output %s participant_file max_bytes must be at most %d", id, MaxPipelineParticipantFileBytes)
				}
				if _, err := util.ValidateRelativePath(output.Source.Path); err != nil {
					return fmt.Errorf("pipeline output %s participant_file path: %w", id, err)
				}
			case "interactor_output":
				if kind != "interactive" {
					return fmt.Errorf("pipeline output %s interactor_output requires an interactive step", id)
				}
				if strings.TrimSpace(output.Source.Path) != "" {
					return fmt.Errorf("pipeline output %s interactor_output cannot set path", id)
				}
			default:
				return fmt.Errorf("pipeline output %s source.kind is unsupported: %s", id, output.Source.Kind)
			}
			artifacts[id] = i
		}
		if participantFiles > 1 {
			return fmt.Errorf("pipeline step %s supports at most one participant_file output", stepID)
		}
	}
	for _, program := range pipeline.Programs {
		if _, ok := usedPrograms[program.ID]; !ok {
			return fmt.Errorf("unused pipeline program: %s", program.ID)
		}
	}
	judge := pipeline.FinalJudge
	judgeKind := strings.ToLower(strings.TrimSpace(judge.Kind))
	if judgeKind != "diff" && judgeKind != "spj" {
		return fmt.Errorf("pipeline final_judge.kind must be diff or spj")
	}
	if strings.ToLower(strings.TrimSpace(judge.Input.Type)) != "resource" {
		return fmt.Errorf("pipeline final_judge.input must reference a resource")
	}
	judgeInputID, err := canonicalPipelineRefID("pipeline final_judge.input.id", judge.Input.ID)
	if err != nil {
		return err
	}
	if _, ok := resources[judgeInputID]; !ok {
		return fmt.Errorf("pipeline final_judge.input references unknown resource: %s", judge.Input.ID)
	}
	if strings.ToLower(strings.TrimSpace(judge.Expected.Type)) != "resource" {
		return fmt.Errorf("pipeline final_judge.expected must reference a resource")
	}
	judgeExpectedID, err := canonicalPipelineRefID("pipeline final_judge.expected.id", judge.Expected.ID)
	if err != nil {
		return err
	}
	if _, ok := resources[judgeExpectedID]; !ok {
		return fmt.Errorf("pipeline final_judge.expected references unknown resource: %s", judge.Expected.ID)
	}
	if strings.ToLower(strings.TrimSpace(judge.Actual.Type)) != "step_stdout" {
		return fmt.Errorf("pipeline final_judge.actual must be step_stdout")
	}
	actualStepID, err := canonicalPipelineRefID("pipeline final_judge.actual.step_id", judge.Actual.StepID)
	if err != nil {
		return err
	}
	actualStep, ok := steps[actualStepID]
	if !ok {
		return fmt.Errorf("pipeline final_judge.actual references unknown step: %s", judge.Actual.StepID)
	}
	if actualStep != len(pipeline.Steps)-1 {
		return fmt.Errorf("pipeline final_judge.actual must reference the final step")
	}
	if judge.Actual.ID != "" || judge.Input.StepID != "" || judge.Expected.StepID != "" {
		return fmt.Errorf("pipeline final_judge references contain fields invalid for their types")
	}
	if judgeKind == "spj" {
		if judge.SPJ == nil {
			return fmt.Errorf("pipeline final_judge.spj is required for spj")
		}
		if len(judge.SPJ.SidecarOutputs) > 0 {
			return fmt.Errorf("pipeline final_judge.spj.sidecar_outputs are not supported with pipeline v1")
		}
		if err := ValidateSPJ(judge.SPJ); err != nil {
			return fmt.Errorf("pipeline final_judge.spj: %w", err)
		}
	} else if judge.SPJ != nil {
		return fmt.Errorf("pipeline final_judge.spj is only valid for spj")
	}
	return nil
}

func canonicalPipelineRefID(label, raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" || id != raw {
		return "", fmt.Errorf("%s must be non-empty and canonical", label)
	}
	return id, nil
}

func ValidateStepPipeline(req *model.RunRequest) error {
	if len(req.Programs) == 0 || len(req.Steps) == 0 {
		return fmt.Errorf("programs and steps must be provided together")
	}
	if req.Lang != "" || len(req.Binaries) > 0 || req.Stdin != "" || req.StdinURL != "" || req.EntryPoint != "" || req.EnableNetwork || req.Interactor != nil || !LimitsAreZero(req.Limits) {
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
		programID := strings.TrimSpace(program.ID)
		if programID == "" {
			return fmt.Errorf("programs[%d].id is required", i)
		}
		if _, exists := programs[programID]; exists {
			return fmt.Errorf("duplicate program id: %s", programID)
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
		programs[programID] = struct{}{}
	}

	handoffID := ""
	seenSteps := map[string]struct{}{}
	referencedPrograms := make(map[string]struct{}, len(req.Steps))
	for i, step := range req.Steps {
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			return fmt.Errorf("steps[%d].id is required", i)
		}
		if _, exists := seenSteps[stepID]; exists {
			return fmt.Errorf("duplicate step id: %s", stepID)
		}
		seenSteps[stepID] = struct{}{}
		programID := strings.TrimSpace(step.ProgramID)
		if _, ok := programs[programID]; !ok {
			return fmt.Errorf("step %s references unknown program_id: %s", step.ID, step.ProgramID)
		}
		referencedPrograms[programID] = struct{}{}
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
			handoffID = strings.TrimSpace(step.Handoff.ID)
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
	for _, program := range req.Programs {
		programID := strings.TrimSpace(program.ID)
		if _, referenced := referencedPrograms[programID]; !referenced {
			return fmt.Errorf("unused program: %s", programID)
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

func ValidateSPJ(spec *model.SPJSpec) error {
	if spec == nil {
		return nil
	}
	if spec.Limits != nil {
		if err := ValidateOptionalLimits("spj.limits", *spec.Limits); err != nil {
			return err
		}
	}
	if spec.Binary == nil {
		return fmt.Errorf("spj.binary is required")
	}
	cleanName, err := util.ValidateRelativePath(spec.Binary.Name)
	if err != nil {
		return fmt.Errorf("spj.binary.name: %w", err)
	}
	dataB64 := strings.TrimSpace(spec.Binary.DataB64)
	dataURL := strings.TrimSpace(spec.Binary.DataURL)
	if dataB64 == "" && dataURL == "" {
		return fmt.Errorf("spj.binary payload is required")
	}
	if dataB64 != "" && dataURL != "" {
		return fmt.Errorf("spj.binary cannot combine data_b64 with data_url")
	}
	if dataB64 != "" {
		data, err := base64.StdEncoding.DecodeString(spec.Binary.DataB64)
		if err != nil {
			return fmt.Errorf("spj.binary.data_b64 invalid base64: %w", err)
		}
		if len(data) > MaxBinaryFileBytes {
			return fmt.Errorf("spj binary too large: %s", cleanName)
		}
	} else {
		if err := payloadurl.Validate(dataURL); err != nil {
			return fmt.Errorf("spj.binary.data_url: %w", err)
		}
	}
	if err := ValidateRunLang("spj.lang", spec.Lang); err != nil {
		return err
	}
	if len(spec.SidecarOutputs) > MaxSidecarOutputs {
		return fmt.Errorf("spj has too many sidecar outputs: max %d", MaxSidecarOutputs)
	}
	seenSidecars := make(map[string]struct{}, len(spec.SidecarOutputs))
	for i, output := range spec.SidecarOutputs {
		clean, err := util.ValidateRelativePath(output.Path)
		if err != nil {
			return fmt.Errorf("spj.sidecar_outputs[%d].path: %w", i, err)
		}
		if _, exists := seenSidecars[clean]; exists {
			return fmt.Errorf("duplicate spj sidecar output path: %s", clean)
		}
		seenSidecars[clean] = struct{}{}
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

func ValidateCaptureLimits(limits *model.CaptureLimits) error {
	if limits == nil {
		return nil
	}
	for _, field := range []struct {
		name  string
		value *int
	}{
		{name: "stdout_bytes", value: limits.StdoutBytes},
		{name: "stderr_bytes", value: limits.StderrBytes},
	} {
		if field.value != nil && (*field.value < 0 || *field.value > MaxCaptureBytes) {
			return fmt.Errorf("capture_limits.%s must be between 0 and %d", field.name, MaxCaptureBytes)
		}
	}
	return nil
}

func UsesSteps(req *model.RunRequest) bool {
	return req.Communication == nil && req.Pipeline == nil && (len(req.Programs) > 0 || len(req.Steps) > 0)
}

func ProgramsEnableNetwork(req *model.RunRequest) bool {
	for _, program := range req.Programs {
		if program.EnableNetwork {
			return true
		}
	}
	if req.Pipeline != nil {
		for _, program := range req.Pipeline.Programs {
			if program.EnableNetwork {
				return true
			}
		}
	}
	return false
}

func UsesPython(req *model.RunRequest) bool {
	if req == nil {
		return false
	}
	languages := make([]string, 0, len(req.Programs)+3)
	languages = append(languages, req.Lang)
	for _, program := range req.Programs {
		languages = append(languages, program.Lang)
	}
	if req.Interactor != nil {
		languages = append(languages, req.Interactor.Lang)
	}
	if req.SPJ != nil {
		languages = append(languages, req.SPJ.Lang)
	}
	if req.Pipeline != nil {
		for _, program := range req.Pipeline.Programs {
			languages = append(languages, program.Lang)
		}
		if req.Pipeline.FinalJudge.SPJ != nil {
			languages = append(languages, req.Pipeline.FinalJudge.SPJ.Lang)
		}
	}
	for _, language := range languages {
		normalizedLang := profiles.NormalizeRunLang(language)
		if normalizedLang == "python" || normalizedLang == "pypy" {
			return true
		}
	}
	return false
}

func LimitsAreZero(l model.Limits) bool {
	return l.TimeMs == 0 && l.MemoryMB == 0 && l.OutputBytes == 0 && l.WorkspaceBytes == 0
}
