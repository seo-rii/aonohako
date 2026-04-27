package cgroup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateRunGroupWritesRequiredLimits(t *testing.T) {
	parent := t.TempDir()
	group, err := CreateRunGroup(parent, "run-123", Limits{
		MemoryMaxBytes:  128 << 20,
		PidsMax:         32,
		CPUQuotaMicros:  100000,
		CPUPeriodMicros: 200000,
	})
	if err != nil {
		t.Fatalf("CreateRunGroup() error = %v", err)
	}
	if group.Path != filepath.Join(parent, "run-123") {
		t.Fatalf("path = %q", group.Path)
	}
	assertFile(t, filepath.Join(group.Path, "memory.max"), "134217728")
	assertFile(t, filepath.Join(group.Path, "memory.oom.group"), "1")
	assertFile(t, filepath.Join(group.Path, "pids.max"), "32")
	assertFile(t, filepath.Join(group.Path, "cpu.max"), "100000 200000")
}

func TestEnableControllersWritesSubtreeControl(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "cgroup.subtree_control"), "")

	if err := EnableControllers(parent, []string{"cpu", "memory", "pids"}); err != nil {
		t.Fatalf("EnableControllers() error = %v", err)
	}
	assertFile(t, filepath.Join(parent, "cgroup.subtree_control"), "+cpu +memory +pids")
}

func TestEnableControllersRejectsUnsafeNames(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "cgroup.subtree_control"), "")

	for _, controller := range []string{"", "+cpu", "-cpu", "cpu memory", "cpu/memory"} {
		t.Run(controller, func(t *testing.T) {
			if err := EnableControllers(parent, []string{controller}); err == nil {
				t.Fatalf("EnableControllers(%q) error = nil, want rejection", controller)
			}
		})
	}
}

func TestValidateParentAcceptsRequiredControllers(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "cgroup.controllers"), "cpu memory pids io\n")
	writeFile(t, filepath.Join(parent, "cgroup.subtree_control"), "")
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	writeFile(t, mountInfo, "36 25 0:31 / "+parent+" rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n")

	if err := ValidateParentAt(parent, mountInfo, []string{"cpu", "memory", "pids"}); err != nil {
		t.Fatalf("ValidateParent() error = %v", err)
	}
}

func TestValidateParentRejectsMissingController(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "cgroup.controllers"), "cpu memory\n")
	writeFile(t, filepath.Join(parent, "cgroup.subtree_control"), "")

	err := ValidateParent(parent, []string{"cpu", "memory", "pids"})
	if err == nil {
		t.Fatalf("ValidateParent() error = nil, want missing pids rejection")
	}
	if !strings.Contains(err.Error(), "pids") {
		t.Fatalf("error %q should mention pids", err)
	}
}

func TestValidateParentRejectsMissingSubtreeControl(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "cgroup.controllers"), "cpu memory pids\n")

	err := ValidateParent(parent, []string{"cpu", "memory", "pids"})
	if err == nil {
		t.Fatalf("ValidateParent() error = nil, want missing subtree control rejection")
	}
	if !strings.Contains(err.Error(), "cgroup.subtree_control") {
		t.Fatalf("error %q should mention cgroup.subtree_control", err)
	}
}

func TestValidateParentRejectsParentOutsideCgroup2Mount(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "cgroup.controllers"), "cpu memory pids\n")
	writeFile(t, filepath.Join(parent, "cgroup.subtree_control"), "")
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	writeFile(t, mountInfo, "36 25 0:31 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n")

	err := ValidateParentAt(parent, mountInfo, []string{"cpu", "memory", "pids"})
	if err == nil {
		t.Fatalf("ValidateParent() error = nil, want non-cgroup mount rejection")
	}
	if !strings.Contains(err.Error(), "cgroup2 mount") {
		t.Fatalf("error %q should mention cgroup2 mount", err)
	}
}

func TestValidateParentAcceptsNestedCgroupParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "aonohako")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	writeFile(t, filepath.Join(parent, "cgroup.controllers"), "cpu memory pids\n")
	writeFile(t, filepath.Join(parent, "cgroup.subtree_control"), "")
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	writeFile(t, mountInfo, "36 25 0:31 / "+root+" rw - cgroup2 cgroup rw\n")

	if err := ValidateParentAt(parent, mountInfo, []string{"cpu", "memory", "pids"}); err != nil {
		t.Fatalf("ValidateParentAt() error = %v", err)
	}
}

func TestRunNameSanitizesPrefix(t *testing.T) {
	got := RunName("../bad name")
	if strings.ContainsAny(got, "/ \t\r\n") {
		t.Fatalf("RunName() returned unsafe name %q", got)
	}
	if !strings.HasPrefix(got, "bad-name-") {
		t.Fatalf("RunName() = %q, want sanitized prefix", got)
	}
}

func TestCreateRunGroupRejectsUnsafeNames(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"", ".", "..", "../run", "nested/run", "run with space"} {
		t.Run(name, func(t *testing.T) {
			_, err := CreateRunGroup(parent, name, Limits{MemoryMaxBytes: 1, PidsMax: 1})
			if err == nil {
				t.Fatalf("CreateRunGroup(%q) error = nil, want rejection", name)
			}
		})
	}
}

func TestCreateRunGroupRejectsMissingHardLimits(t *testing.T) {
	parent := t.TempDir()
	tests := []struct {
		name   string
		limits Limits
		want   string
	}{
		{name: "memory", limits: Limits{PidsMax: 1}, want: "memory"},
		{name: "pids", limits: Limits{MemoryMaxBytes: 1}, want: "pids"},
		{name: "cpu-pair", limits: Limits{MemoryMaxBytes: 1, PidsMax: 1, CPUQuotaMicros: 100000}, want: "cpu quota and period"},
		{name: "cpu-negative", limits: Limits{MemoryMaxBytes: 1, PidsMax: 1, CPUQuotaMicros: -1, CPUPeriodMicros: -1}, want: "must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateRunGroup(parent, "run-"+tc.name, tc.limits)
			if err == nil {
				t.Fatalf("CreateRunGroup() error = nil, want rejection")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should contain %q", err, tc.want)
			}
		})
	}
}

func TestCreateRunGroupDoesNotWriteCPUMaxWhenUnset(t *testing.T) {
	parent := t.TempDir()
	group, err := CreateRunGroup(parent, "run-no-cpu", Limits{
		MemoryMaxBytes: 64 << 20,
		PidsMax:        16,
	})
	if err != nil {
		t.Fatalf("CreateRunGroup() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(group.Path, "cpu.max")); !os.IsNotExist(err) {
		t.Fatalf("cpu.max should not be written when CPU limit is unset, stat err=%v", err)
	}
}

func TestGroupAddProcWritesPID(t *testing.T) {
	parent := t.TempDir()
	group, err := CreateRunGroup(parent, "run-proc", Limits{
		MemoryMaxBytes: 64 << 20,
		PidsMax:        16,
	})
	if err != nil {
		t.Fatalf("CreateRunGroup() error = %v", err)
	}
	writeFile(t, filepath.Join(group.Path, "cgroup.procs"), "")
	if err := group.AddProc(12345); err != nil {
		t.Fatalf("AddProc() error = %v", err)
	}
	assertFile(t, filepath.Join(group.Path, "cgroup.procs"), "12345")
	if err := group.AddProc(0); err == nil {
		t.Fatalf("AddProc(0) error = nil, want rejection")
	}
}

func TestGroupRemoveDeletesRunGroup(t *testing.T) {
	parent := t.TempDir()
	group, err := CreateRunGroup(parent, "run-cleanup", Limits{
		MemoryMaxBytes: 64 << 20,
		PidsMax:        16,
	})
	if err != nil {
		t.Fatalf("CreateRunGroup() error = %v", err)
	}
	removeFakeCgroupFiles(t, group)
	if err := group.Remove(); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(group.Path); !os.IsNotExist(err) {
		t.Fatalf("group path should be removed, stat err=%v", err)
	}
	if err := group.Remove(); err != nil {
		t.Fatalf("second Remove() should be idempotent, got %v", err)
	}
}

func TestGroupRemoveRejectsNonEmptyRunGroup(t *testing.T) {
	parent := t.TempDir()
	group, err := CreateRunGroup(parent, "run-nonempty", Limits{
		MemoryMaxBytes: 64 << 20,
		PidsMax:        16,
	})
	if err != nil {
		t.Fatalf("CreateRunGroup() error = %v", err)
	}
	removeFakeCgroupFiles(t, group)
	writeFile(t, filepath.Join(group.Path, "leftover"), "x")
	if err := group.Remove(); err == nil {
		t.Fatalf("Remove() error = nil, want non-empty cgroup rejection")
	}
}

func removeFakeCgroupFiles(t *testing.T, group Group) {
	t.Helper()
	for _, name := range []string{"memory.max", "memory.oom.group", "pids.max", "cpu.max"} {
		err := os.Remove(filepath.Join(group.Path, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove fake cgroup file %s: %v", name, err)
		}
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(body) != want {
		t.Fatalf("%s = %q, want %q", path, string(body), want)
	}
}
