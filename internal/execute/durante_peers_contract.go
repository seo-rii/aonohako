package execute

import (
	"fmt"

	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

type workMeasurementRole struct {
	role             string
	programID        string
	participantIndex *int
	language         string
}

// The same identities bind local admission and remote reports. In particular,
// communication's empty top-level language and normalized binary commands must
// never replace each program's declared source-language profile.
func workMeasurementRequestRoles(req *model.RunRequest) ([]workMeasurementRole, error) {
	if req == nil {
		return nil, fmt.Errorf("nil work measurement request")
	}
	if req.Pipeline != nil || req.SPJ != nil || req.EntryMode != "" || len(req.Steps) != 0 || (req.Communication == nil && len(req.Programs) != 0) {
		return nil, fmt.Errorf("work_measurement does not support spj, pipelines, entry modes, or steps")
	}
	if req.Communication == nil {
		roles := []workMeasurementRole{{role: "contestant", programID: "contestant", language: req.Lang}}
		if req.Interactor != nil {
			roles = append(roles, workMeasurementRole{role: "interactor", programID: "interactor", language: req.Interactor.Lang})
		}
		return roles, nil
	}
	if req.Interactor != nil {
		return nil, fmt.Errorf("work_measurement cannot combine communication with interactor")
	}
	spec := req.Communication
	if spec.ParticipantCount < runvalidation.MinCommunicationParticipants || spec.ParticipantCount > runvalidation.MaxCommunicationParticipants {
		return nil, fmt.Errorf("invalid communication participant count for work measurement")
	}
	ids := make([]string, spec.ParticipantCount)
	switch spec.Version {
	case 1:
		if len(spec.ParticipantProgramIDs) != 0 {
			return nil, fmt.Errorf("communication-v1 work measurement requires participant_program_id")
		}
		for i := range ids {
			ids[i] = spec.ParticipantProgramID
		}
	case 2:
		if spec.ParticipantCount != 2 || len(spec.ParticipantProgramIDs) != 2 || spec.ParticipantProgramID != "" {
			return nil, fmt.Errorf("communication-v2 work measurement requires two participant_program_ids")
		}
		copy(ids, spec.ParticipantProgramIDs)
	default:
		return nil, fmt.Errorf("unsupported communication version for work measurement")
	}
	programs := make(map[string]model.RunProgram, len(req.Programs))
	for _, program := range req.Programs {
		if program.ID == "" {
			return nil, fmt.Errorf("work measurement program id is required")
		}
		if _, duplicate := programs[program.ID]; duplicate {
			return nil, fmt.Errorf("duplicate work measurement program id: %s", program.ID)
		}
		programs[program.ID] = program
	}
	roles := make([]workMeasurementRole, 0, len(ids)+1)
	for index, id := range ids {
		program, exists := programs[id]
		if !exists || id == spec.ManagerProgramID {
			return nil, fmt.Errorf("unknown or conflicting work measurement participant program: %s", id)
		}
		seat := index
		roles = append(roles, workMeasurementRole{role: "participant", programID: id, participantIndex: &seat, language: program.Lang})
	}
	manager, exists := programs[spec.ManagerProgramID]
	if !exists {
		return nil, fmt.Errorf("unknown work measurement manager program: %s", spec.ManagerProgramID)
	}
	return append(roles, workMeasurementRole{role: "manager", programID: manager.ID, language: manager.Lang}), nil
}

func validateWorkMeasurementRequest(req *model.RunRequest, cfg durante.Config) error {
	roles, err := workMeasurementRequestRoles(req)
	if err != nil {
		return err
	}
	for _, role := range roles {
		if _, err := cfg.Family(role.language); err != nil {
			return fmt.Errorf("work measurement %s %q: %w", role.role, role.programID, err)
		}
	}
	return nil
}

func validateRemotePeerWorkMeasurements(req *model.RunRequest, response model.RunResponse) error {
	peerRequest := req.Interactor != nil || req.Communication != nil
	if !req.WorkMeasurement || !peerRequest {
		if len(response.WorkMeasurements) != 0 {
			return fmt.Errorf("invalid remote result: unsolicited per-role work measurements")
		}
		return nil
	}
	if req.WorkMeasurementLimits != nil || response.WorkMeasurement != nil || response.WorkMeasurementExecution != nil {
		return fmt.Errorf("invalid remote result: per-role work measurement cannot use an ordinary report or resource envelope")
	}
	if len(response.WorkMeasurements) == 0 && response.Status == model.RunStatusInitFail {
		return nil // Admission or preparation failed before any target started.
	}
	roles, err := workMeasurementRequestRoles(req)
	if err != nil {
		return fmt.Errorf("invalid remote work measurement request: %w", err)
	}
	if len(response.WorkMeasurements) != len(roles) {
		return fmt.Errorf("invalid remote result: missing per-role work measurements")
	}
	for i, expected := range roles {
		observed := response.WorkMeasurements[i]
		if observed.Role != expected.role || observed.ProgramID != expected.programID ||
			(observed.ParticipantIndex == nil) != (expected.participantIndex == nil) ||
			(observed.ParticipantIndex != nil && *observed.ParticipantIndex != *expected.participantIndex) {
			return fmt.Errorf("invalid remote result: work measurement role identity mismatch")
		}
		switch observed.ExecutionStatus {
		case "OK", model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusRE, model.RunStatusInitFail:
		default:
			return fmt.Errorf("invalid remote result: unknown work measurement execution status")
		}
		family, known := durante.FamilyForLanguage(expected.language)
		if !known || durante.ValidateMeasurementFamily(family) != nil {
			return fmt.Errorf("invalid remote result: unsupported work measurement role language")
		}
		report := observed.Report
		if report == nil {
			if observed.ExecutionStatus == model.RunStatusInitFail && observed.ExitCode == nil {
				continue
			}
			return fmt.Errorf("invalid remote result: absent per-role diagnostic report")
		}
		if report.Schema != 1 || report.ClientVersion != 1 || !report.ValidDiagnosticContract() || report.Authority != durante.Authority || report.Metric != durante.Metric || report.Scope != durante.Scope || report.Family != family {
			return fmt.Errorf("invalid remote result: absent or incompatible per-role diagnostic contract")
		}
		if !report.Complete {
			if !report.ValidUnavailableResult() {
				return fmt.Errorf("invalid remote result: inconsistent unavailable per-role observation")
			}
			continue
		}
		if !validDiagnosticWorkReport(expected.language, report) {
			return fmt.Errorf("invalid remote result: inconsistent per-role diagnostic observation")
		}
		if observed.ExecutionStatus != "OK" || observed.ExitCode == nil || *observed.ExitCode < 0 || observed.TerminationSource != "" {
			return fmt.Errorf("invalid remote result: interrupted role returned complete diagnostic totals")
		}
	}
	return nil
}
