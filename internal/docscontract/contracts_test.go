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
		"`sources` may contain multiple files",
		"`entry_point` names a source path, it must exactly match one submitted\nsource",
		"`binaries` may contain multiple files",
		"`limits.time_ms` and `limits.memory_mb` are required and bounded at the API\nboundary",
		"`spj.limits` uses the same\nupper caps",
		"`entry_point` must be a submitted file path and selects the\nprimary file to execute",
		"For Java, Scala, Groovy, and Erlang, `entry_point`\nkeeps its existing class/module meaning",
		"JVM\nclass names are validated",
		"| PYTHON3 | `python` | `python3 -I -S -m compileall` |",
		"| PYPY3 | `pypy` | `pypy3 -I -S -m compileall` |",
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
		"`AONOHAKO_MAX_ACTIVE_STREAMS`",
		"`AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS`",
		"`AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE`",
		`"stream_limit_exceeded"`,
		`"principal_stream_limit_exceeded"`,
		`"principal_rate_limited"`,
		"buffered stdout / stderr payloads emitted before `result`",
		"keeps the same SSE contract for `/compile` and `/execute`",
		"forwards `log`, `image`, `error`, and `result`",
		"Workspace Limit Exceeded",
		"truncated stdout (up to `limits.output_bytes`; default `64 KiB`, hard cap `8 MiB`)",
		"`AONOHAKO_DEPLOYMENT_TARGET=cloudrun`",
		"`embedded + helper`, also `1` in `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`",
		"backend rejects values\n  other than `1`",
		"fail server\nstartup instead of silently falling back",
	}
	for _, want := range protocolWants {
		if !strings.Contains(protocol, want) {
			t.Fatalf("protocol.md missing %q", want)
		}
	}

	if !strings.Contains(architecture, "`CloseRange(3, ..., CLOSE_RANGE_CLOEXEC)` fallback `CloseOnExec` loop") {
		t.Fatalf("architecture.md must describe CLOEXEC fd inheritance behavior")
	}
	if !strings.Contains(architecture, "passes the helper request JSON through an inherited pipe file descriptor") || !strings.Contains(architecture, "does not materialize the helper request as a\nworkspace file") {
		t.Fatalf("architecture.md must describe helper request fd delivery")
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
	if !strings.Contains(architecture, "`container` is recognized only as a reserved future backend value") {
		t.Fatalf("architecture.md must describe reserved container backend semantics")
	}
	if !strings.Contains(architecture, "`embedded-helper-process-hardening`") || !strings.Contains(architecture, "`remote-control-plane`") || !strings.Contains(architecture, "`reserved-container-isolation`") {
		t.Fatalf("architecture.md must describe named runtime security contracts")
	}
	if !strings.Contains(architecture, "per-run cgroup, mount namespace, read-only rootfs, masked `/proc`, per-run UID") {
		t.Fatalf("architecture.md must describe missing helper isolation boundaries")
	}
	if !strings.Contains(architecture, "post-start `execve()` blocking") {
		t.Fatalf("architecture.md must include post-start execve blocking in security contract gaps")
	}
	if !strings.Contains(architecture, "`internal/isolation/cgroup` currently checks") || !strings.Contains(architecture, "required `cpu`, `memory`, and `pids` controllers") {
		t.Fatalf("architecture.md must describe cgroup v2 preflight requirements")
	}
	if !strings.Contains(architecture, "positive `memory.max` and\n`pids.max` values") || !strings.Contains(architecture, "writing its PID to `cgroup.procs`") {
		t.Fatalf("architecture.md must describe cgroup run-group write contract")
	}
	if !strings.Contains(architecture, "reads `memory.current`,\n`memory.peak` when present, `memory.events`, `pids.current`, and `cpu.stat`") || !strings.Contains(architecture, "`oom_group_kill` counters") {
		t.Fatalf("architecture.md must describe cgroup accounting read contract")
	}
	if !strings.Contains(architecture, "unsupported runtime security contracts fail startup before request handling") {
		t.Fatalf("architecture.md must describe fail-closed security contract validation")
	}
	if !strings.Contains(architecture, "malformed or out-of-range") || !strings.Contains(architecture, "values fail startup") {
		t.Fatalf("architecture.md must describe strict numeric env parsing")
	}
	if !strings.Contains(architecture, "`AONOHAKO_MAX_ACTIVE_STREAMS`") {
		t.Fatalf("architecture.md must describe active stream cap validation")
	}
	if !strings.Contains(architecture, "`AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS`") || !strings.Contains(architecture, "token fingerprint as the\n  principal key") {
		t.Fatalf("architecture.md must describe per-principal stream caps")
	}
	if !strings.Contains(architecture, "`AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE`") || !strings.Contains(architecture, "fixed one-minute window") {
		t.Fatalf("architecture.md must describe per-principal request-rate caps")
	}
	if !strings.Contains(architecture, "API/control-plane instances in `dev + remote + none`") || !strings.Contains(architecture, "horizontal scale by adding runner instances") {
		t.Fatalf("architecture.md must describe the self-hosted scale-out path")
	}
	if !strings.Contains(architecture, "`cloudrun + remote + none`: supported Cloud Run control-plane target") {
		t.Fatalf("architecture.md must describe the Cloud Run remote control-plane topology")
	}
	if !strings.Contains(architecture, "both `/compile` and `/execute` are\nforwarded to the downstream runner") {
		t.Fatalf("architecture.md must describe remoteized compile and execute paths")
	}
	if !strings.Contains(architecture, "submitted source files are made immutable (`0444`)") || !strings.Contains(architecture, "Python-like compile checks run in isolated startup mode (`-I -S`)") {
		t.Fatalf("architecture.md must describe compile workspace immutability and isolated Python startup")
	}
	if !strings.Contains(architecture, "`socket()` is limited to `AF_INET` and `AF_INET6`") || !strings.Contains(architecture, "Cloud Run embedded-helper execution rejects `enable_network=true` outright") {
		t.Fatalf("architecture.md must describe the network-enabled helper boundary")
	}
	if !strings.Contains(architecture, "post-start\n`execve()` surface") || !strings.Contains(architecture, "world-executable binary that is present in the\nruntime image") {
		t.Fatalf("architecture.md must describe the remaining execve image surface")
	}
	if !strings.Contains(architecture, "treat every world-executable binary in the runtime image as reachable by\nsubmissions") || !strings.Contains(architecture, "shells, package\nmanagers, compilers, debuggers, and diagnostics tooling") {
		t.Fatalf("architecture.md must describe runtime image minimization for execve exposure")
	}
	if !strings.Contains(architecture, "prevention of replacing the running process with another world-executable\n  binary from the runtime image") {
		t.Fatalf("architecture.md must list execve replacement as a current non-goal")
	}
}

func TestReadmeDocumentsExplicitExecutionModeContract(t *testing.T) {
	readme := mustRead(t, filepath.Join("..", "..", "README.md"))

	for _, want := range []string{
		"`AONOHAKO_DEPLOYMENT_TARGET` selects where the server is meant to run",
		"`AONOHAKO_EXECUTION_TRANSPORT` selects how `/compile` and `/execute` are",
		"`AONOHAKO_SANDBOX_BACKEND` selects the local sandbox implementation",
		"`container` is a reserved enum value for a future",
		"`embedded-helper-process-hardening`, `remote-control-plane`, and reserved",
		"not per-run cgroup, mount-namespace, or post-start `execve()` isolation",
		"fail startup instead of falling back",
		"`AONOHAKO_EXECUTION_MODE` remains as a compatibility shorthand",
		"non-root development path)",
		"`AONOHAKO_MAX_ACTIVE_STREAMS` defaults to `64`",
		"`AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS` defaults to `0` for `dev`",
		"`AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE` defaults to `0` for `dev`",
		"`AONOHAKO_WORK_ROOT` points compile/run directories at a dedicated work root",
		"`AONOHAKO_REMOTE_RUNNER_URL` points `remote` transport at another",
		"`embedded + helper` backend rejects values other than `1`",
		"`cloudrun + embedded + helper` is the supported production security target",
		"`cloudrun + remote + none` is the supported Cloud Run control-plane shape",
		"`dev + remote + none` is the non-root development path",
		"forwards `/compile` and `/execute` to a remote hardened runner",
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
