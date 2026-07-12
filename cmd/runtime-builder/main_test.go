package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePythonPackagesContextUsesProvidedDirectory(t *testing.T) {
	dir := t.TempDir()

	got, cleanup, err := resolvePythonPackagesContext(dir)
	if err != nil {
		t.Fatalf("resolvePythonPackagesContext(%q): %v", dir, err)
	}
	defer cleanup()

	if got != dir {
		t.Fatalf("context path = %q, want %q", got, dir)
	}
}

func TestResolvePythonPackagesContextCreatesEmptyContext(t *testing.T) {
	got, cleanup, err := resolvePythonPackagesContext("")
	if err != nil {
		t.Fatalf("resolvePythonPackagesContext(empty): %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(got, ".empty")); err != nil {
		t.Fatalf("empty context marker missing: %v", err)
	}
}

func TestResolvePythonPackagesContextRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packages.py")
	if err := os.WriteFile(path, []byte("pass\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, _, err := resolvePythonPackagesContext(path); err == nil {
		t.Fatalf("expected file path to be rejected as python packages context")
	}
}

func TestDefaultPythonPackagesContextUsesEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AONOHAKO_PYTHON_PACKAGES_CONTEXT", dir)

	if got := defaultPythonPackagesContext(); got != dir {
		t.Fatalf("default context = %q, want env directory %q", got, dir)
	}
}

func TestDefaultPythonPackagesContextUsesRepoPythonDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "python"), 0o755); err != nil {
		t.Fatalf("mkdir python context: %v", err)
	}
	t.Setenv("AONOHAKO_PYTHON_PACKAGES_CONTEXT", "")
	t.Chdir(dir)

	if got := defaultPythonPackagesContext(); got != "python" {
		t.Fatalf("default context = %q, want repository python directory", got)
	}
}

func TestDefaultPythonPackagesContextCanStayEmpty(t *testing.T) {
	t.Setenv("AONOHAKO_PYTHON_PACKAGES_CONTEXT", "")
	t.Chdir(t.TempDir())

	if got := defaultPythonPackagesContext(); got != "" {
		t.Fatalf("default context = %q, want empty", got)
	}
}

func TestRuntimeBuilderRejectsUnknownOnlyFilter(t *testing.T) {
	catalogPath := filepath.Join("..", "..", "runtime-images.yml")
	cmd := exec.Command("go", "run", ".", "-catalog", catalogPath, "-mode", "production", "-dry-run", "-only", "definitely-not-an-image")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("runtime-builder succeeded with an unmatched -only filter: %s", out)
	}
	if !strings.Contains(string(out), `no runtime image matches -only "definitely-not-an-image"`) {
		t.Fatalf("runtime-builder error did not explain the unmatched filter: %s", out)
	}
}

func TestRuntimeBuilderRejectsUnpinnedCatalogBaseImage(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "runtime-images.yml")
	body := `
languages:
  plain:
    smoke:
      command: ["plain", "--version"]
profiles:
  type-a:
    base_image: ubuntu:latest
    languages: [plain]
`
	if err := os.WriteFile(catalogPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile catalog: %v", err)
	}
	cmd := exec.Command("go", "run", ".", "-catalog", catalogPath, "-mode", "production", "-dry-run")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("runtime-builder accepted unpinned base image: %s", out)
	}
	if !strings.Contains(string(out), `base_image must be digest-pinned, got "ubuntu:latest"`) {
		t.Fatalf("runtime-builder error did not explain unpinned base image: %s", out)
	}
}
