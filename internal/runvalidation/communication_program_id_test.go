package runvalidation

import (
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestValidateCommunicationRejectsProgramIDWhitespace(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*model.RunRequest)
	}{
		{name: "participant spec id", edit: func(req *model.RunRequest) { req.Communication.ParticipantProgramID = " participant" }},
		{name: "manager spec id", edit: func(req *model.RunRequest) { req.Communication.ManagerProgramID = "manager " }},
		{name: "participant program id", edit: func(req *model.RunRequest) { req.Programs[0].ID = " participant" }},
		{name: "manager program id", edit: func(req *model.RunRequest) { req.Programs[1].ID = "manager " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validCommunicationRequest()
			tc.edit(req)
			err := Validate(req)
			if err == nil || !strings.Contains(err.Error(), "surrounding whitespace") {
				t.Fatalf("Validate() error = %v, want surrounding whitespace rejection", err)
			}
		})
	}
}
