package runvalidation

import (
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func peerTimeRequestFixture() *model.RunRequest {
	return &model.RunRequest{WorkMeasurement: true, Lang: "CPP20", Interactor: &model.InteractorSpec{Lang: "CPP20"},
		WorkMeasurementPeerTime: &model.PeerTimeRequest{Version: 1, Budgets: []model.PeerTimeBudget{
			{Role: "contestant", ProgramID: "contestant", TimeMs: 1},
			{Role: "interactor", ProgramID: "interactor", TimeMs: 600000},
		}}}
}

func TestWorkMeasurementPeerTimeRequiresExplicitPeerBudgets(t *testing.T) {
	if err := ValidateWorkMeasurementLimits(peerTimeRequestFixture()); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*model.RunRequest){
		"missing flag":      func(r *model.RunRequest) { r.WorkMeasurement = false },
		"missing version":   func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Version = 0 },
		"unknown version":   func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Version = 2 },
		"ordinary":          func(r *model.RunRequest) { r.Interactor = nil },
		"both peer modes":   func(r *model.RunRequest) { r.Communication = &model.CommunicationSpec{} },
		"ordinary time":     func(r *model.RunRequest) { r.WorkMeasurementTime = true },
		"resource envelope": func(r *model.RunRequest) { r.WorkMeasurementLimits = &durante.ResourceLimits{} },
		"pipeline":          func(r *model.RunRequest) { r.Pipeline = &model.PipelineV1{} },
		"special judge":     func(r *model.RunRequest) { r.SPJ = &model.SPJSpec{} },
		"entry mode":        func(r *model.RunRequest) { r.EntryMode = "manual" },
		"missing budgets":   func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets = nil },
		"missing role":      func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets = r.WorkMeasurementPeerTime.Budgets[:1] },
		"duplicate role":    func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[1] = r.WorkMeasurementPeerTime.Budgets[0] },
		"unknown role":      func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].Role = "solver" },
		"missing program":   func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].ProgramID = "" },
		"zero budget":       func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].TimeMs = 0 },
		"negative budget":   func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].TimeMs = -1 },
		"oversize budget":   func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].TimeMs = 600001 },
		"missing seat":      func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].Role = "participant" },
		"unexpected seat":   func(r *model.RunRequest) { n := 0; r.WorkMeasurementPeerTime.Budgets[0].ParticipantIndex = &n },
	} {
		t.Run(name, func(t *testing.T) {
			r := peerTimeRequestFixture()
			edit(r)
			if err := ValidateWorkMeasurementLimits(r); err == nil {
				t.Fatal("invalid per-role diagnostic budget admitted")
			}
		})
	}
}

func TestWorkMeasurementPeerTimeSeatsCompareValues(t *testing.T) {
	r := peerTimeRequestFixture()
	r.Interactor = nil
	r.Communication = &model.CommunicationSpec{Version: 1, ParticipantCount: 2, ParticipantProgramID: "same", ManagerProgramID: "judge"}
	first, second := 0, 1
	r.WorkMeasurementPeerTime.Budgets = []model.PeerTimeBudget{
		{Role: "participant", ProgramID: "same", ParticipantIndex: &first, TimeMs: 1},
		{Role: "participant", ProgramID: "same", ParticipantIndex: &second, TimeMs: 2},
		{Role: "manager", ProgramID: "judge", TimeMs: 3},
	}
	if err := ValidateWorkMeasurementLimits(r); err != nil {
		t.Fatal(err)
	}
	second = 0
	if err := ValidateWorkMeasurementLimits(r); err == nil {
		t.Fatal("duplicate seat values with different pointer addresses admitted")
	}
	for _, value := range []int{-1, MaxCommunicationParticipants} {
		second = value
		if err := ValidateWorkMeasurementLimits(r); err == nil {
			t.Fatal("invalid participant index admitted")
		}
	}
	r.WorkMeasurementPeerTime = nil
	if err := ValidateWorkMeasurementLimits(r); err != nil {
		t.Fatal("unselected peer-time option changed previous admission", err)
	}
}
