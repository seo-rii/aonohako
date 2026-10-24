package execute

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestServiceRejectsInconsistentTextOrSidecarContractsBeforeWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*model.RunRequest)
		want   string
	}{
		{
			name: "invalid expected output UTF-8",
			mutate: func(req *model.RunRequest) {
				req.ExpectedStdout = string([]byte{0xe9, '\n'})
			},
			want: "UTF-8",
		},
		{
			name: "invalid stdin UTF-8",
			mutate: func(req *model.RunRequest) {
				req.Stdin = string([]byte{0xe9, '\n'})
			},
			want: "UTF-8",
		},
		{
			name: "duplicate sidecars",
			mutate: func(req *model.RunRequest) {
				req.SidecarOutputs = []model.OutputFile{{Path: "answer"}, {Path: "answer"}}
			},
			want: "duplicate sidecar output path",
		},
		{
			name: "normalized duplicate sidecars",
			mutate: func(req *model.RunRequest) {
				req.SidecarOutputs = []model.OutputFile{{Path: "answer"}, {Path: "./answer"}}
			},
			want: "duplicate sidecar output path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workRoot := filepath.Join(t.TempDir(), "must-not-exist")
			t.Setenv("AONOHAKO_WORK_ROOT", workRoot)
			req := &model.RunRequest{
				Lang:     "binary",
				Binaries: []model.Binary{{Name: "runner", Mode: "exec", DataB64: b64("unused")}},
				Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
			}
			tc.mutate(req)
			response := New().Run(context.Background(), req, Hooks{})
			if response.Status != model.RunStatusInitFail || !strings.Contains(response.Reason, tc.want) {
				t.Fatalf("invalid request was not rejected at admission: %+v", response)
			}
			if _, err := os.Stat(workRoot); !os.IsNotExist(err) {
				t.Fatalf("invalid request reached workspace creation: %v", err)
			}
		})
	}
}
