package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCommandPathUsesProvidedPath(t *testing.T) {
	tempDir := t.TempDir()
	safeDir := filepath.Join(tempDir, "safe")
	poisonDir := filepath.Join(tempDir, "poison")
	for _, dir := range []string{safeDir, poisonDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	poison := filepath.Join(poisonDir, "nim")
	if err := os.WriteFile(poison, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatalf("write poison binary: %v", err)
	}
	safe := filepath.Join(safeDir, "nim")
	if err := os.WriteFile(safe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write safe binary: %v", err)
	}

	t.Setenv("PATH", poisonDir)

	path, err := ResolveCommandPath("nim", []string{"PATH=" + safeDir})
	if err != nil {
		t.Fatalf("ResolveCommandPath: %v", err)
	}
	if path != safe {
		t.Fatalf("resolved path = %q, want %q", path, safe)
	}
}

func TestResolveCommandPathPreservesSymlinkArgv0(t *testing.T) {
	tempDir := t.TempDir()
	target := filepath.Join(tempDir, "toolchain-driver")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(tempDir, "swiftc")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	path, err := ResolveCommandPath("swiftc", []string{"PATH=" + tempDir})
	if err != nil {
		t.Fatalf("ResolveCommandPath: %v", err)
	}
	if path != link {
		t.Fatalf("resolved path = %q, want symlink %q", path, link)
	}
}
