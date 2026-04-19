package config

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestDefaultMaxActiveRuns(t *testing.T) {
	got := defaultMaxActiveRuns()
	cpu := runtime.NumCPU()
	if cpu == 1 && got != 1 {
		t.Fatalf("expected 1 for single-core, got %d", got)
	}
	if cpu > 1 {
		expected := cpu - 2
		if expected < 1 {
			expected = 1
		}
		if got != expected {
			t.Fatalf("expected %d, got %d", expected, got)
		}
	}
}

func TestLoadUsesEnvAndFallbacks(t *testing.T) {
	prevPort := os.Getenv("PORT")
	prevActive := os.Getenv("AONOHAKO_MAX_ACTIVE_RUNS")
	prevQueue := os.Getenv("AONOHAKO_MAX_PENDING_QUEUE")
	prevHeartbeat := os.Getenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC")
	prevLegacyActive := os.Getenv("GO_MAX_ACTIVE_RUNS")
	prevLegacyQueue := os.Getenv("GO_MAX_PENDING_QUEUE")
	prevLegacyHeartbeat := os.Getenv("GO_HEARTBEAT_INTERVAL_SEC")
	t.Cleanup(func() {
		_ = os.Setenv("PORT", prevPort)
		_ = os.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", prevActive)
		_ = os.Setenv("AONOHAKO_MAX_PENDING_QUEUE", prevQueue)
		_ = os.Setenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", prevHeartbeat)
		_ = os.Setenv("GO_MAX_ACTIVE_RUNS", prevLegacyActive)
		_ = os.Setenv("GO_MAX_PENDING_QUEUE", prevLegacyQueue)
		_ = os.Setenv("GO_HEARTBEAT_INTERVAL_SEC", prevLegacyHeartbeat)
	})

	_ = os.Setenv("PORT", "18080")
	_ = os.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "3")
	_ = os.Setenv("AONOHAKO_MAX_PENDING_QUEUE", "7")
	_ = os.Setenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", "2")
	_ = os.Unsetenv("GO_MAX_ACTIVE_RUNS")
	_ = os.Unsetenv("GO_MAX_PENDING_QUEUE")
	_ = os.Unsetenv("GO_HEARTBEAT_INTERVAL_SEC")

	cfg := Load()
	if cfg.Port != "18080" {
		t.Fatalf("port mismatch: %s", cfg.Port)
	}
	if cfg.MaxActiveRuns != 3 {
		t.Fatalf("max active mismatch: %d", cfg.MaxActiveRuns)
	}
	if cfg.MaxPendingQueue != 7 {
		t.Fatalf("max pending mismatch: %d", cfg.MaxPendingQueue)
	}
	if cfg.HeartbeatInterval != 2*time.Second {
		t.Fatalf("heartbeat mismatch: %v", cfg.HeartbeatInterval)
	}

	_ = os.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "-1")
	_ = os.Setenv("AONOHAKO_MAX_PENDING_QUEUE", "-1")
	_ = os.Setenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", "0")
	cfg = Load()
	if cfg.MaxActiveRuns != defaultMaxActiveRuns() {
		t.Fatalf("fallback max active mismatch: %d", cfg.MaxActiveRuns)
	}
	if cfg.MaxPendingQueue != 0 {
		t.Fatalf("fallback max pending mismatch: %d", cfg.MaxPendingQueue)
	}
	if cfg.HeartbeatInterval != 10*time.Second {
		t.Fatalf("fallback heartbeat mismatch: %v", cfg.HeartbeatInterval)
	}

	_ = os.Unsetenv("AONOHAKO_MAX_ACTIVE_RUNS")
	_ = os.Unsetenv("AONOHAKO_MAX_PENDING_QUEUE")
	_ = os.Unsetenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC")
	_ = os.Setenv("GO_MAX_ACTIVE_RUNS", "5")
	_ = os.Setenv("GO_MAX_PENDING_QUEUE", "9")
	_ = os.Setenv("GO_HEARTBEAT_INTERVAL_SEC", "4")
	cfg = Load()
	if cfg.MaxActiveRuns != 5 || cfg.MaxPendingQueue != 9 || cfg.HeartbeatInterval != 4*time.Second {
		t.Fatalf("legacy env fallback mismatch: %+v", cfg)
	}
}
