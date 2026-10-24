package runvalidation

import (
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestValidateRejectsDuplicateSidecarOutputPaths(t *testing.T) {
	for _, paths := range [][]string{
		{"x", "x"},
		{"x", "./x"},
		{"x", "dir/../x"},
		{"dir/x", "dir//x"},
		{"x", " x "},
	} {
		t.Run(strings.Join(paths, ","), func(t *testing.T) {
			for _, makeRequest := range []func() *model.RunRequest{validSidecarOutputRequest, validTwoStepPipelineRequest} {
				req := makeRequest()
				for _, path := range paths {
					req.SidecarOutputs = append(req.SidecarOutputs, model.OutputFile{Path: path})
				}
				if err := Validate(req); err == nil || !strings.Contains(err.Error(), "duplicate sidecar output path") {
					t.Fatalf("Validate() error = %v, want duplicate sidecar rejection", err)
				}
			}
		})
	}
}

func TestValidateSidecarOutputPaths(t *testing.T) {
	for _, path := range []string{"", ".", "..", "../x", "/x"} {
		t.Run(path, func(t *testing.T) {
			req := validSidecarOutputRequest()
			req.SidecarOutputs = []model.OutputFile{{Path: path}}
			if err := Validate(req); err == nil || !strings.Contains(err.Error(), "sidecar_outputs[0].path") {
				t.Fatalf("Validate() error = %v, want invalid sidecar path", err)
			}
		})
	}

	req := validSidecarOutputRequest()
	req.SidecarOutputs = []model.OutputFile{{Path: "./x"}, {Path: "dir/y"}, {Path: "a..b"}}
	if err := Validate(req); err != nil {
		t.Fatalf("distinct valid sidecar paths rejected: %v", err)
	}
}

func validSidecarOutputRequest() *model.RunRequest {
	return &model.RunRequest{
		Lang:     "binary",
		Binaries: []model.Binary{{Name: "program", DataB64: "eA==", Mode: "exec"}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}
}
