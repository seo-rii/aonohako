package cgroup

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPreflightAtAcceptsCgroupV2WithRequiredControllers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cgroup.controllers"), "memory pids io cpu\n")
	writeFile(t, filepath.Join(root, "cgroup.subtree_control"), "")
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	writeFile(t, mountInfo, "36 25 0:31 / "+root+" rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n")

	got := PreflightAt(root, mountInfo)
	if !got.Available {
		t.Fatalf("PreflightAt() unavailable: %+v", got)
	}
	if got.Root != root {
		t.Fatalf("root = %q, want %q", got.Root, root)
	}
	if !got.SupportsIO {
		t.Fatalf("SupportsIO = false, want true")
	}
	wantControllers := []string{"cpu", "io", "memory", "pids"}
	if !reflect.DeepEqual(got.Controllers, wantControllers) {
		t.Fatalf("controllers = %+v, want %+v", got.Controllers, wantControllers)
	}
	if len(got.Missing) != 0 {
		t.Fatalf("missing = %+v, want empty", got.Missing)
	}
}

func TestPreflightAtRejectsMissingCgroupV2Mount(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cgroup.controllers"), "cpu memory pids\n")
	writeFile(t, filepath.Join(root, "cgroup.subtree_control"), "")
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	writeFile(t, mountInfo, "36 25 0:31 / /sys/fs/cgroup rw - tmpfs tmpfs rw\n")

	got := PreflightAt(root, mountInfo)
	if got.Available {
		t.Fatalf("PreflightAt() available, want unavailable")
	}
	if !hasMissing(got.Missing, "mount:cgroup2") {
		t.Fatalf("missing = %+v, want cgroup2 mount marker", got.Missing)
	}
	if !strings.Contains(got.Reason, "cgroup v2 mount") {
		t.Fatalf("reason = %q, want cgroup v2 mount explanation", got.Reason)
	}
}

func TestPreflightAtRejectsMissingRequiredControllers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cgroup.controllers"), "cpu io\n")
	writeFile(t, filepath.Join(root, "cgroup.subtree_control"), "")
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	writeFile(t, mountInfo, "36 25 0:31 / "+root+" rw - cgroup2 cgroup rw\n")

	got := PreflightAt(root, mountInfo)
	if got.Available {
		t.Fatalf("PreflightAt() available, want unavailable")
	}
	for _, want := range []string{"controller:memory", "controller:pids"} {
		if !hasMissing(got.Missing, want) {
			t.Fatalf("missing = %+v, want %s", got.Missing, want)
		}
	}
}

func TestPreflightAtRejectsMissingSubtreeControl(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cgroup.controllers"), "cpu memory pids\n")
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	writeFile(t, mountInfo, "36 25 0:31 / "+root+" rw - cgroup2 cgroup rw\n")

	got := PreflightAt(root, mountInfo)
	if got.Available {
		t.Fatalf("PreflightAt() available, want unavailable")
	}
	if !hasMissing(got.Missing, "file:cgroup.subtree_control") {
		t.Fatalf("missing = %+v, want subtree control marker", got.Missing)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func hasMissing(missing []string, want string) bool {
	for _, got := range missing {
		if got == want {
			return true
		}
	}
	return false
}
