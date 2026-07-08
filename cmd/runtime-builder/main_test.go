package main

import (
	"os"
	"path/filepath"
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
