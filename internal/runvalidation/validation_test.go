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

	partsReq := valid()
	partsReq.Steps[1].StdinFrom = ""
	partsReq.Steps[1].StdinParts = []model.StdinPart{
		{Type: "text", Data: "DECODE\n"},
		{Type: "handoff", ID: "encoded"},
	}
	if err := ValidateStepPipeline(partsReq); err != nil {
		t.Fatalf("stdin_parts request should validate: %v", err)
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
			name: "unsupported program language",
			edit: func(req *model.RunRequest) {
				req.Programs[0].Lang = "definitely-not-a-runtime"
			},
			want: "unsupported program encode lang",
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
		{
			name: "second step explicit stdin with stdin_from",
			edit: func(req *model.RunRequest) {
				req.Steps[1].Stdin = "ignored\n"
			},
			want: "stdin cannot be combined with stdin_from",
		},
		{
			name: "stdin parts mixed with stdin_from",
			edit: func(req *model.RunRequest) {
				req.Steps[1].StdinParts = []model.StdinPart{{Type: "handoff", ID: "encoded"}}
			},
			want: "stdin_parts cannot be combined",
		},
		{
			name: "first step handoff stdin part",
			edit: func(req *model.RunRequest) {
				req.Steps[0].Stdin = ""
				req.Steps[0].StdinParts = []model.StdinPart{{Type: "handoff", ID: "encoded"}}
			},
			want: "first step cannot use handoff stdin_part",
		},
		{
			name: "second step stdin parts without handoff",
			edit: func(req *model.RunRequest) {
				req.Steps[1].StdinFrom = ""
				req.Steps[1].StdinParts = []model.StdinPart{{Type: "text", Data: "DECODE\n"}}
			},
			want: "stdin_parts must reference",
		},
		{
			name: "unresolved stdin part data url",
			edit: func(req *model.RunRequest) {
				req.Steps[0].Stdin = ""
				req.Steps[0].StdinParts = []model.StdinPart{{Type: "text", DataURL: "https://example.invalid/stdin"}}
			},
			want: "data_url must be resolved",
		},
		{
			name: "duplicate program binary path",
			edit: func(req *model.RunRequest) {
				req.Programs[0].Binaries = append(req.Programs[0].Binaries, model.Binary{Name: "encode.sh", DataB64: "ZWNobw=="})
			},
			want: "duplicate binary path",
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

	legacy.Stdin = ""
	legacy.Binaries[0].DataB64 = "!!!!"
	if err := Validate(legacy); err == nil || !strings.Contains(err.Error(), "invalid base64") {
		t.Fatalf("invalid legacy binary base64 error = %v", err)
	}

	legacy.Binaries = []model.Binary{
		{Name: "run.sh", DataB64: "ZWNobw==", Mode: "exec"},
		{Name: "run.sh", DataB64: "ZWNobw==", Mode: "exec"},
	}
	if err := Validate(legacy); err == nil || !strings.Contains(err.Error(), "duplicate binary path") {
		t.Fatalf("duplicate legacy binary path error = %v", err)
	}

	legacy.Lang = "definitely-not-a-runtime"
	legacy.Binaries = []model.Binary{{Name: "run.sh", DataB64: "ZWNobw==", Mode: "exec"}}
	if err := Validate(legacy); err == nil || !strings.Contains(err.Error(), "unsupported lang") {
		t.Fatalf("unsupported legacy lang error = %v", err)
	}
}

func TestValidateInteractorRequestCoversInteractiveIOShape(t *testing.T) {
	valid := func() *model.RunRequest {
		return &model.RunRequest{
			Lang: "binary",
			Binaries: []model.Binary{{
				Name:    "run.sh",
				DataB64: "ZWNobw==",
				Mode:    "exec",
			}},
			Stdin:          "input\n",
			ExpectedStdout: "answer\n",
			Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			Interactor: &model.InteractorSpec{
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "interactor.sh",
					DataB64: "ZWNobw==",
					Mode:    "exec",
				}},
			},
		}
	}

	if err := Validate(valid()); err != nil {
		t.Fatalf("interactive request should validate: %v", err)
	}

	tests := []struct {
		name string
		edit func(*model.RunRequest)
		want string
	}{
		{
			name: "missing interactor binaries",
			edit: func(req *model.RunRequest) {
				req.Interactor.Binaries = nil
			},
			want: "interactor.binaries is required",
		},
		{
			name: "unsupported interactor language",
			edit: func(req *model.RunRequest) {
				req.Interactor.Lang = "not-a-runtime"
			},
			want: "unsupported interactor.lang",
		},
		{
			name: "spj is mutually exclusive",
			edit: func(req *model.RunRequest) {
				req.SPJ = &model.SPJSpec{Binary: &model.Binary{Name: "spj", DataB64: "ZWNobw==", Mode: "exec"}, Lang: "binary"}
			},
			want: "interactor cannot be combined with spj",
		},
		{
			name: "file outputs are ambiguous",
			edit: func(req *model.RunRequest) {
				req.FileOutputs = []model.OutputFile{{Path: "answer.txt"}}
			},
			want: "interactor cannot be combined with file_outputs",
		},
		{
			name: "interactor limit too high",
			edit: func(req *model.RunRequest) {
				req.Interactor.Limits = &model.Limits{TimeMs: MaxTimeMs + 1}
			},
			want: "interactor.limits.time_ms",
		},
		{
			name: "steps are mutually exclusive",
			edit: func(req *model.RunRequest) {
				req.Programs = []model.RunProgram{{
					ID:       "p",
					Lang:     "binary",
					Binaries: req.Binaries,
				}}
				req.Steps = []model.RunStep{{
					ID:        "s",
					ProgramID: "p",
					Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
				}}
				req.Lang = ""
				req.Binaries = nil
				req.Stdin = ""
				req.Limits = model.Limits{}
			},
			want: "legacy execute fields",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := valid()
			tc.edit(req)
			err := Validate(req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate error = %v, want containing %q", err, tc.want)
			}
		})
	}
}
