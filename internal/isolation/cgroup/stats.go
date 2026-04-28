package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Stats struct {
	MemoryCurrentBytes int64
	MemoryPeakBytes    int64
	PidsCurrent        int64
	CPUUsageMicros     int64
	CPUUserMicros      int64
	CPUSystemMicros    int64
	CPUThrottled       int64
	CPUThrottledMicros int64
	MemoryEvents       map[string]int64
	PidsEvents         map[string]int64
}

func (s Stats) OOMEvents() int64 {
	return s.MemoryEvents["oom"] + s.MemoryEvents["oom_kill"] + s.MemoryEvents["oom_group_kill"]
}

func (s Stats) MemoryMaxEvents() int64 {
	return s.MemoryEvents["max"]
}

func (s Stats) PidsMaxEvents() int64 {
	return s.PidsEvents["max"]
}

func (s Stats) CPUThrottleEvents() int64 {
	return s.CPUThrottled
}

func (s Stats) MemoryLimitBreached(limitBytes int64) bool {
	return s.OOMEvents() > 0 || s.MemoryMaxEvents() > 0 || (limitBytes > 0 && s.MemoryCurrentBytes > limitBytes)
}

func (s Stats) PidsLimitBreached() bool {
	return s.PidsMaxEvents() > 0
}

func ReadStats(groupPath string) (Stats, error) {
	memoryCurrent, err := readIntFile(filepath.Join(groupPath, "memory.current"))
	if err != nil {
		return Stats{}, err
	}
	pidsCurrent, err := readIntFile(filepath.Join(groupPath, "pids.current"))
	if err != nil {
		return Stats{}, err
	}
	events, err := readKeyValueFile(filepath.Join(groupPath, "memory.events"))
	if err != nil {
		return Stats{}, err
	}
	pidsEvents, err := readKeyValueFile(filepath.Join(groupPath, "pids.events"))
	if err != nil {
		return Stats{}, err
	}
	cpu, err := readKeyValueFile(filepath.Join(groupPath, "cpu.stat"))
	if err != nil {
		return Stats{}, err
	}

	stats := Stats{
		MemoryCurrentBytes: memoryCurrent,
		PidsCurrent:        pidsCurrent,
		CPUUsageMicros:     cpu["usage_usec"],
		CPUUserMicros:      cpu["user_usec"],
		CPUSystemMicros:    cpu["system_usec"],
		CPUThrottled:       cpu["nr_throttled"],
		CPUThrottledMicros: cpu["throttled_usec"],
		MemoryEvents:       events,
		PidsEvents:         pidsEvents,
	}
	if peak, err := readIntFile(filepath.Join(groupPath, "memory.peak")); err == nil {
		stats.MemoryPeakBytes = peak
	}
	return stats, nil
}

func readIntFile(path string) (int64, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return value, nil
}

func readKeyValueFile(path string) (map[string]int64, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	values := map[string]int64{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("parse %s line %q", filepath.Base(path), line)
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s key %s: %w", filepath.Base(path), fields[0], err)
		}
		values[fields[0]] = value
	}
	return values, nil
}
