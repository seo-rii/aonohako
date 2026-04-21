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

func TestDefaultMaxActiveRunsCloudRunIsOne(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	if got := defaultMaxActiveRuns(); got != 1 {
		t.Fatalf("expected Cloud Run default max active runs to be 1, got %d", got)
	}
}

func TestLoadRejectsCloudRunMarkersWithoutExplicitTarget(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "")
	t.Setenv("K_SERVICE", "aonohako")

	if _, err := Load(); err == nil {
		t.Fatalf("expected explicit cloudrun target requirement when Cloud Run markers are present")
	}
}

func TestLoadRejectsStrictTargetWithoutWorkRoot(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("AONOHAKO_WORK_ROOT", "")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")

	if _, err := Load(); err == nil {
		t.Fatalf("expected cloudrun target to require AONOHAKO_WORK_ROOT")
	}
}

func TestLoadRejectsRemoteExecutionWithoutURL(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "")

	if _, err := Load(); err == nil {
		t.Fatalf("expected remote execution to require AONOHAKO_REMOTE_RUNNER_URL")
	}
}

func TestLoadRejectsBearerRemoteAuthWithoutToken(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "bearer")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatalf("expected bearer remote auth to require a token")
	}
}

func TestLoadAllowsRemoteExecutionWithoutRoot(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "none")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Execution.Platform.ExecutionTransport != "remote" {
		t.Fatalf("execution transport mismatch: %+v", cfg.Execution.Platform)
	}
	if cfg.Execution.Platform.SandboxBackend != "none" {
		t.Fatalf("sandbox backend mismatch: %+v", cfg.Execution.Platform)
	}
}

func TestLoadRejectsEmbeddedHelperWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires non-root test runner")
	}
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "embedded")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "helper")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())

	if _, err := Load(); err == nil {
		t.Fatalf("expected embedded helper execution to require root")
	}
}

func TestLoadUsesEnvAndFallbacks(t *testing.T) {
	prevPort := os.Getenv("PORT")
	prevActive := os.Getenv("AONOHAKO_MAX_ACTIVE_RUNS")
	prevQueue := os.Getenv("AONOHAKO_MAX_PENDING_QUEUE")
	prevHeartbeat := os.Getenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC")
	t.Cleanup(func() {
		_ = os.Setenv("PORT", prevPort)
		_ = os.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", prevActive)
		_ = os.Setenv("AONOHAKO_MAX_PENDING_QUEUE", prevQueue)
		_ = os.Setenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", prevHeartbeat)
	})

	_ = os.Setenv("PORT", "18080")
	_ = os.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "3")
	_ = os.Setenv("AONOHAKO_MAX_PENDING_QUEUE", "7")
	_ = os.Setenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", "2")
	_ = os.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	_ = os.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	_ = os.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
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
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load returned error on fallback path: %v", err)
	}
	if cfg.MaxActiveRuns != defaultMaxActiveRuns() {
		t.Fatalf("fallback max active mismatch: %d", cfg.MaxActiveRuns)
	}
	if cfg.MaxPendingQueue != 0 {
		t.Fatalf("fallback max pending mismatch: %d", cfg.MaxPendingQueue)
	}
	if cfg.HeartbeatInterval != 10*time.Second {
		t.Fatalf("fallback heartbeat mismatch: %v", cfg.HeartbeatInterval)
	}
}

func TestLoadIgnoresLegacyEnvFallbacks(t *testing.T) {
	_ = os.Unsetenv("AONOHAKO_MAX_ACTIVE_RUNS")
	_ = os.Unsetenv("AONOHAKO_MAX_PENDING_QUEUE")
	_ = os.Unsetenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC")
	_ = os.Setenv("GO_MAX_ACTIVE_RUNS", "5")
	_ = os.Setenv("GO_MAX_PENDING_QUEUE", "9")
	_ = os.Setenv("GO_HEARTBEAT_INTERVAL_SEC", "4")
	_ = os.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	_ = os.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	_ = os.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")

	t.Cleanup(func() {
		_ = os.Unsetenv("GO_MAX_ACTIVE_RUNS")
		_ = os.Unsetenv("GO_MAX_PENDING_QUEUE")
		_ = os.Unsetenv("GO_HEARTBEAT_INTERVAL_SEC")
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MaxActiveRuns != defaultMaxActiveRuns() {
		t.Fatalf("legacy max active env should be ignored, got %d", cfg.MaxActiveRuns)
	}
	if cfg.MaxPendingQueue != 0 {
		t.Fatalf("legacy max pending env should be ignored, got %d", cfg.MaxPendingQueue)
	}
	if cfg.HeartbeatInterval != 10*time.Second {
		t.Fatalf("legacy heartbeat env should be ignored, got %v", cfg.HeartbeatInterval)
	}
}

func TestLoadMapsCloudRunIDTokenAudienceToRemoteURLWhenUnset(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUDIENCE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Execution.Remote.Audience != "https://runner.internal" {
		t.Fatalf("audience mismatch: %+v", cfg.Execution.Remote)
	}
}
