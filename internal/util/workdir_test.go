package util

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateWorkDirUsesConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AONOHAKO_WORK_ROOT", root)

	dir, err := CreateWorkDir("aonohako-test-*")
	if err != nil {
		t.Fatalf("CreateWorkDir: %v", err)
	}

	if !strings.HasPrefix(dir, filepath.Clean(root)+string(filepath.Separator)) {
		t.Fatalf("expected work dir under %s, got %s", root, dir)
	}
}

func TestCreateWorkDirIgnoresLegacyRootEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AONOHAKO_WORK_ROOT", "")
	t.Setenv("GO_WORK_ROOT", root)

	dir, err := CreateWorkDir("aonohako-test-*")
	if err != nil {
		t.Fatalf("CreateWorkDir: %v", err)
	}

	if strings.HasPrefix(dir, filepath.Clean(root)+string(filepath.Separator)) {
		t.Fatalf("expected legacy GO_WORK_ROOT to be ignored, got %s", dir)
	}
}
