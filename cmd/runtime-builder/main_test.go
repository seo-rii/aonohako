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
