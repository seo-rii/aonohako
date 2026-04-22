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

func TestCreateWorkDirRequiresConfiguredRootInStrictModes(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "cloudrun")
	t.Setenv("AONOHAKO_WORK_ROOT", "")

	if _, err := CreateWorkDir("aonohako-test-*"); err == nil {
		t.Fatalf("expected strict execution mode to reject missing AONOHAKO_WORK_ROOT")
	}
}

func TestCreateWorkDirRejectsMissingStrictRoot(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "local-root")
	t.Setenv("AONOHAKO_WORK_ROOT", filepath.Join(t.TempDir(), "missing"))

	if _, err := CreateWorkDir("aonohako-test-*"); err == nil {
		t.Fatalf("expected strict execution mode to reject a missing work root")
	}
}

func TestCreateWorkDirRejectsUnknownExecutionMode(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "mystery")

	if _, err := CreateWorkDir("aonohako-test-*"); err == nil {
		t.Fatalf("expected unknown execution mode to be rejected")
	}
}
