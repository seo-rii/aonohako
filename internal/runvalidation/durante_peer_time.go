package runvalidation

import (
	"fmt"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// ValidateWorkMeasurementPeerTime checks request shape. The execution layer
// additionally binds every budget to the canonical role and operator model.
func ValidateWorkMeasurementPeerTime(req *model.RunRequest) error {
	if req == nil || req.WorkMeasurementPeerTime == nil {
		return nil
	}
	if !req.WorkMeasurement {
		return fmt.Errorf("work_measurement_peer_time requires work_measurement=true")
	}
	if req.WorkMeasurementTime || req.WorkMeasurementLimits != nil {
		return fmt.Errorf("work_measurement_peer_time cannot use ordinary time conversion or resource envelopes")
	}
	request := req.WorkMeasurementPeerTime
	staged := req.Pipeline != nil || len(req.Steps) != 0
	if req.EntryMode != "" {
		return fmt.Errorf("work_measurement_peer_time requires standalone roles")
	}
	if staged {
		if request.Version != 2 || req.Interactor != nil || req.Communication != nil || req.IgnoreTLE || req.Pipeline != nil && (len(req.Steps) != 0 || req.SPJ != nil) {
			return fmt.Errorf("staged peer clocks require version 2 with ignore_tle off")
		}
	} else if req.SPJ != nil {
		if request.Version != 2 || req.Interactor != nil || req.Communication != nil || len(req.Programs) != 0 || req.IgnoreTLE {
			return fmt.Errorf("SPJ peer clocks require standalone version 2 with ignore_tle off")
		}
		for _, lang := range []string{req.Lang, req.SPJ.Lang} {
			if !durante.SupportsRuntimeOptions(lang) {
				return fmt.Errorf("SPJ peer clocks require registered contestant and checker source languages")
			}
		}
	} else if (req.Interactor == nil) == (req.Communication == nil) {
		return fmt.Errorf("work_measurement_peer_time requires an interactive or communication run or standalone SPJ")
	}
	minRoles, maxRoles := 2, MaxCommunicationParticipants+1
	if staged {
		minRoles, maxRoles = 1, 2*MaxPipelineSteps+1
	}
	if (request.Version != 1 && request.Version != 2) || len(request.Budgets) < minRoles || len(request.Budgets) > maxRoles {
		return fmt.Errorf("work_measurement_peer_time requires version 1 or 2 and a budget for every role")
	}
	type key struct {
		role, program, step string
		participant         bool
		seat                int
	}
	seen := make(map[key]bool, len(request.Budgets))
	for _, budget := range request.Budgets {
		if budget.ModelBinding != "" && (request.Version != 2 || len(budget.ModelBinding) > 128) {
			return fmt.Errorf("invalid peer clock model_binding")
		}
		if budget.ProgramID == "" || budget.TimeMs < 1 || budget.TimeMs > 600000 {
			return fmt.Errorf("work_measurement_peer_time requires program_id and time_ms between 1 and 600000")
		}
		if staged {
			switch budget.Role {
			case "contestant", "interactor":
				if budget.StepID == "" {
					return fmt.Errorf("stage clock requires step_id")
				}
			case "checker":
				if budget.StepID != "" || budget.ProgramID != "checker" {
					return fmt.Errorf("final checker requires program_id checker and no step_id")
				}
			default:
				return fmt.Errorf("unknown staged work clock role")
			}
		} else {
			if budget.StepID != "" {
				return fmt.Errorf("step_id requires a staged clock")
			}
			if req.SPJ != nil && ((budget.Role != "contestant" && budget.Role != "checker") || budget.ProgramID != budget.Role) || (req.SPJ == nil && budget.Role == "checker") {
				return fmt.Errorf("SPJ peer clock identities must be contestant and checker")
			}
		}
		identity := key{role: budget.Role, program: budget.ProgramID, step: budget.StepID}
		switch budget.Role {
		case "participant":
			if budget.ParticipantIndex == nil || *budget.ParticipantIndex < 0 || *budget.ParticipantIndex >= MaxCommunicationParticipants {
				return fmt.Errorf("work_measurement_peer_time participant requires a valid participant_index")
			}
			identity.participant, identity.seat = true, *budget.ParticipantIndex
		case "contestant", "interactor", "manager", "checker":
			if budget.ParticipantIndex != nil {
				return fmt.Errorf("work_measurement_peer_time non-participant cannot have participant_index")
			}
		default:
			return fmt.Errorf("work_measurement_peer_time has an unknown role")
		}
		if seen[identity] {
			return fmt.Errorf("work_measurement_peer_time contains a duplicate role budget")
		}
		seen[identity] = true
	}
	return nil
}
