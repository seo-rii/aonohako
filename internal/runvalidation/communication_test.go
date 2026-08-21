package runvalidation

import (
	"strings"
	"testing"

	"aonohako/internal/model"
)

func validCommunicationRequest() *model.RunRequest {
	return &model.RunRequest{
		Programs: []model.RunProgram{
			{ID: "participant", Lang: "binary", Binaries: []model.Binary{{Name: "participant", DataB64: "eA==", Mode: "exec"}}},
			{ID: "manager", Lang: "binary", Binaries: []model.Binary{{Name: "manager", DataB64: "eA==", Mode: "exec"}}},
		},
		Communication: &model.CommunicationSpec{
			Version:              1,
			ParticipantProgramID: "participant",
			ManagerProgramID:     "manager",
			ParticipantCount:     64,
			ResultProtocol:       "manager-result-v1",
		},
		Limits: model.Limits{TimeMs: 1000, MemoryMB: 256},
	}
}

func TestValidateCommunicationContract(t *testing.T) {
	if err := Validate(validCommunicationRequest()); err != nil {
		t.Fatalf("valid communication request rejected: %v", err)
	}
	if UsesSteps(validCommunicationRequest()) {
		t.Fatal("communication programs must not be interpreted as a step pipeline")
	}

	tests := []struct {
		name string
		edit func(*model.RunRequest)
		want string
	}{
		{name: "version", edit: func(req *model.RunRequest) { req.Communication.Version = 2 }, want: "version must be 1"},
		{name: "minimum participants", edit: func(req *model.RunRequest) { req.Communication.ParticipantCount = 1 }, want: "between 2 and 64"},
		{name: "maximum participants", edit: func(req *model.RunRequest) { req.Communication.ParticipantCount = 65 }, want: "between 2 and 64"},
		{name: "protocol", edit: func(req *model.RunRequest) { req.Communication.ResultProtocol = "legacy" }, want: "manager-result-v1"},
		{name: "same program", edit: func(req *model.RunRequest) { req.Communication.ManagerProgramID = "participant" }, want: "must differ"},
		{name: "legacy interactor", edit: func(req *model.RunRequest) { req.Interactor = &model.InteractorSpec{} }, want: "cannot be combined"},
		{name: "legacy stdin", edit: func(req *model.RunRequest) { req.Stdin = "hidden" }, want: "legacy execute fields"},
		{name: "input url conflict", edit: func(req *model.RunRequest) {
			req.Communication.Input = "x"
			req.Communication.InputURL = "https://example.invalid/input"
		}, want: "cannot combine"},
		{name: "program network", edit: func(req *model.RunRequest) { req.Programs[0].EnableNetwork = true }, want: "cannot enable network"},
		{name: "managed runtime", edit: func(req *model.RunRequest) { req.Programs[0].Lang = "python" }, want: "native binary"},
		{name: "unreferenced program", edit: func(req *model.RunRequest) { req.Programs[1].ID = "other" }, want: "unreferenced program"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := validCommunicationRequest()
			tc.edit(req)
			err := Validate(req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate error = %v, want containing %q", err, tc.want)
			}
		})
	}
}
