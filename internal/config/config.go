package config

import (
	"os"
	"runtime"
	"strconv"
	"time"
)

type Config struct {
	Port              string
	MaxActiveRuns     int
	MaxPendingQueue   int
	HeartbeatInterval time.Duration
}

func Load() Config {
	port := getenv("PORT", "8080")
	maxActive := parsePositiveInt(getenvAny([]string{"AONOHAKO_MAX_ACTIVE_RUNS", "GO_MAX_ACTIVE_RUNS"}, ""), defaultMaxActiveRuns())
	maxPending := parseNonNegativeInt(getenvAny([]string{"AONOHAKO_MAX_PENDING_QUEUE", "GO_MAX_PENDING_QUEUE"}, "0"), 0)
	heartbeatSec := parsePositiveInt(getenvAny([]string{"AONOHAKO_HEARTBEAT_INTERVAL_SEC", "GO_HEARTBEAT_INTERVAL_SEC"}, "10"), 10)
	return Config{
		Port:              port,
		MaxActiveRuns:     maxActive,
		MaxPendingQueue:   maxPending,
		HeartbeatInterval: time.Duration(heartbeatSec) * time.Second,
	}
}

func defaultMaxActiveRuns() int {
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

func getenvAny(keys []string, fallback string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
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
