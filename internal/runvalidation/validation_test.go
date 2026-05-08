package runvalidation

import (
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestValidateStepPipelineRejectsAPILevelDriftCases(t *testing.T) {
	valid := func() *model.RunRequest {
		return &model.RunRequest{
			Programs: []model.RunProgram{
				{
					ID:   "encode",
					Lang: "binary",
					Binaries: []model.Binary{{
						Name:    "encode.sh",
						DataB64: "ZWNobw==",
						Mode:    "exec",
					}},
				},
				{
					ID:   "decode",
					Lang: "binary",
					Binaries: []model.Binary{{
						Name:    "decode.sh",
						DataB64: "ZWNobw==",
						Mode:    "exec",
					}},
				},
			},
			Steps: []model.RunStep{
				{
					ID:        "encode",
					ProgramID: "encode",
					Stdin:     "input\n",
					Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
					Handoff:   &model.StepHandoff{ID: "encoded", From: "stdout", MaxBytes: 1024},
				},
				{
					ID:        "decode",
					ProgramID: "decode",
					StdinFrom: "encoded",
					Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
				},
			},
		}
	}

	tests := []struct {
		name string
		edit func(*model.RunRequest)
		want string
	}{
		{
			name: "legacy fields mixed with steps",
			edit: func(req *model.RunRequest) {
				req.Lang = "binary"
			},
			want: "legacy execute fields",
		},
		{
			name: "missing program language",
			edit: func(req *model.RunRequest) {
				req.Programs[0].Lang = ""
			},
			want: "program encode lang is required",
		},
		{
			name: "oversized step stdin",
			edit: func(req *model.RunRequest) {
				req.Steps[0].Stdin = strings.Repeat("x", MaxTextFieldBytes+1)
			},
			want: "stdin too large",
		},
		{
			name: "workspace limit too high",
			edit: func(req *model.RunRequest) {
				req.Steps[0].Limits.WorkspaceBytes = MaxWorkspaceBytes + 1
			},
			want: "workspace_bytes",
		},
		{
			name: "handoff limit too high",
			edit: func(req *model.RunRequest) {
				req.Steps[0].Handoff.MaxBytes = MaxStepHandoffBytes + 1
			},
			want: "handoff.max_bytes",
		},
		{
			name: "second step handoff",
			edit: func(req *model.RunRequest) {
				req.Steps[1].Handoff = &model.StepHandoff{ID: "next"}
			},
			want: "second step handoff is not supported",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := valid()
			tc.edit(req)
			err := ValidateStepPipeline(req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateStepPipeline error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateRunRequestCoversLegacyAndStepModes(t *testing.T) {
	legacy := &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: "ZWNobw==",
			Mode:    "exec",
		}},
		Stdin:  "input\n",
		Limits: model.Limits{TimeMs: 1000, MemoryMB: 128},
	}
	if err := Validate(legacy); err != nil {
		t.Fatalf("legacy request should validate: %v", err)
	}

	legacy.Stdin = strings.Repeat("x", MaxTextFieldBytes+1)
	if err := Validate(legacy); err == nil || !strings.Contains(err.Error(), "stdin too large") {
		t.Fatalf("oversized legacy stdin error = %v", err)
	}
}
