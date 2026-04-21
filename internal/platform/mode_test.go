package platform

import "testing"

func TestCurrentExecutionModeDefaultsToLocalDev(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "")
	if got := CurrentExecutionMode(); got != ExecutionModeLocalDev {
		t.Fatalf("CurrentExecutionMode() = %q, want %q", got, ExecutionModeLocalDev)
	}
}

func TestCurrentExecutionModeParsesExplicitModes(t *testing.T) {
	tests := []struct {
		raw  string
		want ExecutionMode
	}{
		{raw: "cloudrun", want: ExecutionModeCloudRun},
		{raw: "local-root", want: ExecutionModeLocalRoot},
		{raw: "local-dev", want: ExecutionModeLocalDev},
		{raw: " CLOUDRUN ", want: ExecutionModeCloudRun},
		{raw: "unknown", want: ExecutionModeLocalDev},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("AONOHAKO_EXECUTION_MODE", tc.raw)
			if got := CurrentExecutionMode(); got != tc.want {
				t.Fatalf("CurrentExecutionMode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCloudRunMarkersDoNotChangeExecutionMode(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "")
	t.Setenv("K_SERVICE", "aonohako")
	t.Setenv("CLOUD_RUN_JOB", "job")
	t.Setenv("CLOUD_RUN_WORKER_POOL", "pool")

	if !CloudRunMarkersPresent() {
		t.Fatalf("CloudRunMarkersPresent() = false, want true")
	}
	if got := CurrentExecutionMode(); got != ExecutionModeLocalDev {
		t.Fatalf("CurrentExecutionMode() = %q, want %q even when markers are present", got, ExecutionModeLocalDev)
	}
	if IsCloudRun() {
		t.Fatalf("IsCloudRun() = true, want false without explicit target")
	}
}

func TestCurrentRuntimeOptionsDefaultToLegacyLocalDevShape(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "")
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "")

	got := CurrentRuntimeOptions()
	if got.DeploymentTarget != DeploymentTargetDev || got.ExecutionTransport != ExecutionTransportEmbedded || got.SandboxBackend != SandboxBackendHelper {
		t.Fatalf("CurrentRuntimeOptions() = %+v", got)
	}
}

func TestCurrentRuntimeOptionsAllowAxisOverrides(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "cloudrun")
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")

	got := CurrentRuntimeOptions()
	if got.DeploymentTarget != DeploymentTargetSelfHosted || got.ExecutionTransport != ExecutionTransportRemote || got.SandboxBackend != SandboxBackendNone {
		t.Fatalf("CurrentRuntimeOptions() = %+v", got)
	}
}

func TestCurrentRuntimeOptionsDefaultRemoteBackendToNone(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "local-dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "")

	got := CurrentRuntimeOptions()
	if got.ExecutionTransport != ExecutionTransportRemote || got.SandboxBackend != SandboxBackendNone {
		t.Fatalf("CurrentRuntimeOptions() = %+v", got)
	}
}

func TestUsesDedicatedWorkRootMatchesRuntimeShape(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		target    string
		transport string
		backend   string
		want      bool
	}{
		{name: "cloudrun helper", mode: "cloudrun", want: true},
		{name: "selfhosted helper", target: "selfhosted", transport: "embedded", backend: "helper", want: true},
		{name: "selfhosted remote", target: "selfhosted", transport: "remote", want: false},
		{name: "dev helper", mode: "local-dev", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AONOHAKO_EXECUTION_MODE", tc.mode)
			t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", tc.target)
			t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", tc.transport)
			t.Setenv("AONOHAKO_SANDBOX_BACKEND", tc.backend)
			if got := UsesDedicatedWorkRoot(); got != tc.want {
				t.Fatalf("UsesDedicatedWorkRoot() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequiresRootForExecutionMatchesBackend(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		backend   string
		want      bool
	}{
		{name: "embedded helper", transport: "embedded", backend: "helper", want: true},
		{name: "remote none", transport: "remote", backend: "none", want: false},
		{name: "embedded container", transport: "embedded", backend: "container", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
			t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", tc.transport)
			t.Setenv("AONOHAKO_SANDBOX_BACKEND", tc.backend)
			if got := RequiresRootForExecution(); got != tc.want {
				t.Fatalf("RequiresRootForExecution() = %v, want %v", got, tc.want)
			}
		})
	}
}
