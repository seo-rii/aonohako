package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzReadStats hammers the cgroup stat-file parser with arbitrary file bodies.
// These files come from the kernel, but a corrupt or truncated read must return
// an error rather than panic and take down the long-lived control plane.
func FuzzReadStats(f *testing.F) {
	f.Add("0", "0", "max 0\noom 0\n", "max 0\n", "usage_usec 0\nuser_usec 0\nsystem_usec 0\n", "0")
	f.Add("123456", "7", "low 0\nhigh 1\nmax 2\noom 3\noom_kill 4\n", "max 9\n", "usage_usec 5\nnr_throttled 2\nthrottled_usec 9\n", "999")
	f.Add("", "", "", "", "", "")
	f.Add("notanumber", "-1", "max\nx y z\n", "max 1 2 3\n", "usage_usec notanumber\n", "  ")
	f.Fuzz(func(t *testing.T, memCurrent, pidsCurrent, memEvents, pidsEvents, cpuStat, memPeak string) {
		dir := t.TempDir()
		write := func(name, body string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		write("memory.current", memCurrent)
		write("pids.current", pidsCurrent)
		write("memory.events", memEvents)
		write("pids.events", pidsEvents)
		write("cpu.stat", cpuStat)
		write("memory.peak", memPeak)

		stats, err := ReadStats(dir)
		if err != nil {
			return // malformed content may error; it must not panic
		}
		_ = stats.OOMEvents()
		_ = stats.MemoryMaxEvents()
		_ = stats.PidsMaxEvents()
		_ = stats.CPUThrottleEvents()
		_ = stats.FirstLimitBreach(1 << 20)
	})
}
