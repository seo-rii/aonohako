package execute

import (
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestSequentialClockLedgerReusesMemoryButNeverPriorRoleEvidence(t *testing.T) {
	req := &model.RunRequest{Pipeline: &model.PipelineV1{}, WorkMeasurementPeerTime: &model.PeerTimeRequest{Version: 2}}
	plans := make(map[peerTimeIdentity]peerTimePlan)
	add := func(order int, step, role, program string, memory int) {
		plans[peerTimeKey(role, program, nil, step)] = peerTimePlan{order: order,
			role:      workMeasurementRole{stepID: step, role: role, programID: program},
			rawLimits: durante.ResourceLimits{MemoryMB: memory}}
	}
	add(0, "first", "contestant", "solution", 128)
	add(1, "second", "contestant", "solution", 100)
	add(2, "second", "interactor", "interaction", 160)
	add(3, "", "checker", "checker", 192)
	if got := peakPeerMemoryMB(req, plans); got != 260 {
		t.Fatalf("peak concurrent memory = %d MB", got)
	}
	c := newStagedWorkClocks(plans, durante.Config{})
	c.record("first", "contestant", "solution", model.WorkMeasurementProcess{Role: "contestant", ExecutionStatus: model.RunStatusTLE, TerminationSource: "durante_work_clock"})
	if c.err != nil || c.rows[0].ProgramID != "solution" || c.rows[0].StepID != "first" || c.rows[0].ExecutionStatus != model.RunStatusTLE {
		t.Fatal("lost the first stage's clock cutoff")
	}
	for _, r := range c.rows[1:] {
		if r.TerminationSource != "stage_not_started" || r.Report != nil || r.TimeBudget != nil || r.ExitCode != nil {
			t.Fatalf("unstarted role acquired measured data: %+v", r)
		}
	}
	c.record("missing", "contestant", "solution", model.WorkMeasurementProcess{Role: "contestant"})
	if c.err == nil || c.rows[0].TerminationSource != "durante_work_clock" {
		t.Fatal("unknown identity overwrote an already observed role")
	}
}
