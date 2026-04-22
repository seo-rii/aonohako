package docscontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/platform"
)

func TestPayloadDocMatchesRuntimeLimitsAndModes(t *testing.T) {
	body := mustRead(t, filepath.Join("..", "..", "docs", "payload.md"))

	wants := []string{
		`"exec" → chmod 0555; otherwise chmod 0444`,
		`Accepted|Wrong Answer|Time Limit Exceeded|Memory Limit Exceeded|Workspace Limit Exceeded|Runtime Error|Container Initialization Failed`,
		"`prlimit --as` | Virtual address space (memory_mb + 64 MB, min 512 MB)",
		"`prlimit --fsize` | Max file size (workspace_bytes when set, otherwise 128 MB)",
		`"sources": [                               // source files to compile (max 512 entries)`,
		`"binaries": [                              // files to place in work directory (max 512 entries)`,
		`"sidecar_outputs": [                       // capture extra files after execution (max 64 paths)`,
		"at most one path is supported",
		"capture failure is reported as `Runtime Error`",
	}

	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("payload.md missing %q", want)
		}
	}
}

func TestProtocolAndArchitectureDocsMatchQueueLoggingAndFDSemantics(t *testing.T) {
	protocol := mustRead(t, filepath.Join("..", "..", "docs", "protocol.md"))
	architecture := mustRead(t, filepath.Join("..", "..", "docs", "architecture.md"))

	protocolWants := []string{
		"Both `/compile` and `/execute` share the same bounded queue",
		"buffered stdout / stderr payloads emitted before `result`",
		"forwards `log`, `image`, `error`, and `result`",
		"Workspace Limit Exceeded",
		"truncated stdout (up to `limits.output_bytes`; default `64 KiB`, hard cap `8 MiB`)",
		"`AONOHAKO_DEPLOYMENT_TARGET=cloudrun`",
		"`embedded + helper`, also `1` in `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`",
		"backend rejects values\n  other than `1`",
	}
	for _, want := range protocolWants {
		if !strings.Contains(protocol, want) {
			t.Fatalf("protocol.md missing %q", want)
		}
	}

	if !strings.Contains(architecture, "`CloseRange(3, ..., CLOSE_RANGE_CLOEXEC)` fallback `CloseOnExec` loop") {
		t.Fatalf("architecture.md must describe CLOEXEC fd inheritance behavior")
	}
	if !strings.Contains(architecture, "ships shared scratch paths such as `/tmp`, `/var/tmp`, and `/run/lock`") || !strings.Contains(architecture, "entrypoint no longer mutates") {
		t.Fatalf("architecture.md must describe static scratch hardening without startup mutation")
	}
	if !strings.Contains(architecture, "Server startup validates the deployment contract instead of trusting docs alone.") || !strings.Contains(architecture, "The following checks are enforced before the HTTP server starts") {
		t.Fatalf("architecture.md must describe startup deployment contract validation")
	}
	if !strings.Contains(architecture, "`embedded + helper` also requires `AONOHAKO_MAX_ACTIVE_RUNS=1`") {
		t.Fatalf("architecture.md must describe serialized helper execution")
	}
	if !strings.Contains(architecture, "API/control-plane instances in `dev + remote + none`") || !strings.Contains(architecture, "horizontal scale by adding runner instances") {
		t.Fatalf("architecture.md must describe the self-hosted scale-out path")
	}
}

func TestReadmeDocumentsExplicitExecutionModeContract(t *testing.T) {
	readme := mustRead(t, filepath.Join("..", "..", "README.md"))

	for _, want := range []string{
		"`AONOHAKO_DEPLOYMENT_TARGET` selects where the server is meant to run",
		"`AONOHAKO_EXECUTION_TRANSPORT` selects how `/execute` is handled",
		"`AONOHAKO_SANDBOX_BACKEND` selects the local sandbox implementation",
		"`AONOHAKO_EXECUTION_MODE` remains as a compatibility shorthand",
		"`AONOHAKO_WORK_ROOT` points compile/run directories at a dedicated work root",
		"`AONOHAKO_REMOTE_RUNNER_URL` points `remote` execution at another",
		"`embedded + helper` backend rejects values other than `1`",
		"`cloudrun + embedded + helper` is the supported production security target",
		"`dev + remote + none` is the non-root development path",
		"[docs/selfhosted.md](docs/selfhosted.md)",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("README.md missing %q", want)
		}
	}
}

func TestReadmeExecutionModeNarrativeMatchesRuntimeBehavior(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "")
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "")
	t.Setenv("AONOHAKO_WORK_ROOT", "")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "")
	t.Setenv("K_SERVICE", "")
	t.Setenv("CLOUD_RUN_JOB", "")
	t.Setenv("CLOUD_RUN_WORKER_POOL", "")

	gotMode, err := platform.CurrentExecutionMode()
	if err != nil {
		t.Fatalf("CurrentExecutionMode() error = %v", err)
	}
	if gotMode != platform.ExecutionModeLocalDev {
		t.Fatalf("CurrentExecutionMode() = %q, want local-dev default", gotMode)
	}
	gotOptions, err := platform.CurrentRuntimeOptions()
	if err != nil {
		t.Fatalf("CurrentRuntimeOptions() error = %v", err)
	}
	if gotOptions.DeploymentTarget != platform.DeploymentTargetDev || gotOptions.ExecutionTransport != platform.ExecutionTransportEmbedded || gotOptions.SandboxBackend != platform.SandboxBackendHelper {
		t.Fatalf("CurrentRuntimeOptions() = %+v", gotOptions)
	}

	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() in remote dev mode: %v", err)
	}
	if cfg.MaxActiveRuns < 1 {
		t.Fatalf("config.Load() returned invalid MaxActiveRuns: %+v", cfg)
	}
	if cfg.Execution.Platform.ExecutionTransport != platform.ExecutionTransportRemote || cfg.Execution.Platform.SandboxBackend != platform.SandboxBackendNone {
		t.Fatalf("config.Load() returned wrong remote execution shape: %+v", cfg.Execution.Platform)
	}

	t.Setenv("K_SERVICE", "aonohako")
	if _, err := config.Load(); err == nil {
		t.Fatalf("config.Load() should reject Cloud Run markers without AONOHAKO_DEPLOYMENT_TARGET=cloudrun")
	}

	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "cloudrun")
	t.Setenv("K_SERVICE", "")
	if _, err := config.Load(); err == nil {
		t.Fatalf("config.Load() should require AONOHAKO_WORK_ROOT in cloudrun target")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
