package cgroup

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestReadStatsReadsCgroupAccountingFiles(t *testing.T) {
	group := t.TempDir()
	writeFile(t, filepath.Join(group, "memory.current"), "1048576\n")
	writeFile(t, filepath.Join(group, "memory.peak"), "2097152\n")
	writeFile(t, filepath.Join(group, "pids.current"), "3\n")
	writeFile(t, filepath.Join(group, "memory.events"), strings.Join([]string{
		"low 0",
		"high 1",
		"max 2",
		"oom 3",
		"oom_kill 4",
		"oom_group_kill 5",
	}, "\n")+"\n")
	writeFile(t, filepath.Join(group, "pids.events"), "max 6\n")
	writeFile(t, filepath.Join(group, "cpu.stat"), strings.Join([]string{
		"usage_usec 100",
		"user_usec 60",
		"system_usec 40",
		"nr_periods 7",
		"nr_throttled 8",
		"throttled_usec 90",
	}, "\n")+"\n")

	got, err := ReadStats(group)
	if err != nil {
		t.Fatalf("ReadStats() error = %v", err)
	}
	if got.MemoryCurrentBytes != 1048576 {
		t.Fatalf("MemoryCurrentBytes = %d", got.MemoryCurrentBytes)
	}
	if got.MemoryPeakBytes != 2097152 {
		t.Fatalf("MemoryPeakBytes = %d", got.MemoryPeakBytes)
	}
	if got.PidsCurrent != 3 {
		t.Fatalf("PidsCurrent = %d", got.PidsCurrent)
	}
	if got.CPUUsageMicros != 100 || got.CPUUserMicros != 60 || got.CPUSystemMicros != 40 {
		t.Fatalf("CPU stats mismatch: %+v", got)
	}
	if got.CPUThrottled != 8 || got.CPUThrottledMicros != 90 {
		t.Fatalf("CPU throttling mismatch: %+v", got)
	}
	if got.OOMEvents() != 12 {
		t.Fatalf("OOMEvents() = %d, want 12", got.OOMEvents())
	}
	if got.MemoryMaxEvents() != 2 {
		t.Fatalf("MemoryMaxEvents() = %d, want 2", got.MemoryMaxEvents())
	}
	if got.PidsMaxEvents() != 6 {
		t.Fatalf("PidsMaxEvents() = %d, want 6", got.PidsMaxEvents())
	}
	if got.CPUThrottleEvents() != 8 {
		t.Fatalf("CPUThrottleEvents() = %d, want 8", got.CPUThrottleEvents())
	}
}

func TestReadStatsAllowsMissingMemoryPeak(t *testing.T) {
	group := t.TempDir()
	writeFile(t, filepath.Join(group, "memory.current"), "1\n")
	writeFile(t, filepath.Join(group, "pids.current"), "1\n")
	writeFile(t, filepath.Join(group, "memory.events"), "oom 0\n")
	writeFile(t, filepath.Join(group, "pids.events"), "max 0\n")
	writeFile(t, filepath.Join(group, "cpu.stat"), "usage_usec 1\n")

	got, err := ReadStats(group)
	if err != nil {
		t.Fatalf("ReadStats() error = %v", err)
	}
	if got.MemoryPeakBytes != 0 {
		t.Fatalf("missing memory.peak should leave MemoryPeakBytes at zero, got %d", got.MemoryPeakBytes)
	}
}

func TestReadStatsRejectsMissingRequiredFiles(t *testing.T) {
	group := t.TempDir()
	writeFile(t, filepath.Join(group, "pids.current"), "1\n")
	writeFile(t, filepath.Join(group, "memory.events"), "oom 0\n")
	writeFile(t, filepath.Join(group, "pids.events"), "max 0\n")
	writeFile(t, filepath.Join(group, "cpu.stat"), "usage_usec 1\n")

	_, err := ReadStats(group)
	if err == nil {
		t.Fatalf("ReadStats() error = nil, want missing memory.current rejection")
	}
	if !strings.Contains(err.Error(), "memory.current") {
		t.Fatalf("error %q should mention memory.current", err)
	}
}

func TestReadStatsRejectsMissingPidsEvents(t *testing.T) {
	group := t.TempDir()
	writeFile(t, filepath.Join(group, "memory.current"), "1\n")
	writeFile(t, filepath.Join(group, "pids.current"), "1\n")
	writeFile(t, filepath.Join(group, "memory.events"), "oom 0\n")
	writeFile(t, filepath.Join(group, "cpu.stat"), "usage_usec 1\n")

	_, err := ReadStats(group)
	if err == nil {
		t.Fatalf("ReadStats() error = nil, want missing pids.events rejection")
	}
	if !strings.Contains(err.Error(), "pids.events") {
		t.Fatalf("error %q should mention pids.events", err)
	}
}

func TestReadStatsRejectsMalformedKeyValueFiles(t *testing.T) {
	group := t.TempDir()
	writeFile(t, filepath.Join(group, "memory.current"), "1\n")
	writeFile(t, filepath.Join(group, "pids.current"), "1\n")
	writeFile(t, filepath.Join(group, "memory.events"), "oom nope\n")
	writeFile(t, filepath.Join(group, "pids.events"), "max 0\n")
	writeFile(t, filepath.Join(group, "cpu.stat"), "usage_usec 1\n")

	_, err := ReadStats(group)
	if err == nil {
		t.Fatalf("ReadStats() error = nil, want malformed memory.events rejection")
	}
	if !strings.Contains(err.Error(), "memory.events") {
		t.Fatalf("error %q should mention memory.events", err)
	}
}
