package execute

import (
	"fmt"
	"reflect"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

type peerTimeIdentity struct {
	role, programID string
	hasSeat         bool
	seat            int
}

func peerTimeKey(role, program string, seat *int) peerTimeIdentity {
	key := peerTimeIdentity{role: role, programID: program, hasSeat: seat != nil}
	if seat != nil {
		key.seat = *seat
	}
	return key
}

type peerTimePlan struct {
	role      workMeasurementRole
	budgetMs  int
	policy    durante.TimePolicy
	runtimeID string
	clock     *durante.WorkClockRequest
	rawLimits durante.ResourceLimits
}

// Resolve every role before execution or remote upload. Budget array order has
// no meaning; communication seats remain distinct even for the same program.
func workMeasurementPeerTimePlans(req *model.RunRequest, cfg durante.Config, runtimeTuning ...config.RuntimeTuningConfig) (map[peerTimeIdentity]peerTimePlan, error) {
	if req == nil || req.WorkMeasurementPeerTime == nil {
		return nil, nil
	}
	if err := runvalidation.ValidateWorkMeasurementPeerTime(req); err != nil {
		return nil, err
	}
	roles, err := workMeasurementRequestRoles(req)
	if err != nil {
		return nil, err
	}
	if len(req.WorkMeasurementPeerTime.Budgets) != len(roles) {
		return nil, fmt.Errorf("work_measurement_peer_time requires exactly one budget for every role")
	}
	budgets := make(map[peerTimeIdentity]model.PeerTimeBudget, len(roles))
	for _, budget := range req.WorkMeasurementPeerTime.Budgets {
		budgets[peerTimeKey(budget.Role, budget.ProgramID, budget.ParticipantIndex)] = budget
	}
	plans := make(map[peerTimeIdentity]peerTimePlan, len(roles))
	for _, role := range roles {
		key := peerTimeKey(role.role, role.programID, role.participantIndex)
		budget, found := budgets[key]
		if !found {
			return nil, fmt.Errorf("work_measurement_peer_time is missing a budget for %s %q", role.role, role.programID)
		}
		roleReq, err := peerRuntimeRequest(req, role)
		if err != nil {
			return nil, err
		}
		policy, runtimeID, err := peerTimePolicy(role, roleReq, cfg, optionalRuntimeTuning(runtimeTuning))
		if err != nil {
			return nil, fmt.Errorf("work_measurement_peer_time %s %q: %w", role.role, role.programID, err)
		}
		plan := peerTimePlan{role: role, budgetMs: budget.TimeMs, policy: policy, runtimeID: runtimeID}
		if req.WorkMeasurementPeerTime.Version == 2 {
			if err := plan.enableClock(req, roleReq, cfg, budget); err != nil {
				return nil, fmt.Errorf("peer clock %s %q: %w", role.role, role.programID, err)
			}
		}
		plans[key] = plan
	}
	return plans, nil
}

func evaluatePeerTimeBudget(plan peerTimePlan, observed model.WorkMeasurementProcess) *model.PeerTimeBudgetResult {
	result := &model.PeerTimeBudgetResult{Version: 1, Authority: durante.Authority, BudgetMs: plan.budgetMs}
	if observed.ExecutionStatus != "OK" || observed.ExitCode == nil || *observed.ExitCode < 0 || observed.TerminationSource != "" {
		result.Unavailable = "execution_not_complete"
		return result
	}
	if observed.Report == nil {
		result.Unavailable = "observation_unavailable"
		return result
	}
	if !validDiagnosticWorkReport(plan.role.language, observed.Report) {
		result.Unavailable = "incompatible_observation"
		return result
	}
	// Current peers must preserve the observed MOVS/STOS split. Absence is not
	// an observed zero partition, even when the selected model uses total REP.
	if observed.Report.REPBreakdown == nil {
		result.Unavailable = "observation_not_convertible"
		return result
	}
	estimate, err := plan.policy.Estimate(plan.runtimeID, *observed.Report)
	if err != nil {
		result.Unavailable = "observation_not_convertible"
		return result
	}
	exceeded := estimate.Nanoseconds > int64(plan.budgetMs)*1_000_000
	result.Estimate, result.Exceeded = &estimate, &exceeded
	return result
}

func finishWorkMeasurementPeerTime(req *model.RunRequest, response model.RunResponse, cfg durante.Config, runtimeTuning ...config.RuntimeTuningConfig) model.RunResponse {
	if req == nil || req.WorkMeasurementPeerTime == nil || len(response.WorkMeasurements) == 0 {
		return response
	}
	plans, err := workMeasurementPeerTimePlans(req, cfg, runtimeTuning...)
	if err != nil {
		return response // Admission rejects this before any execution starts.
	}
	response.WorkMeasurements = append([]model.WorkMeasurementProcess(nil), response.WorkMeasurements...)
	for i, observed := range response.WorkMeasurements {
		plan, found := plans[peerTimeKey(observed.Role, observed.ProgramID, observed.ParticipantIndex)]
		if found {
			if plan.clock != nil {
				result, err := peerClockResult(plan, observed)
				if err != nil {
					return model.RunResponse{Status: model.RunStatusInitFail, VerdictSource: "durante_peer_clock_error", Reason: "inconsistent role clock evidence: " + err.Error()}
				}
				response.WorkMeasurements[i].TimeBudget = result
			} else {
				response.WorkMeasurements[i].TimeBudget = evaluatePeerTimeBudget(plan, observed)
			}
		}
	}
	if req.WorkMeasurementPeerTime.Version == 2 {
		if err := validatePeerClockOutcome(req, response); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, VerdictSource: "durante_peer_clock_error", Reason: "inconsistent peer clock verdict: " + err.Error()}
		}
	}
	return response
}

func validatePeerClockOutcome(req *model.RunRequest, response model.RunResponse) error {
	if response.CPUTimeNormalization != nil {
		return fmt.Errorf("invalid peer clock result: normalized CPU accounting")
	}
	if spjPeerClocksRequested(req) {
		return validateSPJPeerClockVerdict(req, response)
	}
	if req.Interactor != nil {
		return validateInteractivePeerClockVerdict(response)
	}
	return validateCommunicationPeerClockVerdict(req, response)
}

func validateRemoteWorkMeasurementPeerTime(req *model.RunRequest, response model.RunResponse, cfg durante.Config, runtimeTuning ...config.RuntimeTuningConfig) error {
	if req.WorkMeasurementPeerTime == nil {
		for _, role := range response.WorkMeasurements {
			if role.TimeBudget != nil {
				return fmt.Errorf("invalid remote result: unsolicited per-role time budget")
			}
		}
		return nil
	}
	plans, err := workMeasurementPeerTimePlans(req, cfg, runtimeTuning...)
	if err != nil {
		return fmt.Errorf("invalid remote result: incompatible per-role time configuration")
	}
	if len(response.WorkMeasurements) == 0 && response.Status == model.RunStatusInitFail {
		if req.WorkMeasurementPeerTime.Version == 2 && (response.Score != nil || response.StartedParticipants != 0 ||
			(response.Match != nil && (response.Match.Outcome != "judge_error" || response.Match.Wins != nil))) {
			return fmt.Errorf("invalid remote result: missing clock evidence after peer execution")
		}
		return nil // Admission or preparation failed before targets started.
	}
	if len(response.WorkMeasurements) != len(plans) {
		return fmt.Errorf("invalid remote result: missing per-role time budgets")
	}
	for _, observed := range response.WorkMeasurements {
		plan, found := plans[peerTimeKey(observed.Role, observed.ProgramID, observed.ParticipantIndex)]
		if !found || observed.TimeBudget == nil {
			return fmt.Errorf("invalid remote result: missing per-role time budget")
		}
		var want *model.PeerTimeBudgetResult
		if plan.clock != nil {
			want, err = peerClockResult(plan, observed)
			if err != nil {
				return fmt.Errorf("invalid remote result: %w", err)
			}
		} else {
			want = evaluatePeerTimeBudget(plan, observed)
		}
		if !reflect.DeepEqual(observed.TimeBudget, want) {
			return fmt.Errorf("invalid remote result: per-role time budget mismatch")
		}
	}
	if req.WorkMeasurementPeerTime.Version == 2 {
		return validatePeerClockOutcome(req, response)
	}
	return nil
}
