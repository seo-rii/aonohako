package config

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"aonohako/internal/platform"
)

func TestDefaultMaxActiveRuns(t *testing.T) {
	got := defaultMaxActiveRuns(platform.RuntimeOptions{
		DeploymentTarget:   platform.DeploymentTargetDev,
		ExecutionTransport: platform.ExecutionTransportRemote,
		SandboxBackend:     platform.SandboxBackendNone,
	})
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
	if got := defaultMaxActiveRuns(platform.RuntimeOptions{
		DeploymentTarget:   platform.DeploymentTargetCloudRun,
		ExecutionTransport: platform.ExecutionTransportRemote,
		SandboxBackend:     platform.SandboxBackendNone,
	}); got != 1 {
		t.Fatalf("expected Cloud Run default max active runs to be 1, got %d", got)
	}
}

func TestDefaultMaxActiveRunsEmbeddedHelperIsOne(t *testing.T) {
	if got := defaultMaxActiveRuns(platform.RuntimeOptions{
		DeploymentTarget:   platform.DeploymentTargetSelfHosted,
		ExecutionTransport: platform.ExecutionTransportEmbedded,
		SandboxBackend:     platform.SandboxBackendHelper,
	}); got != 1 {
		t.Fatalf("expected embedded helper default max active runs to be 1, got %d", got)
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

func TestLoadRejectsInvalidRemoteRunnerURLs(t *testing.T) {
	tests := []string{
		"/execute",
		"ftp://runner.internal/execute",
		"https://user:pass@runner.internal/execute",
		"https://runner.internal/execute?debug=1",
		"https://runner.internal/execute#frag",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
			t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
			t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
			t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", rawURL)

			if _, err := Load(); err == nil {
				t.Fatalf("expected Load() to reject remote URL %q", rawURL)
			}
		})
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

func TestLoadRejectsEmbeddedHelperWithParallelActiveRuns(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "embedded")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "helper")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	t.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "2")

	if _, err := Load(); err == nil {
		t.Fatalf("expected embedded helper execution to reject parallel active runs")
	}
}

func TestLoadRejectsUnknownRuntimeAxisValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "execution mode", env: map[string]string{"AONOHAKO_EXECUTION_MODE": "mystery"}},
		{name: "deployment target", env: map[string]string{"AONOHAKO_DEPLOYMENT_TARGET": "mystery"}},
		{name: "execution transport", env: map[string]string{"AONOHAKO_EXECUTION_TRANSPORT": "mystery"}},
		{name: "sandbox backend", env: map[string]string{"AONOHAKO_SANDBOX_BACKEND": "mystery"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AONOHAKO_EXECUTION_MODE", "")
			t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
			t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
			t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
			t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil {
				t.Fatalf("expected Load() to reject %+v", tc.env)
			}
		})
	}
}

func TestLoadRejectsGroupWritableDedicatedWorkRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o775); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_WORK_ROOT", root)

	if _, err := Load(); err == nil {
		t.Fatalf("expected group-writable dedicated work root to be rejected")
	}
}

func TestLoadUsesConfiguredNumericEnv(t *testing.T) {
	t.Setenv("PORT", "18080")
	t.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "3")
	t.Setenv("AONOHAKO_MAX_PENDING_QUEUE", "7")
	t.Setenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", "2")
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")

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
}

func TestLoadRejectsInvalidNumericEnv(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "max active zero", key: "AONOHAKO_MAX_ACTIVE_RUNS", value: "0"},
		{name: "max active negative", key: "AONOHAKO_MAX_ACTIVE_RUNS", value: "-1"},
		{name: "max active malformed", key: "AONOHAKO_MAX_ACTIVE_RUNS", value: "many"},
		{name: "pending negative", key: "AONOHAKO_MAX_PENDING_QUEUE", value: "-1"},
		{name: "pending malformed", key: "AONOHAKO_MAX_PENDING_QUEUE", value: "many"},
		{name: "heartbeat zero", key: "AONOHAKO_HEARTBEAT_INTERVAL_SEC", value: "0"},
		{name: "heartbeat negative", key: "AONOHAKO_HEARTBEAT_INTERVAL_SEC", value: "-1"},
		{name: "heartbeat malformed", key: "AONOHAKO_HEARTBEAT_INTERVAL_SEC", value: "soon"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
			t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
			t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
			t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
			t.Setenv(tc.key, tc.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("expected Load() to reject %s=%q", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error %q should mention %s", err, tc.key)
			}
		})
	}
}

func TestLoadIgnoresLegacyEnvFallbacks(t *testing.T) {
	t.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "")
	t.Setenv("AONOHAKO_MAX_PENDING_QUEUE", "")
	t.Setenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", "")
	t.Setenv("GO_MAX_ACTIVE_RUNS", "5")
	t.Setenv("GO_MAX_PENDING_QUEUE", "9")
	t.Setenv("GO_HEARTBEAT_INTERVAL_SEC", "4")
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MaxActiveRuns != defaultMaxActiveRuns(cfg.Execution.Platform) {
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
