package execute

import (
	"encoding/json"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestRemoteLegacyStepObservation(t *testing.T) {
	cfg := observerConfig()
	batch := observerRequest()
	req := &model.RunRequest{
		Programs: []model.RunProgram{{ID: "program", Lang: batch.Lang, Binaries: batch.Binaries}},
		Steps: []model.RunStep{
			{ID: "first", ProgramID: " program", Limits: batch.Limits, Handoff: &model.StepHandoff{ID: "text"}},
			{ID: "last", ProgramID: "program", Limits: batch.Limits, StdinFrom: "text"},
		},
	}
	if one := remoteObservationBudget(batch, cfg); one <= 0 || remoteObservationBudget(req, cfg) != 2*one {
		t.Fatal("deadline does not cover both eligible replays")
	}
	zero := 0
	report := completeEnvelopeReport()
	report.CPUIDProfile = cfg.CPUIDProfile
	primary := model.RunResponse{Status: model.RunStatusWA, VerdictSource: "step:last:stdout", Steps: []model.StepResult{
		{ID: "first", ProgramID: " program", Status: model.RunStatusAccepted, ExitCode: &zero, HandoffBytes: 5},
		{ID: "last", ProgramID: "program", Status: model.RunStatusWA, VerdictSource: "stdout", ExitCode: &zero},
	}}
	block := model.WorkMeasurementObservation{Version: 2, State: "steps", Authority: durante.Authority, Steps: []model.PipelineStepObservation{
		{StepID: "first", ProgramID: " program", State: "complete", OutputCheck: "native_output", Report: report},
		{StepID: "last", ProgramID: "program", State: "complete", OutputCheck: "native_output", Report: report},
	}}
	check := func(t *testing.T, primary model.RunResponse, block *model.WorkMeasurementObservation, states ...string) {
		t.Helper()
		raw, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		got := remoteObservation(req, primary, cfg, raw)
		if len(states) == 0 {
			if got.State != "unavailable" || got.Report != nil || len(got.Steps) > 0 {
				t.Fatalf("retained unbound work measurement: %+v", got)
			}
			return
		}
		if got.State != "steps" || len(got.Steps) != len(states) {
			t.Fatalf("unexpected receipt: %+v", got)
		}
		for i, state := range states {
			row := got.Steps[i]
			if row.State != state || (row.Report != nil) != (state == "complete") {
				t.Errorf("row %d: %+v", i, row)
			}
		}
	}

	check(t, primary, &block, "complete", "complete")
	check(t, primary, nil)
	check(t, primary, &model.WorkMeasurementObservation{Version: 1, State: "unavailable"})
	wrong := block
	wrong.Steps = []model.PipelineStepObservation{block.Steps[1], block.Steps[0]}
	check(t, primary, &wrong)
	wrong = block
	wrong.State = "pipeline"
	check(t, primary, &wrong)
	wrong = block
	wrong.Steps = append([]model.PipelineStepObservation(nil), block.Steps...)
	wrong.Steps[0].Reason = "unverified"
	check(t, primary, &wrong, "unavailable", "complete")
	partial := primary
	partial.Steps = partial.Steps[:1]
	prefix := block
	prefix.Steps = prefix.Steps[:1]
	check(t, partial, &prefix)
	partial.Status, partial.VerdictSource = model.RunStatusRE, "step:first:handoff"
	check(t, partial, &prefix, "unavailable")
	partial.Status, partial.VerdictSource = model.RunStatusInitFail, "unexpected"
	check(t, partial, &prefix)
	partial.VerdictSource = "step:last:handoff"
	check(t, partial, &prefix, "complete")
	primary.VerdictSource = "step:first:handoff"
	check(t, primary, &block)
	primary.VerdictSource = "step:last:stdout"
	exit := 1
	primary.Steps[0].ExitCode = &exit
	check(t, primary, &block)
	primary.Steps[0].ExitCode = &zero
	req.Steps[0].Handoff.MaxBytes = primary.Steps[0].HandoffBytes - 1
	check(t, primary, &block)
	req.Steps[0].Handoff.MaxBytes = 0
	req.SidecarOutputs = []model.OutputFile{{Path: "info"}}
	check(t, primary, &block, "complete", "unavailable")
	if one := remoteObservationBudget(batch, cfg); remoteObservationBudget(req, cfg) != one {
		t.Fatal("remote budget includes an ineligible final step")
	}
}
