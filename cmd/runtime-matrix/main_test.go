package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeMatrixRejectsUnknownOnlyFilter(t *testing.T) {
	catalogPath := filepath.Join("..", "..", "runtime-images.yml")
	cmd := exec.Command("go", "run", ".", "-catalog", catalogPath, "-mode", "production", "-only", "definitely-not-an-image")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("runtime-matrix succeeded with an unmatched -only filter: %s", out)
	}
	if !strings.Contains(string(out), `no runtime image matches -only "definitely-not-an-image"`) {
		t.Fatalf("runtime-matrix error did not explain the unmatched filter: %s", out)
	}
}
