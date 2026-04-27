package config

import (
	"os"
	"path/filepath"
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

func TestDefaultMaxPrincipalStreams(t *testing.T) {
	if got := defaultMaxPrincipalStreams(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev}); got != 0 {
		t.Fatalf("dev default principal stream cap = %d, want 0", got)
	}
	if got := defaultMaxPrincipalStreams(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetCloudRun}); got != 16 {
		t.Fatalf("cloudrun default principal stream cap = %d, want 16", got)
	}
	if got := defaultMaxPrincipalStreams(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetSelfHosted}); got != 16 {
		t.Fatalf("selfhosted default principal stream cap = %d, want 16", got)
	}
}

func TestDefaultMaxPrincipalRequestsPerMinute(t *testing.T) {
	if got := defaultMaxPrincipalRequestsPerMinute(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev}); got != 0 {
		t.Fatalf("dev default principal request rate = %d, want 0", got)
	}
	if got := defaultMaxPrincipalRequestsPerMinute(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetCloudRun}); got != 60 {
		t.Fatalf("cloudrun default principal request rate = %d, want 60", got)
	}
	if got := defaultMaxPrincipalRequestsPerMinute(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetSelfHosted}); got != 60 {
		t.Fatalf("selfhosted default principal request rate = %d, want 60", got)
	}
}

func TestDefaultAllowRequestNetwork(t *testing.T) {
	if !defaultAllowRequestNetwork(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev}) {
		t.Fatalf("dev should allow request-controlled network by default")
	}
	if defaultAllowRequestNetwork(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetSelfHosted}) {
		t.Fatalf("selfhosted should require explicit network policy opt-in")
	}
	if defaultAllowRequestNetwork(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetCloudRun}) {
		t.Fatalf("cloudrun should reject request-controlled network by default")
	}
}

func TestDefaultTrustedRunnerIngress(t *testing.T) {
	if !defaultTrustedRunnerIngress(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev}) {
		t.Fatalf("dev should trust runner ingress by default")
	}
	if !defaultTrustedRunnerIngress(platform.RuntimeOptions{
		DeploymentTarget:   platform.DeploymentTargetSelfHosted,
		ExecutionTransport: platform.ExecutionTransportRemote,
	}) {
		t.Fatalf("remote control planes should delegate ingress trust to the runner boundary")
	}
	if defaultTrustedRunnerIngress(platform.RuntimeOptions{
		DeploymentTarget:   platform.DeploymentTargetSelfHosted,
		ExecutionTransport: platform.ExecutionTransportEmbedded,
		SandboxBackend:     platform.SandboxBackendHelper,
	}) {
		t.Fatalf("non-dev embedded helper should require explicit trusted ingress assertion")
	}
}

func TestDefaultTrustedPlatformHeaders(t *testing.T) {
	if !defaultTrustedPlatformHeaders(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev}) {
		t.Fatalf("dev should trust platform headers by default")
	}
	if defaultTrustedPlatformHeaders(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetCloudRun}) {
		t.Fatalf("cloudrun should require explicit trusted platform header assertion")
	}
	if defaultTrustedPlatformHeaders(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetSelfHosted}) {
		t.Fatalf("selfhosted should require explicit trusted platform header assertion")
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
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "true")

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

func TestLoadRuntimeTuningConfig(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "none")
	t.Setenv("AONOHAKO_JVM_HEAP_PERCENT", "40")
	t.Setenv("AONOHAKO_GO_MEMORY_RESERVE_MB", "64")
	t.Setenv("AONOHAKO_GO_GOGC", "80")
	t.Setenv("AONOHAKO_ERLANG_SCHEDULERS", "2")
	t.Setenv("AONOHAKO_ERLANG_ASYNC_THREADS", "3")
	t.Setenv("AONOHAKO_DOTNET_GC_HEAP_PERCENT", "55")
	t.Setenv("AONOHAKO_KOTLIN_NATIVE_COMPILER_HEAP_MB", "768")
	t.Setenv("AONOHAKO_NODE_OLD_SPACE_PERCENT", "50")
	t.Setenv("AONOHAKO_NODE_MAX_SEMI_SPACE_MB", "2")
	t.Setenv("AONOHAKO_NODE_STACK_SIZE_KB", "1024")
	t.Setenv("AONOHAKO_WASMTIME_MEMORY_GUARD_BYTES", "131072")
	t.Setenv("AONOHAKO_WASMTIME_MAX_WASM_STACK_BYTES", "524288")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := RuntimeTuningConfig{
		JVMHeapPercent:             40,
		GoMemoryReserveMB:          64,
		GoGOGC:                     80,
		ErlangSchedulers:           2,
		ErlangAsyncThreads:         3,
		DotnetGCHeapPercent:        55,
		KotlinNativeCompilerHeapMB: 768,
		NodeOldSpacePercent:        50,
		NodeMaxSemiSpaceMB:         2,
		NodeStackSizeKB:            1024,
		WasmtimeMemoryGuardBytes:   131072,
		WasmtimeMaxWasmStackBytes:  524288,
	}
	if cfg.Execution.RuntimeTuning != want {
		t.Fatalf("runtime tuning = %+v, want %+v", cfg.Execution.RuntimeTuning, want)
	}
}

func TestLoadRejectsUnsafeRuntimeTuningConfig(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "AONOHAKO_JVM_HEAP_PERCENT", value: "90"},
		{key: "AONOHAKO_GO_MEMORY_RESERVE_MB", value: "999"},
		{key: "AONOHAKO_GO_GOGC", value: "5"},
		{key: "AONOHAKO_ERLANG_SCHEDULERS", value: "8"},
		{key: "AONOHAKO_ERLANG_ASYNC_THREADS", value: "8"},
		{key: "AONOHAKO_DOTNET_GC_HEAP_PERCENT", value: "90"},
		{key: "AONOHAKO_KOTLIN_NATIVE_COMPILER_HEAP_MB", value: "2048"},
		{key: "AONOHAKO_NODE_OLD_SPACE_PERCENT", value: "90"},
		{key: "AONOHAKO_NODE_MAX_SEMI_SPACE_MB", value: "64"},
		{key: "AONOHAKO_NODE_STACK_SIZE_KB", value: "64"},
		{key: "AONOHAKO_WASMTIME_MEMORY_GUARD_BYTES", value: "1024"},
		{key: "AONOHAKO_WASMTIME_MAX_WASM_STACK_BYTES", value: "67108864"},
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
			t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
			t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
			t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "none")
			t.Setenv(tc.key, tc.value)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("expected %s validation error, got %v", tc.key, err)
			}
		})
	}
}

func TestRuntimeTuningWithSafeDefaultsClampsManualConfig(t *testing.T) {
	got := (RuntimeTuningConfig{
		JVMHeapPercent:             1,
		GoMemoryReserveMB:          999,
		GoGOGC:                     1,
		ErlangSchedulers:           99,
		ErlangAsyncThreads:         99,
		DotnetGCHeapPercent:        1,
		KotlinNativeCompilerHeapMB: 1,
		NodeOldSpacePercent:        1,
		NodeMaxSemiSpaceMB:         99,
		NodeStackSizeKB:            64,
		WasmtimeMemoryGuardBytes:   1,
		WasmtimeMaxWasmStackBytes:  99 << 20,
	}).WithSafeDefaults()

	if got.NodeOldSpacePercent != minNodeOldSpacePercent {
		t.Fatalf("NodeOldSpacePercent = %d, want %d", got.NodeOldSpacePercent, minNodeOldSpacePercent)
	}
	if got.JVMHeapPercent != minJVMHeapPercent {
		t.Fatalf("JVMHeapPercent = %d, want %d", got.JVMHeapPercent, minJVMHeapPercent)
	}
	if got.GoMemoryReserveMB != maxGoMemoryReserveMB {
		t.Fatalf("GoMemoryReserveMB = %d, want %d", got.GoMemoryReserveMB, maxGoMemoryReserveMB)
	}
	if got.GoGOGC != minGoGOGC {
		t.Fatalf("GoGOGC = %d, want %d", got.GoGOGC, minGoGOGC)
	}
	if got.ErlangSchedulers != maxErlangSchedulers {
		t.Fatalf("ErlangSchedulers = %d, want %d", got.ErlangSchedulers, maxErlangSchedulers)
	}
	if got.ErlangAsyncThreads != maxErlangAsyncThreads {
		t.Fatalf("ErlangAsyncThreads = %d, want %d", got.ErlangAsyncThreads, maxErlangAsyncThreads)
	}
	if got.DotnetGCHeapPercent != minDotnetGCHeapPercent {
		t.Fatalf("DotnetGCHeapPercent = %d, want %d", got.DotnetGCHeapPercent, minDotnetGCHeapPercent)
	}
	if got.KotlinNativeCompilerHeapMB != minKotlinNativeCompilerHeapMB {
		t.Fatalf("KotlinNativeCompilerHeapMB = %d, want %d", got.KotlinNativeCompilerHeapMB, minKotlinNativeCompilerHeapMB)
	}
	if got.NodeMaxSemiSpaceMB != maxNodeMaxSemiSpaceMB {
		t.Fatalf("NodeMaxSemiSpaceMB = %d, want %d", got.NodeMaxSemiSpaceMB, maxNodeMaxSemiSpaceMB)
	}
	if got.NodeStackSizeKB != minNodeStackSizeKB {
		t.Fatalf("NodeStackSizeKB = %d, want %d", got.NodeStackSizeKB, minNodeStackSizeKB)
	}
	if got.WasmtimeMemoryGuardBytes != minWasmtimeMemoryGuardBytes {
		t.Fatalf("WasmtimeMemoryGuardBytes = %d, want %d", got.WasmtimeMemoryGuardBytes, minWasmtimeMemoryGuardBytes)
	}
	if got.WasmtimeMaxWasmStackBytes != maxWasmtimeMaxWasmStackBytes {
		t.Fatalf("WasmtimeMaxWasmStackBytes = %d, want %d", got.WasmtimeMaxWasmStackBytes, maxWasmtimeMaxWasmStackBytes)
	}
}

func TestLoadRejectsRemoteAuthNoneOutsideDev(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "none")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AONOHAKO_REMOTE_RUNNER_AUTH=none") {
		t.Fatalf("expected remote auth none rejection outside dev, got %v", err)
	}
}

func TestLoadAllowsCloudRunRemoteControlPlaneWithWorkRoot(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "true")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Execution.Platform.DeploymentTarget != platform.DeploymentTargetCloudRun {
		t.Fatalf("deployment target mismatch: %+v", cfg.Execution.Platform)
	}
	if cfg.Execution.Platform.ExecutionTransport != platform.ExecutionTransportRemote {
		t.Fatalf("execution transport mismatch: %+v", cfg.Execution.Platform)
	}
	if cfg.Execution.Platform.SandboxBackend != platform.SandboxBackendNone {
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
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "true")
	t.Setenv("AONOHAKO_TRUSTED_RUNNER_INGRESS", "true")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())

	if _, err := Load(); err == nil {
		t.Fatalf("expected embedded helper execution to require root")
	}
}

func TestLoadRejectsEmbeddedHelperWithoutTrustedIngressOutsideDev(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "embedded")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "helper")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "true")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AONOHAKO_TRUSTED_RUNNER_INGRESS=true") {
		t.Fatalf("expected trusted runner ingress assertion error, got %v", err)
	}
}

func TestLoadRejectsEmbeddedHelperWithParallelActiveRuns(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "embedded")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "helper")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "true")
	t.Setenv("AONOHAKO_TRUSTED_RUNNER_INGRESS", "true")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	t.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "2")

	if _, err := Load(); err == nil {
		t.Fatalf("expected embedded helper execution to reject parallel active runs")
	}
}

func TestLoadRejectsCgroupParentOutsideSelfHostedHelper(t *testing.T) {
	parent := t.TempDir()
	os.WriteFile(filepath.Join(parent, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o644)
	os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte(""), 0o644)
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_CGROUP_PARENT", parent)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AONOHAKO_CGROUP_PARENT is supported only with selfhosted embedded helper execution") {
		t.Fatalf("expected cgroup parent topology rejection, got %v", err)
	}
}

func TestLoadRejectsInvalidCgroupParent(t *testing.T) {
	parent := t.TempDir()
	os.WriteFile(filepath.Join(parent, "cgroup.controllers"), []byte("cpu memory\n"), 0o644)
	os.WriteFile(filepath.Join(parent, "cgroup.subtree_control"), []byte(""), 0o644)
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "embedded")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "helper")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "true")
	t.Setenv("AONOHAKO_TRUSTED_RUNNER_INGRESS", "true")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	t.Setenv("AONOHAKO_CGROUP_PARENT", parent)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "missing pids controller") {
		t.Fatalf("expected invalid cgroup parent rejection, got %v", err)
	}
}

func TestLoadRejectsReservedContainerBackend(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "embedded")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "container")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")

	_, err := Load()
	if err == nil {
		t.Fatalf("expected reserved container backend to be rejected")
	}
	if !strings.Contains(err.Error(), "reserved-container-isolation") {
		t.Fatalf("reserved backend error should name the contract, got %v", err)
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
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "true")
	t.Setenv("AONOHAKO_WORK_ROOT", root)

	if _, err := Load(); err == nil {
		t.Fatalf("expected group-writable dedicated work root to be rejected")
	}
}

func TestLoadUsesConfiguredNumericEnv(t *testing.T) {
	t.Setenv("PORT", "18080")
	t.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "3")
	t.Setenv("AONOHAKO_MAX_PENDING_QUEUE", "7")
	t.Setenv("AONOHAKO_MAX_ACTIVE_STREAMS", "11")
	t.Setenv("AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS", "5")
	t.Setenv("AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE", "13")
	t.Setenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", "2")
	t.Setenv("AONOHAKO_BODY_READ_TIMEOUT_SEC", "9")
	t.Setenv("AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC", "4")
	t.Setenv("AONOHAKO_ALLOW_REQUEST_NETWORK", "true")
	t.Setenv("AONOHAKO_TRUSTED_RUNNER_INGRESS", "false")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "false")
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
	if cfg.MaxActiveStreams != 11 {
		t.Fatalf("max active streams mismatch: %d", cfg.MaxActiveStreams)
	}
	if cfg.MaxPrincipalStreams != 5 {
		t.Fatalf("max principal streams mismatch: %d", cfg.MaxPrincipalStreams)
	}
	if cfg.MaxPrincipalRequestsPerMinute != 13 {
		t.Fatalf("max principal requests mismatch: %d", cfg.MaxPrincipalRequestsPerMinute)
	}
	if cfg.HeartbeatInterval != 2*time.Second {
		t.Fatalf("heartbeat mismatch: %v", cfg.HeartbeatInterval)
	}
	if cfg.BodyReadTimeout != 9*time.Second {
		t.Fatalf("body read timeout mismatch: %v", cfg.BodyReadTimeout)
	}
	if cfg.Execution.Remote.SSEIdleTimeout != 4*time.Second {
		t.Fatalf("remote SSE idle timeout mismatch: %v", cfg.Execution.Remote.SSEIdleTimeout)
	}
	if !cfg.AllowRequestNetwork {
		t.Fatalf("allow request network should be parsed from env")
	}
	if cfg.TrustedRunnerIngress {
		t.Fatalf("trusted runner ingress should be parsed from env")
	}
	if cfg.TrustedPlatformHeaders {
		t.Fatalf("trusted platform headers should be parsed from env")
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
		{name: "streams negative", key: "AONOHAKO_MAX_ACTIVE_STREAMS", value: "-1"},
		{name: "streams malformed", key: "AONOHAKO_MAX_ACTIVE_STREAMS", value: "many"},
		{name: "principal streams negative", key: "AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS", value: "-1"},
		{name: "principal streams malformed", key: "AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS", value: "many"},
		{name: "principal rpm negative", key: "AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE", value: "-1"},
		{name: "principal rpm malformed", key: "AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE", value: "many"},
		{name: "heartbeat zero", key: "AONOHAKO_HEARTBEAT_INTERVAL_SEC", value: "0"},
		{name: "heartbeat negative", key: "AONOHAKO_HEARTBEAT_INTERVAL_SEC", value: "-1"},
		{name: "heartbeat malformed", key: "AONOHAKO_HEARTBEAT_INTERVAL_SEC", value: "soon"},
		{name: "body read timeout zero", key: "AONOHAKO_BODY_READ_TIMEOUT_SEC", value: "0"},
		{name: "body read timeout negative", key: "AONOHAKO_BODY_READ_TIMEOUT_SEC", value: "-1"},
		{name: "body read timeout malformed", key: "AONOHAKO_BODY_READ_TIMEOUT_SEC", value: "soon"},
		{name: "remote sse idle zero", key: "AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC", value: "0"},
		{name: "remote sse idle negative", key: "AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC", value: "-1"},
		{name: "remote sse idle malformed", key: "AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC", value: "soon"},
		{name: "allow network malformed", key: "AONOHAKO_ALLOW_REQUEST_NETWORK", value: "sometimes"},
		{name: "trusted runner ingress malformed", key: "AONOHAKO_TRUSTED_RUNNER_INGRESS", value: "sometimes"},
		{name: "trusted platform headers malformed", key: "AONOHAKO_TRUSTED_PLATFORM_HEADERS", value: "sometimes"},
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
	if cfg.MaxPendingQueue != defaultMaxPendingQueue {
		t.Fatalf("legacy max pending env should be ignored, got %d", cfg.MaxPendingQueue)
	}
	if cfg.MaxActiveStreams != defaultMaxActiveStreams {
		t.Fatalf("legacy max active streams env should be ignored, got %d", cfg.MaxActiveStreams)
	}
	if cfg.MaxPrincipalStreams != defaultMaxPrincipalStreams(cfg.Execution.Platform) {
		t.Fatalf("legacy max principal streams env should be ignored, got %d", cfg.MaxPrincipalStreams)
	}
	if cfg.MaxPrincipalRequestsPerMinute != defaultMaxPrincipalRequestsPerMinute(cfg.Execution.Platform) {
		t.Fatalf("legacy max principal request rate env should be ignored, got %d", cfg.MaxPrincipalRequestsPerMinute)
	}
	if cfg.HeartbeatInterval != 10*time.Second {
		t.Fatalf("legacy heartbeat env should be ignored, got %v", cfg.HeartbeatInterval)
	}
}

func TestLoadDefaultsInboundAuthByDeploymentTarget(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "")

	devCfg, err := Load()
	if err != nil {
		t.Fatalf("Load dev config: %v", err)
	}
	if devCfg.InboundAuth.Mode != InboundAuthNone {
		t.Fatalf("dev inbound auth default = %q, want none", devCfg.InboundAuth.Mode)
	}

	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AONOHAKO_API_BEARER_TOKEN") {
		t.Fatalf("cloudrun default should require bearer token, got %v", err)
	}

	t.Setenv("AONOHAKO_API_BEARER_TOKEN", "secret")
	cloudCfg, err := Load()
	if err != nil {
		t.Fatalf("Load cloudrun config with token: %v", err)
	}
	if cloudCfg.InboundAuth.Mode != InboundAuthBearer {
		t.Fatalf("cloudrun inbound auth default = %q, want bearer", cloudCfg.InboundAuth.Mode)
	}
}

func TestLoadAllowsExplicitPlatformInboundAuth(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.InboundAuth.Mode != InboundAuthPlatform {
		t.Fatalf("inbound auth mode = %q, want platform", cfg.InboundAuth.Mode)
	}
}

func TestLoadAllowsPlatformInboundAuthWithPrincipalHMAC(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")
	t.Setenv("AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.InboundAuth.Mode != InboundAuthPlatform {
		t.Fatalf("inbound auth mode = %q, want platform", cfg.InboundAuth.Mode)
	}
	if cfg.InboundAuth.PlatformPrincipalHMACSecret != "secret" {
		t.Fatalf("platform HMAC secret was not loaded")
	}
}

func TestLoadRejectsPlatformInboundAuthWithoutTrustedHeadersOutsideDev(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	t.Setenv("AONOHAKO_INBOUND_AUTH", "platform")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AONOHAKO_TRUSTED_PLATFORM_HEADERS=true") {
		t.Fatalf("expected trusted platform header assertion error, got %v", err)
	}
}

func TestLoadRejectsInboundAuthNoneOutsideDev(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "selfhosted")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "cloudrun-idtoken")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "none")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AONOHAKO_INBOUND_AUTH=none") {
		t.Fatalf("expected inbound none rejection outside dev, got %v", err)
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
