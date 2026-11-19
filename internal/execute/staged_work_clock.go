package execute

import (
	"context"
	"errors"
	"io"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// A stage can fail during input preparation, before a process starts. Keep the
// planned roles in execution order so unstarted roles never inherit an earlier
// run's counters, even when the same program appears in several steps.
type stagedWorkClocks struct {
	err    error
	plans  map[peerTimeIdentity]peerTimePlan
	rows   []model.WorkMeasurementProcess
	config durante.Config
}

func newStagedWorkClocks(plans map[peerTimeIdentity]peerTimePlan, cfg durante.Config) *stagedWorkClocks {
	c := &stagedWorkClocks{plans: plans, rows: make([]model.WorkMeasurementProcess, len(plans)), config: cfg}
	for _, p := range plans {
		c.rows[p.order] = model.WorkMeasurementProcess{StepID: p.role.stepID, Role: p.role.role,
			ProgramID: p.role.programID, ExecutionStatus: model.RunStatusInitFail, TerminationSource: "stage_not_started"}
	}
	return c
}

func (c *stagedWorkClocks) record(stepID, role, program string, row model.WorkMeasurementProcess) {
	if c == nil || row.Role == "" || row.TerminationSource == "spj_checker_not_started" {
		return
	}
	p, ok := c.plans[peerTimeKey(role, program, nil, stepID)]
	if !ok || c.rows[p.order].TerminationSource != "stage_not_started" || row.Role != role {
		c.err = errors.New("inconsistent stage work clock identity")
		return
	}
	row.StepID, row.Role, row.ProgramID = stepID, role, program
	c.rows[p.order] = row
}

func (c *stagedWorkClocks) checkerClocks() *spjWorkClocks {
	if c == nil {
		return nil
	}
	return &spjWorkClocks{config: c.config, checkerPlan: stageRolePlan(c.plans, "", "checker", "checker")}
}

func (c *stagedWorkClocks) interactivePlans(req *model.RunRequest, stepID, contestant, interactor string) map[peerTimeIdentity]peerTimePlan {
	if c == nil {
		return nil
	}
	req.WorkMeasurement = true
	req.WorkMeasurementPeerTime = &model.PeerTimeRequest{Version: 2}
	return map[peerTimeIdentity]peerTimePlan{
		peerTimeKey("contestant", "contestant", nil): stageRolePlan(c.plans, stepID, "contestant", contestant),
		peerTimeKey("interactor", "interactor", nil): stageRolePlan(c.plans, stepID, "interactor", interactor),
	}
}

func (s *Service) runClockedStage(ctx context.Context, req *model.RunRequest, stdin io.Reader, stdinMaxBytes int64, hooks Hooks,
	tuning config.RuntimeTuningConfig, evaluateOutput, captureRawStdout bool, clocks *stagedWorkClocks, stepID, program string) sandboxRunResult {
	if clocks == nil {
		return s.runOneWithStdin(ctx, req, stdin, stdinMaxBytes, hooks, tuning, evaluateOutput, captureRawStdout)
	}
	raw := *req
	raw.WorkMeasurement = true
	applyPeerWorkClock(&raw, stageRolePlan(clocks.plans, stepID, "contestant", program))
	state := &spjWorkClocks{}
	if raw.SPJ != nil {
		state = clocks.checkerClocks()
	}
	result := s.runProgram(ctx, &raw, stdin, stdinMaxBytes, hooks, tuning, evaluateOutput, captureRawStdout, state)
	clocks.record(stepID, "contestant", program, state.contestant)
	clocks.record("", "checker", "checker", state.checker)
	result.response.WorkMeasurement = nil
	return result
}

// Sequential stages reuse a reservation. Within an interactive stage the two
// raw memory allowances must fit at the same time.
func peakPeerMemoryMB(req *model.RunRequest, plans map[peerTimeIdentity]peerTimePlan) int {
	concurrent := make(map[string]int)
	peak := 0
	for _, p := range plans {
		group := p.role.stepID
		if req.SPJ != nil && !stagedPeerClocksRequested(req) {
			group = p.role.role
		}
		concurrent[group] += p.rawLimits.MemoryMB
		peak = max(peak, concurrent[group])
	}
	return peak
}
