package execute

import (
	"testing"

	"aonohako/internal/model"
)

// The worker and the HTTP client reconstruct the coordinator decision from the
// same role evidence. Both the attached steps and score must agree with it.
func TestInteractivePeerClockVerdictChecksRolesAndUsage(t *testing.T) {
	zero, cpu, wall, memory := 0, uint64(3500), int64(12), int64(48)
	one := 1.0
	fresh := func() model.RunResponse {
		var rows []model.WorkMeasurementProcess
		var steps []model.StepResult
		for _, name := range []string{"contestant", "interactor"} {
			source := "contestant"
			if name == "interactor" {
				source = "interactor:exit_code"
			}
			rows = append(rows, model.WorkMeasurementProcess{Role: name, ProgramID: name, ExecutionStatus: "OK", ExitCode: &zero,
				TimeBudget: &model.PeerTimeBudgetResult{RawUsage: &model.PeerClockUsage{CPUTimeNs: &cpu, WallTimeMs: &wall, MemoryKB: &memory}}})
			steps = append(steps, model.StepResult{ID: name, ProgramID: name, Status: model.RunStatusAccepted,
				VerdictSource: source, ExitCode: &zero, CPUTimeNs: &cpu, WallTimeMs: wall, MemoryKB: memory})
		}
		return model.RunResponse{Status: model.RunStatusAccepted, VerdictSource: "interactor", Score: &one, Steps: steps, WorkMeasurements: rows}
	}
	if err := validateInteractivePeerClockVerdict(fresh()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*model.RunResponse){
		func(r *model.RunResponse) { r.Status = model.RunStatusTLE },
		func(r *model.RunResponse) { r.Score = nil },
		func(r *model.RunResponse) { r.Steps[1].MemoryKB++ },
		func(r *model.RunResponse) { r.Steps[0].WallTimeMs++ },
		func(r *model.RunResponse) { r.Steps[0].ExitCode = nil },
		func(r *model.RunResponse) { r.Steps[1].VerdictSource = "contestant:exit_code" },
	} {
		r := fresh()
		change(&r)
		if err := validateInteractivePeerClockVerdict(r); err == nil {
			t.Errorf("accepted inconsistent clock verdict: %+v", r)
		}
	}
}
