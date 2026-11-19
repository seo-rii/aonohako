package execute

import (
	"context"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestPipelineClocksPreserveUnstartedRolesOnPreparationFailure(t *testing.T) {
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	req := syntheticInteractiveBatchPipeline(4096)
	pipeline := req.Pipeline
	for i := range pipeline.Programs {
		pipeline.Programs[i].Lang = "PYTHON3"
	}
	pipeline.FinalJudge.SPJ.Lang = "CPP20"
	roles, err := stageWorkMeasurementRoles(req)
	if err != nil {
		t.Fatal(err)
	}
	plans := make(map[peerTimeIdentity]peerTimePlan, len(roles))
	for i, role := range roles {
		plans[peerTimeKey(role.role, role.programID, nil, role.stepID)] = peerTimePlan{role: role, order: i}
	}
	clocks := newStagedWorkClocks(plans, durante.Config{})
	service := New()
	tuning := config.DefaultRuntimeTuningConfig()
	// The unavailable meter stops preparation before either peer starts.
	result := service.runPipelineV1Private(context.Background(), req, tuning, clocks)
	if len(result.Steps) != 1 || result.Steps[0].ID != "phase1" || result.Steps[0].HandoffBytes != 0 ||
		result.Status != model.RunStatusInitFail || result.VerdictSource != "step:phase1" ||
		!strings.Contains(result.Reason, "durante preparation failed") || result.Score != nil {
		t.Fatalf("unavailable interactive stage advanced or was scored: %+v", result)
	}
	for i, row := range clocks.rows {
		if row.Role != roles[i].role || row.StepID != roles[i].stepID || row.ProgramID != roles[i].programID ||
			row.TerminationSource != "stage_not_started" || row.ExitCode != nil || row.Report != nil {
			t.Fatalf("unstarted role %d received evidence: %+v", i, row)
		}
	}

	// A checker that cannot be prepared also lacks execution evidence and a score.
	plan, err := compileExecutionPlan(req)
	if err != nil {
		t.Fatal(err)
	}
	result = service.runPipelineFinalJudge(context.Background(), req, plan.finalJudge, []byte("private handoff"), tuning, clocks)
	if result.Status != model.RunStatusInitFail || result.VerdictSource != "final:checker:spj" || result.Score != nil {
		t.Fatalf("unavailable checker became a contestant verdict: %+v", result)
	}
	checker := clocks.rows[len(clocks.rows)-1]
	if checker.Role != "checker" || checker.TerminationSource != "stage_not_started" || checker.Report != nil {
		t.Fatalf("unstarted checker received evidence: %+v", checker)
	}
	public := RedactPipelineResponse(result)
	if public.Reason != "pipeline initialization failed" {
		t.Fatalf("private checker diagnostics were exposed: %q", public.Reason)
	}
}
