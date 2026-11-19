package execute

import (
	"context"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// Per-request state follows the contestant and its single checker invocation.
// A contestant failure leaves the checker unstarted, without a counter or time.
type spjWorkClocks struct {
	config      durante.Config
	checkerPlan peerTimePlan
	contestant  model.WorkMeasurementProcess
	checker     model.WorkMeasurementProcess
}

func (s *Service) runSPJClockJudgment(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig, peerPlans map[peerTimeIdentity]peerTimePlan) model.RunResponse {
	raw := *req
	raw.IgnoreTLE = false
	applyPeerWorkClock(&raw, peerPlans[peerTimeKey("contestant", "contestant", nil)])
	clocks := spjWorkClocks{config: s.workMeasurement, checkerPlan: peerPlans[peerTimeKey("checker", "checker", nil)],
		checker: model.WorkMeasurementProcess{Role: "checker", ProgramID: "checker", ExecutionStatus: model.RunStatusInitFail, TerminationSource: "spj_checker_not_started"}}
	response := s.runProgram(ctx, &raw, nil, 0, hooks, tuning, true, false, &clocks).response
	if clocks.contestant.Role != "" {
		response.WorkMeasurement = nil
		response.WorkMeasurements = []model.WorkMeasurementProcess{clocks.contestant, clocks.checker}
		if response.VerdictSource != "spj" && !strings.HasPrefix(response.VerdictSource, "checker:") {
			response.VerdictSource = "contestant:" + response.VerdictSource
		}
	}
	return response
}
