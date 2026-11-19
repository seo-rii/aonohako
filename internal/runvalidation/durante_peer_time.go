package runvalidation

import (
	"fmt"

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
	if (req.Interactor == nil) == (req.Communication == nil) || req.Pipeline != nil || req.SPJ != nil || len(req.Steps) != 0 || req.EntryMode != "" {
		return fmt.Errorf("work_measurement_peer_time requires an interactive or communication run")
	}
	request := req.WorkMeasurementPeerTime
	if (request.Version != 1 && request.Version != 2) || len(request.Budgets) < 2 || len(request.Budgets) > MaxCommunicationParticipants+1 {
		return fmt.Errorf("work_measurement_peer_time requires version 1 or 2 and a budget for every role")
	}
	type key struct {
		role, program string
		participant   bool
		seat          int
	}
	seen := make(map[key]bool, len(request.Budgets))
	for _, budget := range request.Budgets {
		if budget.ModelBinding != "" && (request.Version != 2 || len(budget.ModelBinding) > 128) {
			return fmt.Errorf("invalid peer clock model_binding")
		}
		if budget.ProgramID == "" || budget.TimeMs < 1 || budget.TimeMs > 600000 {
			return fmt.Errorf("work_measurement_peer_time requires program_id and time_ms between 1 and 600000")
		}
		identity := key{role: budget.Role, program: budget.ProgramID}
		switch budget.Role {
		case "participant":
			if budget.ParticipantIndex == nil || *budget.ParticipantIndex < 0 || *budget.ParticipantIndex >= MaxCommunicationParticipants {
				return fmt.Errorf("work_measurement_peer_time participant requires a valid participant_index")
			}
			identity.participant, identity.seat = true, *budget.ParticipantIndex
		case "contestant", "interactor", "manager":
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
