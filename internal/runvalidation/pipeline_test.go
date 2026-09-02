package runvalidation

import (
	"encoding/base64"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func validPipelineRequest() *model.RunRequest {
	encoded := base64.StdEncoding.EncodeToString([]byte("payload\n"))
	limits := model.Limits{TimeMs: 1000, MemoryMB: 64}
	return &model.RunRequest{Pipeline: &model.PipelineV1{
		Version: 1,
		Resources: map[string]model.PipelineResource{
			"testcase": {DataB64: encoded},
			"answer":   {DataB64: encoded},
		},
		Programs: []model.RunProgram{
			{ID: "participant", Lang: "python", Binaries: []model.Binary{{Name: "main.py", DataB64: encoded}}},
			{ID: "interactor", Lang: "python", Binaries: []model.Binary{{Name: "interactor.py", DataB64: encoded}}},
		},
		Steps: []model.PipelineStep{
			{
				ID: "phase1",
				Executor: model.PipelineExecutor{
					Kind: "interactive", ParticipantProgramID: "participant", InteractorProgramID: "interactor",
					InteractorAnswer: &model.PipelineRef{Type: "resource", ID: "answer"},
				},
				Stdin:   []model.PipelineRef{{Type: "resource", ID: "testcase"}},
				Outputs: []model.PipelineOutput{{ID: "phase2-input", Source: model.PipelineOutputSource{Kind: "interactor_output"}, MaxBytes: 1024}},
				Limits:  limits,
			},
			{
				ID:       "phase2",
				Executor: model.PipelineExecutor{Kind: "batch", ProgramID: "participant"},
				Stdin:    []model.PipelineRef{{Type: "artifact", ID: "phase2-input"}},
				Limits:   limits,
			},
		},
		FinalJudge: model.PipelineFinalJudge{
			Kind:     "diff",
			Input:    model.PipelineRef{Type: "resource", ID: "testcase"},
			Expected: model.PipelineRef{Type: "resource", ID: "answer"},
			Actual:   model.PipelineRef{Type: "step_stdout", StepID: "phase2"},
		},
	}}
}

func TestValidatePipelineV1AllowsExplicitOriginalJudgeInput(t *testing.T) {
	if err := Validate(validPipelineRequest()); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidatePipelineV1RejectsForwardArtifactReference(t *testing.T) {
	req := validPipelineRequest()
	req.Pipeline.Steps[0].Stdin = []model.PipelineRef{{Type: "artifact", ID: "phase2-input"}}
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "earlier step") {
		t.Fatalf("Validate() error = %v, want forward-reference rejection", err)
	}
}

func TestValidatePipelineV1RejectsImplicitLegacyCombination(t *testing.T) {
	req := validPipelineRequest()
	req.Interactor = &model.InteractorSpec{}
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "legacy execute fields") {
		t.Fatalf("Validate() error = %v, want mixed-mode rejection", err)
	}
}

func TestValidatePipelineV1RejectsCommunicationCombination(t *testing.T) {
	req := validPipelineRequest()
	req.Communication = &model.CommunicationSpec{Version: 1}
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "cannot be combined with pipeline") {
		t.Fatalf("Validate() error = %v, want communication/pipeline rejection", err)
	}
}

func TestValidatePipelineV1RestrictsInteractorOutputToInteractiveStep(t *testing.T) {
	req := validPipelineRequest()
	req.Pipeline.Steps[1].Outputs = []model.PipelineOutput{{ID: "invalid", Source: model.PipelineOutputSource{Kind: "interactor_output"}, MaxBytes: 1024}}
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "requires an interactive step") {
		t.Fatalf("Validate() error = %v, want interactor output source rejection", err)
	}
}

func TestValidatePipelineV1RejectsUnknownInteractorAnswerResource(t *testing.T) {
	req := validPipelineRequest()
	req.Pipeline.Steps[0].Executor.InteractorAnswer.ID = "missing"
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "unknown resource") {
		t.Fatalf("Validate() error = %v, want interactor answer rejection", err)
	}
}

func TestValidatePipelineV1RejectsFinalJudgeSidecars(t *testing.T) {
	req := validPipelineRequest()
	req.Pipeline.FinalJudge.Kind = "spj"
	req.Pipeline.FinalJudge.SPJ = &model.SPJSpec{
		Lang:           "python",
		Binary:         &model.Binary{Name: "spj.py", DataB64: base64.StdEncoding.EncodeToString([]byte("pass\n"))},
		SidecarOutputs: []model.OutputFile{{Path: "images.jsonl"}},
	}
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "sidecar_outputs are not supported") {
		t.Fatalf("Validate() error = %v, want final judge sidecar rejection", err)
	}
}

func TestValidatePipelineV1RejectsExcessiveAggregateArtifactBudget(t *testing.T) {
	req := validPipelineRequest()
	req.Pipeline.Steps[0].Outputs = []model.PipelineOutput{
		{ID: "first", Source: model.PipelineOutputSource{Kind: "interactor_output"}, MaxBytes: MaxStepHandoffBytes},
		{ID: "second", Source: model.PipelineOutputSource{Kind: "participant_stdout"}, MaxBytes: MaxStepHandoffBytes},
		{ID: "third", Source: model.PipelineOutputSource{Kind: "participant_file", Path: "output.txt"}, MaxBytes: 1},
	}
	req.Pipeline.Steps[1].Stdin = []model.PipelineRef{{Type: "artifact", ID: "first"}}
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "artifact max_bytes total") {
		t.Fatalf("Validate() error = %v, want aggregate artifact budget rejection", err)
	}
}

func TestValidatePipelineV1RejectsNonCanonicalReferenceIDs(t *testing.T) {
	tests := []struct {
		name string
		edit func(*model.RunRequest)
	}{
		{
			name: "batch program",
			edit: func(req *model.RunRequest) { req.Pipeline.Steps[1].Executor.ProgramID = " participant " },
		},
		{
			name: "interactive participant",
			edit: func(req *model.RunRequest) { req.Pipeline.Steps[0].Executor.ParticipantProgramID = " participant " },
		},
		{
			name: "interactive interactor",
			edit: func(req *model.RunRequest) { req.Pipeline.Steps[0].Executor.InteractorProgramID = " interactor " },
		},
		{
			name: "interactor answer",
			edit: func(req *model.RunRequest) { req.Pipeline.Steps[0].Executor.InteractorAnswer.ID = " answer " },
		},
		{
			name: "resource stdin",
			edit: func(req *model.RunRequest) { req.Pipeline.Steps[0].Stdin[0].ID = " testcase " },
		},
		{
			name: "artifact stdin",
			edit: func(req *model.RunRequest) { req.Pipeline.Steps[1].Stdin[0].ID = " phase2-input " },
		},
		{
			name: "final input",
			edit: func(req *model.RunRequest) { req.Pipeline.FinalJudge.Input.ID = " testcase " },
		},
		{
			name: "final expected",
			edit: func(req *model.RunRequest) { req.Pipeline.FinalJudge.Expected.ID = " answer " },
		},
		{
			name: "final actual step",
			edit: func(req *model.RunRequest) { req.Pipeline.FinalJudge.Actual.StepID = " phase2 " },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := validPipelineRequest()
			tc.edit(req)
			if err := Validate(req); err == nil || !strings.Contains(err.Error(), "canonical") {
				t.Fatalf("Validate() error = %v, want non-canonical reference rejection", err)
			}
		})
	}
}

func TestValidatePipelineV1ParticipantFileLimitMatchesCaptureLimit(t *testing.T) {
	req := validPipelineRequest()
	req.Pipeline.Steps[0].Outputs = append(req.Pipeline.Steps[0].Outputs, model.PipelineOutput{
		ID:       "participant-file",
		Source:   model.PipelineOutputSource{Kind: "participant_file", Path: "handoff.bin"},
		MaxBytes: MaxPipelineParticipantFileBytes + 1,
	})
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "participant_file max_bytes") {
		t.Fatalf("Validate() error = %v, want participant file capture-limit rejection", err)
	}

	req.Pipeline.Steps[0].Outputs[len(req.Pipeline.Steps[0].Outputs)-1].MaxBytes = MaxPipelineParticipantFileBytes
	if err := Validate(req); err != nil {
		t.Fatalf("Validate() at participant file capture limit: %v", err)
	}
}
