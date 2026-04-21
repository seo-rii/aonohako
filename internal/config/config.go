package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/platform"
)

type Config struct {
	Port              string
	MaxActiveRuns     int
	MaxPendingQueue   int
	HeartbeatInterval time.Duration
}

func Load() (Config, error) {
	port := getenv("PORT", "8080")
	maxActive := parsePositiveInt(getenv("AONOHAKO_MAX_ACTIVE_RUNS", ""), defaultMaxActiveRuns())
	maxPending := parseNonNegativeInt(getenv("AONOHAKO_MAX_PENDING_QUEUE", "0"), 0)
	heartbeatSec := parsePositiveInt(getenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", "10"), 10)
	mode := platform.CurrentExecutionMode()
	workRoot := strings.TrimSpace(os.Getenv("AONOHAKO_WORK_ROOT"))

	if platform.CloudRunMarkersPresent() && mode != platform.ExecutionModeCloudRun {
		return Config{}, fmt.Errorf("AONOHAKO_EXECUTION_MODE=cloudrun is required when Cloud Run markers are present")
	}
	if platform.UsesDedicatedWorkRoot() {
		if workRoot == "" {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT is required in %s mode", mode)
		}
		info, err := os.Stat(workRoot)
		if err != nil {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT validation failed: %w", err)
		}
		if !info.IsDir() {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT is not a directory: %s", workRoot)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT must be owned by uid %d", os.Geteuid())
		}
		probe, err := os.MkdirTemp(workRoot, ".aonohako-contract-*")
		if err != nil {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT is not writable: %w", err)
		}
		_ = os.RemoveAll(probe)
		if os.Geteuid() != 0 {
			return Config{}, fmt.Errorf("execution mode %s requires root", mode)
		}
	}

	return Config{
		Port:              port,
		MaxActiveRuns:     maxActive,
		MaxPendingQueue:   maxPending,
		HeartbeatInterval: time.Duration(heartbeatSec) * time.Second,
	}, nil
}

func defaultMaxActiveRuns() int {
	if platform.IsCloudRun() {
		return 1
	}
	cpu := runtime.NumCPU()
	if cpu <= 1 {
		return 1
	}
	v := cpu - 2
	if v < 1 {
		return 1
	}
	return v
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parsePositiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func parseNonNegativeInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}
