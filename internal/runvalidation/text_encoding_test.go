package runvalidation

import (
	"encoding/base64"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestValidateRejectsInvalidUTF8Text(t *testing.T) {
	invalid := string([]byte{0xe9, '\n'})
	for _, tc := range []struct {
		field string
		make  func() *model.RunRequest
	}{
		{field: "stdin", make: func() *model.RunRequest {
			req := validTextEncodingRequest()
			req.Stdin = invalid
			return req
		}},
		{field: "expected_stdout", make: func() *model.RunRequest {
			req := validTextEncodingRequest()
			req.ExpectedStdout = invalid
			return req
		}},
		{field: "communication.input", make: func() *model.RunRequest {
			req := validCommunicationRequest()
			req.Communication.Input = invalid
			return req
		}},
		{field: "communication.answer", make: func() *model.RunRequest {
			req := validCommunicationRequest()
			req.Communication.Answer = invalid
			return req
		}},
		{field: "steps[0].stdin", make: func() *model.RunRequest {
			req := validTwoStepPipelineRequest()
			req.Steps[0].Stdin = invalid
			return req
		}},
		{field: "steps[0].stdin_parts[0].data", make: func() *model.RunRequest {
			req := validTwoStepPipelineRequest()
			req.Steps[0].StdinParts = []model.StdinPart{{Type: "text", Data: invalid}}
			return req
		}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			if err := Validate(tc.make()); err == nil || !strings.Contains(err.Error(), tc.field) || !strings.Contains(err.Error(), "valid UTF-8") {
				t.Fatalf("Validate() error = %v, want UTF-8 rejection for %s", err, tc.field)
			}
		})
	}
}

func TestValidateAllowsUTF8TextAndBinaryPayloads(t *testing.T) {
	req := validTextEncodingRequest()
	req.Stdin = "입력\x00\n"
	req.ExpectedStdout = "réponse\x00\n"
	req.Binaries[0].DataB64 = base64.StdEncoding.EncodeToString([]byte{0xe9, 0xff, 0})
	if err := Validate(req); err != nil {
		t.Fatalf("valid text and byte-preserving binary payload rejected: %v", err)
	}

	pipeline := validPipelineRequest()
	pipeline.Pipeline.Resources["answer"] = model.PipelineResource{DataB64: base64.StdEncoding.EncodeToString([]byte{0xe9, '\n'})}
	if err := Validate(pipeline); err != nil {
		t.Fatalf("byte-preserving pipeline answer rejected: %v", err)
	}
}

func validTextEncodingRequest() *model.RunRequest {
	return &model.RunRequest{
		Lang:     "binary",
		Binaries: []model.Binary{{Name: "program", DataB64: "eA==", Mode: "exec"}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}
}
