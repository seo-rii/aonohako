package execute

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"aonohako/internal/model"
)

func TestSidecarBudgetCountsOnlyCollectedFiles(t *testing.T) {
	ws, err := prepareWorkspaceDirs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	specs := []model.OutputFile{{Path: "a"}, {Path: "b"}, {Path: "too-large-for-rest"}, {Path: "fits-rest"}}
	sizes := []int64{8 << 20, 7 << 20, 2 << 20, 1 << 20}
	for i, spec := range specs {
		file, err := os.Create(filepath.Join(ws.BoxDir, spec.Path))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(sizes[i]); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	outputs, errs := captureSidecarOutputs(ws, specs)
	if len(errs) != 1 || errs[0].Path != specs[2].Path || errs[0].Reason != "sidecar total size exceeded" {
		t.Fatalf("sidecar errors = %+v, want only the 2 MiB file rejected", errs)
	}
	if len(outputs) != 3 || outputs[0].Path != "a" || outputs[1].Path != "b" || outputs[2].Path != "fits-rest" {
		t.Fatalf("collected paths do not include the final fitting file: %+v", sidecarPaths(outputs))
	}
	var total int
	for _, output := range outputs {
		data, err := base64.StdEncoding.DecodeString(output.DataB64)
		if err != nil {
			t.Fatal(err)
		}
		total += len(data)
	}
	if total != maxCapturedSidecarTotalBytes {
		t.Fatalf("collected bytes = %d, want %d", total, maxCapturedSidecarTotalBytes)
	}
}

func sidecarPaths(outputs []model.SidecarOutput) []string {
	paths := make([]string, len(outputs))
	for i, output := range outputs {
		paths[i] = output.Path
	}
	return paths
}
