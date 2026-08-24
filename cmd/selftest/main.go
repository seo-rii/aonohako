package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/api"
	"aonohako/internal/compile"
	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/isolation/cgroup"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/processhardening"
	"aonohako/internal/profiles"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/sandbox"
	"aonohako/internal/security"

	"golang.org/x/sys/unix"
)

type suiteCase struct {
	name  string
	req   model.RunRequest
	check func(model.RunResponse) error
}

type judgeIOCase struct {
	stdin          string
	expectedStdout string
	a              int
	b              int
}

var (
	standardABJudgeIO = []judgeIOCase{
		{stdin: "20 22\n", expectedStdout: "42\n", a: 20, b: 22},
		{stdin: "7 13\n", expectedStdout: "20\n", a: 7, b: 13},
	}
	lineSeparatedABJudgeIO = []judgeIOCase{
		{stdin: "20\n22\n", expectedStdout: "42\n", a: 20, b: 22},
		{stdin: "7\n13\n", expectedStdout: "20\n", a: 7, b: 13},
	}
	singleDigitABJudgeIO = []judgeIOCase{
		{stdin: "1 2\n", expectedStdout: "3\n", a: 1, b: 2},
		{stdin: "3 4\n", expectedStdout: "7\n", a: 3, b: 4},
	}
	twoDigitABJudgeIO = []judgeIOCase{
		{stdin: "20 22\n", expectedStdout: "42\n", a: 20, b: 22},
		{stdin: "10 13\n", expectedStdout: "23\n", a: 10, b: 13},
	}
	sqlABJudgeIO = []judgeIOCase{
		{
			stdin:          "create table input(a integer, b integer);\ninsert into input values (20, 22);\n",
			expectedStdout: "42\n",
			a:              20,
			b:              22,
		},
		{
			stdin:          "create table input(a integer, b integer);\ninsert into input values (7, 13);\n",
			expectedStdout: "20\n",
			a:              7,
			b:              13,
		},
	}
)

type compileExecuteCase struct {
	compileLang       string
	compileVariants   []string
	compileAttempts   int
	entryPoint        string
	stdin             string
	expectedStdout    string
	judgeIO           []judgeIOCase
	nonABReason       string
	limits            model.Limits
	sources           []model.Source
	pythonLibraryMode pythonpolicy.LibraryMode
}

type languageSecurityCase struct {
	name           string
	compileLang    string
	entryPoint     string
	expectedStdout string
	limits         model.Limits
	sources        []model.Source
}

type runtimeMemoryCase struct {
	compileLang string
	memoryMB    int
	sources     []model.Source
}

func strictRuntimeMemoryCases() map[string]runtimeMemoryCase {
	source := func(name, body string) model.Source {
		return model.Source{Name: name, DataB64: encodeScript(body)}
	}
	return map[string]runtimeMemoryCase{
		"go": {
			compileLang: "GO",
			memoryMB:    64,
			sources: []model.Source{source("Main.go", `package main

var chunks [][]byte

func main() {
	for {
		chunk := make([]byte, 8*1024*1024)
		for i := 0; i < len(chunk); i += 4096 {
			chunk[i] = 1
		}
		chunks = append(chunks, chunk)
	}
}
`)},
		},
		"rust": {
			compileLang: "RUST2021",
			memoryMB:    64,
			sources: []model.Source{source("Main.rs", `fn main() {
    let mut chunks: Vec<Vec<u8>> = Vec::new();
    loop {
        chunks.push(vec![1_u8; 8 * 1024 * 1024]);
    }
}
`)},
		},
		"ruby": {
			compileLang: "RUBY",
			memoryMB:    64,
			sources: []model.Source{source("Main.rb", `chunks = []
loop do
  chunks << ("x".b * (8 * 1024 * 1024))
  sleep 0.005
end
`)},
		},
		"php": {
			compileLang: "PHP",
			memoryMB:    64,
			sources: []model.Source{source("Main.php", `<?php
$chunks = [];
while (true) {
    $chunks[] = str_repeat("x", 8 * 1024 * 1024);
    usleep(5000);
}
`)},
		},
		"lua": {
			compileLang: "LUA",
			memoryMB:    64,
			sources: []model.Source{source("Main.lua", `local chunks = {}
while true do
  chunks[#chunks + 1] = string.rep("x", 8 * 1024 * 1024)
end
`)},
		},
		"perl": {
			compileLang: "PERL",
			memoryMB:    64,
			sources: []model.Source{source("Main.pl", `use strict;
my @chunks;
while (1) {
    push @chunks, "x" x (8 * 1024 * 1024);
    select undef, undef, undef, 0.005;
}
`)},
		},
	}
}

func runtimeStartupMemoryMB() map[string]int {
	return map[string]int{
		"go":         1120,
		"rust":       64,
		"zig":        160,
		"java":       64,
		"kotlin-jvm": 1536,
		"erlang":     1088,
		"julia":      1088,
		"swift":      288,
		"dart":       288,
	}
}

const (
	mountNamespaceProbeEnv = "AONOHAKO_MOUNTNS_PREFLIGHT_PROBE"
	selftestUsage          = "usage: aonohako-selftest image-permissions|permissions|compile-security|compile-execute|two-step|language-security|runtime-memory|cgroup-preflight|mount-preflight|deployment-contract"
	twoStepStabilityRuns   = 25
)

func main() {
	if sandbox.MaybeRunFromEnv() {
		return
	}
	if os.Getenv(mountNamespaceProbeEnv) == "1" {
		if err := runMountNamespaceProbe(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := processhardening.DisableDumpability(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "process hardening failed: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) != 2 {
		_, _ = fmt.Fprintln(os.Stderr, selftestUsage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "image-permissions":
		if err := runImagePermissionsSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "permissions":
		if err := runPermissionsSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "compile-security":
		if err := runCompileSecuritySuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "compile-execute":
		if err := runCompileExecuteSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "two-step":
		if err := runTwoStepSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "language-security":
		if err := runLanguageSecuritySuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "runtime-memory":
		if err := runRuntimeMemorySuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "cgroup-preflight":
		if err := runCgroupPreflightSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "mount-preflight":
		if err := runMountPreflightSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "deployment-contract":
		if err := runDeploymentContractSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown selftest suite: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func runDeploymentContractSuite() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("deployment contract validation failed: %w", err)
	}
	contract, err := cfg.Execution.Platform.SecurityContract()
	if err != nil {
		return fmt.Errorf("security contract lookup failed: %w", err)
	}
	cgroupParentConfigured := strings.TrimSpace(cfg.Execution.Cgroup.ParentDir) != ""
	if cgroupParentConfigured {
		contract = contract.WithAvailableCapabilities(
			platform.CapabilityPerRunCgroup,
			platform.CapabilityChildProcessAccounting,
		)
	}
	summary := struct {
		DeploymentTarget              platform.DeploymentTarget     `json:"deployment_target"`
		ExecutionTransport            platform.ExecutionTransport   `json:"execution_transport"`
		SandboxBackend                platform.SandboxBackend       `json:"sandbox_backend"`
		Contract                      string                        `json:"contract"`
		ContractImplemented           bool                          `json:"contract_implemented"`
		RequiresRootParent            bool                          `json:"requires_root_parent"`
		RequiresDedicatedWorkRoot     bool                          `json:"requires_dedicated_work_root"`
		RequiresSingleActiveRun       bool                          `json:"requires_single_active_run"`
		DelegatesIsolation            bool                          `json:"delegates_isolation"`
		Capabilities                  []platform.SecurityCapability `json:"capabilities,omitempty"`
		MissingCapabilities           []platform.SecurityCapability `json:"missing_capabilities,omitempty"`
		MaxActiveRuns                 int                           `json:"max_active_runs"`
		MaxPendingQueue               int                           `json:"max_pending_queue"`
		MaxActiveStreams              int                           `json:"max_active_streams"`
		PlatformBodyHashConcurrency   int                           `json:"platform_body_hash_concurrency"`
		MaxPrincipalStreams           int                           `json:"max_principal_streams"`
		MaxPrincipalRequestsPerMinute int                           `json:"max_principal_requests_per_minute"`
		HeartbeatIntervalSec          int64                         `json:"heartbeat_interval_sec"`
		RemoteSSEIdleTimeoutSec       int64                         `json:"remote_sse_idle_timeout_sec"`
		TrustedRunnerIngress          bool                          `json:"trusted_runner_ingress"`
		TrustedPlatformHeaders        bool                          `json:"trusted_platform_headers"`
		TrustedPlatformHeaderCIDRs    []string                      `json:"trusted_platform_header_cidrs,omitempty"`
		RequireWorkRootTmpfs          bool                          `json:"require_work_root_tmpfs"`
		WorkRootMaxBytes              int                           `json:"work_root_max_bytes,omitempty"`
		WorkRootMaxFiles              int                           `json:"work_root_max_files,omitempty"`
		InboundAuth                   config.InboundAuthMode        `json:"inbound_auth"`
		PlatformPrincipalHMAC         bool                          `json:"platform_principal_hmac"`
		RemoteAuth                    config.RemoteAuthMode         `json:"remote_auth"`
		RemoteURLConfigured           bool                          `json:"remote_url_configured"`
		CgroupParentConfigured        bool                          `json:"cgroup_parent_configured"`
	}{
		DeploymentTarget:              cfg.Execution.Platform.DeploymentTarget,
		ExecutionTransport:            cfg.Execution.Platform.ExecutionTransport,
		SandboxBackend:                cfg.Execution.Platform.SandboxBackend,
		Contract:                      contract.Name,
		ContractImplemented:           contract.Implemented,
		RequiresRootParent:            contract.RequiresRootParent,
		RequiresDedicatedWorkRoot:     contract.RequiresDedicatedWorkRoot,
		RequiresSingleActiveRun:       contract.RequiresSingleActiveRun,
		DelegatesIsolation:            contract.DelegatesIsolation,
		Capabilities:                  contract.Capabilities,
		MissingCapabilities:           contract.MissingCapabilities,
		MaxActiveRuns:                 cfg.MaxActiveRuns,
		MaxPendingQueue:               cfg.MaxPendingQueue,
		MaxActiveStreams:              cfg.MaxActiveStreams,
		PlatformBodyHashConcurrency:   cfg.PlatformBodyHashConcurrency,
		MaxPrincipalStreams:           cfg.MaxPrincipalStreams,
		MaxPrincipalRequestsPerMinute: cfg.MaxPrincipalRequestsPerMinute,
		HeartbeatIntervalSec:          int64(cfg.HeartbeatInterval / time.Second),
		RemoteSSEIdleTimeoutSec:       int64(cfg.Execution.Remote.SSEIdleTimeout / time.Second),
		TrustedRunnerIngress:          cfg.TrustedRunnerIngress,
		TrustedPlatformHeaders:        cfg.TrustedPlatformHeaders,
		TrustedPlatformHeaderCIDRs:    cfg.TrustedPlatformHeaderCIDRs,
		RequireWorkRootTmpfs:          cfg.RequireWorkRootTmpfs,
		WorkRootMaxBytes:              cfg.WorkRootMaxBytes,
		WorkRootMaxFiles:              cfg.WorkRootMaxFiles,
		InboundAuth:                   cfg.InboundAuth.Mode,
		PlatformPrincipalHMAC:         strings.TrimSpace(cfg.InboundAuth.PlatformPrincipalHMACSecret) != "",
		RemoteAuth:                    cfg.Execution.Remote.Auth,
		RemoteURLConfigured:           strings.TrimSpace(cfg.Execution.Remote.URL) != "",
		CgroupParentConfigured:        cgroupParentConfigured,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func runCgroupPreflightSuite() error {
	result := cgroup.Preflight()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encode cgroup preflight result: %w", err)
	}
	if !result.Available {
		return fmt.Errorf("cgroup preflight unavailable: %s", result.Reason)
	}
	return nil
}

type mountPreflightResult struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func runMountPreflightSuite() error {
	result := probeMountNamespaceSupport()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encode mount preflight result: %w", err)
	}
	if !result.Available {
		return fmt.Errorf("mount namespace preflight unavailable: %s", result.Reason)
	}
	return nil
}

func probeMountNamespaceSupport() mountPreflightResult {
	if runtime.GOOS != "linux" {
		return mountPreflightResult{Available: false, Reason: "mount namespaces require linux"}
	}
	if os.Geteuid() != 0 {
		return mountPreflightResult{Available: false, Reason: "mount namespace preflight requires root"}
	}
	exe, err := os.Executable()
	if err != nil {
		return mountPreflightResult{Available: false, Reason: "resolve selftest executable: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "mount-preflight")
	cmd.Env = append(os.Environ(), mountNamespaceProbeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return mountPreflightResult{Available: false, Reason: "mount namespace probe timed out"}
	}
	if err != nil {
		reason := strings.TrimSpace(string(out))
		if reason == "" {
			reason = err.Error()
		}
		return mountPreflightResult{Available: false, Reason: reason}
	}
	return mountPreflightResult{Available: true}
}

func runMountNamespaceProbe() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("probe requires root")
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("unshare mount namespace: %w", err)
	}
	if err := unix.Mount("", "/", "", uintptr(unix.MS_PRIVATE|unix.MS_REC), ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	base, err := os.MkdirTemp("", "aonohako-mountns-probe-*")
	if err != nil {
		return fmt.Errorf("mktemp mount probe: %w", err)
	}
	defer func() { _ = os.RemoveAll(base) }()

	tmpfsDir := filepath.Join(base, "tmpfs")
	if err := os.Mkdir(tmpfsDir, 0o700); err != nil {
		return fmt.Errorf("mkdir tmpfs probe: %w", err)
	}
	if err := unix.Mount("tmpfs", tmpfsDir, "tmpfs", uintptr(unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC), "size=1048576,mode=0700"); err != nil {
		return fmt.Errorf("mount tmpfs probe: %w", err)
	}
	defer func() { _ = unix.Unmount(tmpfsDir, 0) }()
	if err := os.WriteFile(filepath.Join(tmpfsDir, "probe"), []byte("ok\n"), 0o600); err != nil {
		return fmt.Errorf("write tmpfs probe: %w", err)
	}

	procDir := filepath.Join(base, "proc")
	if err := os.Mkdir(procDir, 0o700); err != nil {
		return fmt.Errorf("mkdir proc probe: %w", err)
	}
	if err := unix.Mount("proc", procDir, "proc", uintptr(unix.MS_NODEV|unix.MS_NOSUID|unix.MS_NOEXEC), "hidepid=2"); err != nil {
		return fmt.Errorf("mount proc probe with hidepid=2: %w", err)
	}
	defer func() { _ = unix.Unmount(procDir, 0) }()
	if _, err := os.ReadFile(filepath.Join(procDir, "self", "status")); err != nil {
		return fmt.Errorf("read proc self status probe: %w", err)
	}

	bindDir := filepath.Join(base, "bind")
	if err := os.Mkdir(bindDir, 0o700); err != nil {
		return fmt.Errorf("mkdir bind probe: %w", err)
	}
	if err := os.WriteFile(filepath.Join(bindDir, "existing"), []byte("ok\n"), 0o600); err != nil {
		return fmt.Errorf("seed bind probe: %w", err)
	}
	if err := unix.Mount(bindDir, bindDir, "", uintptr(unix.MS_BIND), ""); err != nil {
		return fmt.Errorf("bind mount probe: %w", err)
	}
	defer func() { _ = unix.Unmount(bindDir, 0) }()
	if err := unix.Mount("", bindDir, "", uintptr(unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY), ""); err != nil {
		return fmt.Errorf("remount bind probe read-only: %w", err)
	}
	if err := os.WriteFile(filepath.Join(bindDir, "blocked"), []byte("blocked\n"), 0o600); err == nil {
		return fmt.Errorf("read-only bind probe allowed a write")
	} else if !errors.Is(err, unix.EROFS) {
		return fmt.Errorf("read-only bind probe returned %w, want EROFS", err)
	}
	return nil
}

func runImagePermissionsSuite() error {
	if err := runDirectImagePermissionChecks(); err != nil {
		return err
	}
	return nil
}

func runPermissionsSuite() error {
	if err := runDirectImagePermissionChecks(); err != nil {
		return err
	}

	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-selftest-unixgram-%d.sock", time.Now().UnixNano()))
	addr := &net.UnixAddr{Name: socketPath, Net: "unixgram"}
	listener, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return fmt.Errorf("listen unixgram socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o777); err != nil {
		return fmt.Errorf("chmod unixgram socket: %w", err)
	}

	cases := []suiteCase{
		{
			name: "unix-datagram-send-is-blocked",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "main.py",
					DataB64: encodeScript(fmt.Sprintf(
						"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)\n    s.sendto(b'escape', %q)\n    print('sent')\nexcept OSError:\n    print('blocked')\n",
						socketPath,
					)),
				}},
				ExpectedStdout: "blocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
			check: func(model.RunResponse) error {
				_ = listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				buf := make([]byte, 64)
				if n, _, err := listener.ReadFromUnix(buf); err == nil {
					return fmt.Errorf("unix-datagram-send-is-blocked: unexpected datagram %q", string(buf[:n]))
				}
				return nil
			},
		},
		{
			name: "socketpair-creation-is-blocked",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "main.py",
					DataB64: encodeScript(
						"import socket\ntry:\n    socket.socketpair()\n    print('created')\nexcept OSError:\n    print('blocked')\n",
					),
				}},
				ExpectedStdout: "blocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		{
			name: "namespace-unshare-is-blocked",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "main.py",
					DataB64: encodeScript(
						"import ctypes\nlibc = ctypes.CDLL(None, use_errno=True)\ntry:\n    rc = libc.unshare(0x00020000)\n    if rc == 0:\n        print('escaped')\n    else:\n        print('blocked')\nexcept Exception:\n    print('blocked')\n",
					),
				}},
				ExpectedStdout: "blocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		{
			name: "submitted-files-cannot-be-replaced",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{
					{
						Name: "main.py",
						DataB64: encodeScript(
							"from pathlib import Path\nimport os\ntry:\n    os.unlink('data.txt')\n    print('unlinked')\nexcept OSError:\n    print('blocked-unlink')\nPath('swap.txt').write_text('mutated\\n')\ntry:\n    os.replace('swap.txt', 'data.txt')\n    print('replaced')\nexcept OSError:\n    print('blocked-replace')\nprint(Path('data.txt').read_text(), end='')\n",
						),
					},
					{
						Name:    "data.txt",
						DataB64: encodeScript("original\n"),
					},
				},
				ExpectedStdout: "blocked-unlink\nblocked-replace\noriginal\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		{
			name: "outside-temp-dirs-are-not-writable",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "main.py",
					DataB64: encodeScript(
						"from pathlib import Path\nfor target in ['/tmp/aonohako-outside.txt', '/var/tmp/aonohako-outside.txt']:\n    try:\n        Path(target).write_text('escape')\n        print('wrote')\n    except OSError:\n        print('blocked')\n",
					),
				}},
				ExpectedStdout: "blocked\nblocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128, WorkspaceBytes: 8 << 10},
			},
		},
	}

	if err := runSuiteCases(cases); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(os.Stdout, "sandbox permissions ok")
	return nil
}

func runCompileSecuritySuite() error {
	python, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("python3 not available: %w", err)
	}

	compileResp := compile.New().Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: encodeScript("print('ok')\n"),
		}},
	})
	if compileResp.Status != model.CompileStatusOK {
		return fmt.Errorf("python compile failed: status=%s reason=%s stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
	}
	if len(compileResp.Artifacts) == 0 {
		return fmt.Errorf("python compile produced no artifacts")
	}
	mutationResp := compile.New().Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{
			{
				Name:    "Main.py",
				DataB64: encodeScript("print('ok')\n"),
			},
			{
				Name:    "sitecustomize.py",
				DataB64: encodeScript("from pathlib import Path\nPath('Main.py').write_text(\"print(\\\"pwned\\\")\\n\")\n"),
			},
		},
	})
	if mutationResp.Status != model.CompileStatusOK {
		return fmt.Errorf("python sitecustomize compile failed: status=%s reason=%s stdout=%q stderr=%q", mutationResp.Status, mutationResp.Reason, mutationResp.Stdout, mutationResp.Stderr)
	}
	mainArtifact := ""
	for _, artifact := range mutationResp.Artifacts {
		if artifact.Name != "Main.py" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(artifact.DataB64)
		if err != nil {
			return fmt.Errorf("decode mutated Main.py artifact: %w", err)
		}
		mainArtifact = string(raw)
		break
	}
	if mainArtifact != "print('ok')\n" {
		return fmt.Errorf("python compile executed sitecustomize and changed Main.py to %q", mainArtifact)
	}

	workDir, err := os.MkdirTemp("", "aonohako-selftest-compile-*")
	if err != nil {
		return fmt.Errorf("mkdtemp compile selftest: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	stdout, stderr, status, reason := compile.RunSandboxedCommand(
		context.Background(),
		workDir,
		"/bin/sh",
		[]string{"-c", "sleep 30 & echo $! > bg.pid"},
		nil,
	)
	if status != model.CompileStatusOK {
		return fmt.Errorf("background-child probe failed: status=%s reason=%s stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
	rawPID, err := os.ReadFile(filepath.Join(workDir, "bg.pid"))
	if err != nil {
		return fmt.Errorf("read bg.pid: %w", err)
	}
	pidText := strings.TrimSpace(string(rawPID))
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return fmt.Errorf("parse bg.pid %q: %w", pidText, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			break
		}
		if err != nil {
			return fmt.Errorf("kill(%d, 0): %w", pid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("background child %d is still alive", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}

	streamDir, err := os.MkdirTemp("", "aonohako-selftest-compile-stream-*")
	if err != nil {
		return fmt.Errorf("mkdtemp stream probe: %w", err)
	}
	defer func() { _ = os.RemoveAll(streamDir) }()
	if err := os.Chmod(streamDir, 0o777); err != nil {
		return fmt.Errorf("chmod stream probe dir: %w", err)
	}
	streamPath := filepath.Join(streamDir, "control.sock")
	streamListener, err := net.Listen("unix", streamPath)
	if err != nil {
		return fmt.Errorf("listen unix stream probe: %w", err)
	}
	defer streamListener.Close()
	if err := os.Chmod(streamPath, 0o777); err != nil {
		return fmt.Errorf("chmod unix stream probe socket: %w", err)
	}

	dgramPath := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-selftest-compile-dgram-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(dgramPath)
	dgramListener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: dgramPath, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("listen unix datagram probe: %w", err)
	}
	defer func() {
		_ = dgramListener.Close()
		_ = os.Remove(dgramPath)
	}()
	if err := os.Chmod(dgramPath, 0o777); err != nil {
		return fmt.Errorf("chmod unix datagram probe socket: %w", err)
	}

	probes := []struct {
		name string
		args []string
	}{
		{
			name: "network-socket",
			args: []string{"-c", "import errno, socket, sys\ntry:\n    socket.socket()\nexcept OSError as exc:\n    sys.exit(0 if exc.errno in (errno.EPERM, errno.EACCES) else 1)\nsys.exit(1)\n"},
		},
		{
			name: "local-unix-socketpair",
			args: []string{"-c", "import socket, sys\na, b = socket.socketpair()\na.sendall(b'ok')\nsys.exit(0 if b.recv(2) == b'ok' else 1)\n"},
		},
		{
			name: "unix-stream-connect",
			args: []string{"-c", fmt.Sprintf("import socket, sys\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\n    s.settimeout(0.5)\n    s.connect(%q)\nexcept OSError:\n    sys.exit(0)\nsys.exit(1)\n", streamPath)},
		},
		{
			name: "unix-datagram-sendmsg",
			args: []string{"-c", fmt.Sprintf("import socket, sys\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)\n    s.sendmsg([b'escape'], [], 0, %q)\nexcept OSError:\n    sys.exit(0)\nsys.exit(1)\n", dgramPath)},
		},
		{
			name: "namespace-unshare",
			args: []string{"-c", "import ctypes, errno, sys\nlibc = ctypes.CDLL(None, use_errno=True)\nif libc.unshare(0x20000) == 0:\n    sys.exit(1)\nsys.exit(0 if ctypes.get_errno() in (errno.EPERM, errno.ENOSYS) else 1)\n"},
		},
		{
			name: "process-group-escape",
			args: []string{"-c", "import errno, os, sys\ntry:\n    os.setpgid(0, 0)\nexcept OSError as exc:\n    sys.exit(0 if exc.errno in (errno.EPERM, errno.EACCES) else 1)\nsys.exit(1)\n"},
		},
		{
			name: "filesystem-privilege-syscalls",
			args: []string{"-c", "import errno, os, sys\nopen('owned.txt', 'w').close()\nchecks = [\n    lambda: os.chmod('owned.txt', 0o777),\n    lambda: os.chown('owned.txt', os.getuid(), os.getgid()),\n    lambda: os.mknod('node'),\n]\nfor action in checks:\n    try:\n        action()\n        sys.exit(1)\n    except OSError as exc:\n        if exc.errno not in (errno.EPERM, errno.EACCES, errno.ENOSYS):\n            sys.exit(1)\nsys.exit(0)\n"},
		},
	}
	for _, probe := range probes {
		probeDir, err := os.MkdirTemp("", "aonohako-selftest-compile-probe-*")
		if err != nil {
			return fmt.Errorf("mkdtemp %s: %w", probe.name, err)
		}
		stdout, stderr, status, reason := compile.RunSandboxedCommand(context.Background(), probeDir, python, probe.args, nil)
		_ = os.RemoveAll(probeDir)
		if status != model.CompileStatusOK {
			return fmt.Errorf("%s probe failed: status=%s reason=%s stdout=%q stderr=%q", probe.name, status, reason, stdout, stderr)
		}
	}
	_ = dgramListener.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	dgramBuf := make([]byte, 64)
	if n, _, err := dgramListener.ReadFromUnix(dgramBuf); err == nil {
		return fmt.Errorf("unix-datagram-sendmsg probe delivered %q", string(dgramBuf[:n]))
	}

	if os.Geteuid() == 0 {
		secretDir, err := os.MkdirTemp("", "aonohako-selftest-compile-secret-*")
		if err != nil {
			return fmt.Errorf("mkdtemp secret probe: %w", err)
		}
		defer func() { _ = os.RemoveAll(secretDir) }()
		if err := os.Chmod(secretDir, 0o700); err != nil {
			return fmt.Errorf("chmod secret probe dir: %w", err)
		}
		secretPath := filepath.Join(secretDir, "secret.txt")
		if err := os.WriteFile(secretPath, []byte("top-secret"), 0o600); err != nil {
			return fmt.Errorf("write secret probe file: %w", err)
		}
		secretScript := fmt.Sprintf("from pathlib import Path\nfor action in [lambda: Path(%q).read_text(), lambda: Path(%q).write_text('escape')]:\n    try:\n        action()\n        raise SystemExit(1)\n    except Exception:\n        pass\n", secretPath, filepath.Join(secretDir, "created.txt"))
		secretDirWork, err := os.MkdirTemp("", "aonohako-selftest-compile-secret-work-*")
		if err != nil {
			return fmt.Errorf("mkdtemp secret work probe: %w", err)
		}
		stdout, stderr, status, reason := compile.RunSandboxedCommand(context.Background(), secretDirWork, python, []string{"-c", secretScript}, nil)
		_ = os.RemoveAll(secretDirWork)
		if status != model.CompileStatusOK {
			return fmt.Errorf("host-path probe failed: status=%s reason=%s stdout=%q stderr=%q", status, reason, stdout, stderr)
		}
	}

	fdDir, err := os.MkdirTemp("", "aonohako-selftest-compile-fd-*")
	if err != nil {
		return fmt.Errorf("mkdtemp fd probe: %w", err)
	}
	defer func() { _ = os.RemoveAll(fdDir) }()
	fdFile, err := os.CreateTemp(fdDir, "inherited-fd-*")
	if err != nil {
		return fmt.Errorf("CreateTemp inherited fd: %w", err)
	}
	defer fdFile.Close()
	if _, err := fdFile.WriteString("secret"); err != nil {
		return fmt.Errorf("write inherited fd probe: %w", err)
	}
	if _, err := fdFile.Seek(0, 0); err != nil {
		return fmt.Errorf("seek inherited fd probe: %w", err)
	}
	fd := int(fdFile.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return fmt.Errorf("fcntl F_GETFD: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
		return fmt.Errorf("fcntl F_SETFD: %w", err)
	}
	stdout, stderr, status, reason = compile.RunSandboxedCommand(
		context.Background(),
		fdDir,
		python,
		[]string{"-c", "import errno, os, sys\nfd = int(sys.argv[1])\ntry:\n    os.read(fd, 1)\nexcept OSError as exc:\n    sys.exit(0 if exc.errno == errno.EBADF else 1)\nsys.exit(1)\n", fmt.Sprintf("%d", fd)},
		nil,
	)
	if status != model.CompileStatusOK {
		return fmt.Errorf("fd leak probe failed: status=%s reason=%s stdout=%q stderr=%q", status, reason, stdout, stderr)
	}

	_, _ = fmt.Fprintln(os.Stdout, "compile security ok")
	return nil
}

func runCompileExecuteSuite() error {
	rawLanguages := strings.TrimSpace(os.Getenv("AONOHAKO_LANGUAGES"))
	if rawLanguages == "" {
		return fmt.Errorf("AONOHAKO_LANGUAGES is empty")
	}

	server := api.NewWithServices(
		config.Config{
			MaxActiveRuns:                        1,
			MaxPendingQueue:                      1,
			HeartbeatInterval:                    time.Second,
			AllowRequestPythonInstalledLibraries: true,
		},
		compile.New(),
		execute.New(),
	)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	cases := compileExecuteCases()
	startupMemory := runtimeStartupMemoryMB()
	seen := map[string]struct{}{}
	for _, rawLanguage := range strings.Split(rawLanguages, ",") {
		language := strings.TrimSpace(rawLanguage)
		if language == "" {
			continue
		}
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}

		tc, ok := cases[language]
		if !ok {
			return fmt.Errorf("compile-execute selftest has no case for language %q", language)
		}

		compileLanguages := append([]string{tc.compileLang}, tc.compileVariants...)
		for variantIndex, compileLanguage := range compileLanguages {
			profile, ok := profiles.Resolve(compileLanguage)
			if !ok {
				return fmt.Errorf("compile-execute selftest could not resolve compile profile %q", compileLanguage)
			}

			compileAttempts := max(tc.compileAttempts, 1)
			var compileResp model.CompileResponse
			for attempt := 1; attempt <= compileAttempts; attempt++ {
				var err error
				compileResp, err = postCompileRequest(httpServer.URL, model.CompileRequest{
					Lang:       compileLanguage,
					Sources:    tc.sources,
					EntryPoint: tc.entryPoint,
				})
				if err != nil {
					return fmt.Errorf("%s/%s compile request %d/%d failed: %w", language, compileLanguage, attempt, compileAttempts, err)
				}
				if compileResp.Status != model.CompileStatusOK {
					return fmt.Errorf("%s/%s compile %d/%d failed: status=%s reason=%s stdout=%q stderr=%q", language, compileLanguage, attempt, compileAttempts, compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
				}
			}
			if language == "fsharp" && variantIndex == 0 {
				regressionSource := model.Source{Name: "Main.fs", DataB64: encodeScript(`let rec fac n =
    if n <= 1I then
        1I
    else
        n * fac(n - 1I)

let rec getans num cnt =
    if num % 10I = 0I then
        getans(num / 10I)(cnt + 1I)
    else
        cnt

let a = bigint(System.Console.ReadLine())
printfn"%A"(getans(fac(a))0I)
`)}
				for attempt := 1; attempt <= 20; attempt++ {
					regressionResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
						Lang:    compileLanguage,
						Sources: []model.Source{regressionSource},
					})
					if err != nil {
						return fmt.Errorf("fsharp regression compile request %d/20 failed: %w", attempt, err)
					}
					output := regressionResp.Stdout + regressionResp.Stderr
					if regressionResp.Status != model.CompileStatusCompileError || !strings.Contains(output, "FS0041") {
						return fmt.Errorf("fsharp regression compile %d/20 returned status=%s reason=%s stdout=%q stderr=%q", attempt, regressionResp.Status, regressionResp.Reason, regressionResp.Stdout, regressionResp.Stderr)
					}
				}
			}

			limits := tc.limits
			if limits.TimeMs <= 0 {
				limits.TimeMs = 6000
			}
			if limits.MemoryMB <= 0 {
				limits.MemoryMB = 512
			}

			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{
					Name:    artifact.Name,
					DataB64: artifact.DataB64,
					Mode:    artifact.Mode,
				})
			}

			judgeIO := tc.judgeIO
			if len(judgeIO) == 0 {
				judgeIO = []judgeIOCase{{
					stdin:          tc.stdin,
					expectedStdout: tc.expectedStdout,
				}}
			}
			for index, ioCase := range judgeIO {
				runResp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
					Lang:              profile.RunLang,
					Binaries:          binaries,
					EntryPoint:        tc.entryPoint,
					Stdin:             ioCase.stdin,
					ExpectedStdout:    ioCase.expectedStdout,
					Limits:            limits,
					PythonLibraryMode: tc.pythonLibraryMode,
				})
				if err != nil {
					return fmt.Errorf("%s/%s execute case %d/%d request failed: %w", language, compileLanguage, index+1, len(judgeIO), err)
				}
				if runResp.Status != model.RunStatusAccepted {
					return fmt.Errorf("%s/%s execute case %d/%d failed: status=%s reason=%s stdout=%q stderr=%q", language, compileLanguage, index+1, len(judgeIO), runResp.Status, runResp.Reason, runResp.Stdout, runResp.Stderr)
				}
			}

			if memoryMB, ok := startupMemory[language]; ok {
				startupLimits := limits
				startupLimits.MemoryMB = memoryMB
				attempts := 2
				if language == "java" {
					attempts = 5
				}
				for attempt := 1; attempt <= attempts; attempt++ {
					startupResp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
						Lang:              profile.RunLang,
						Binaries:          binaries,
						EntryPoint:        tc.entryPoint,
						Stdin:             judgeIO[0].stdin,
						ExpectedStdout:    judgeIO[0].expectedStdout,
						Limits:            startupLimits,
						PythonLibraryMode: tc.pythonLibraryMode,
					})
					if err != nil {
						return fmt.Errorf("%s/%s constrained startup attempt %d failed: %w", language, compileLanguage, attempt, err)
					}
					if startupResp.Status != model.RunStatusAccepted {
						return fmt.Errorf("%s/%s constrained startup attempt %d failed: memory_mb=%d status=%s reason=%s stdout=%q stderr=%q", language, compileLanguage, attempt, memoryMB, startupResp.Status, startupResp.Reason, startupResp.Stdout, startupResp.Stderr)
					}
				}
			}
		}
	}

	_, _ = fmt.Fprintln(os.Stdout, "compile execute ok")
	return nil
}

func runTwoStepSuite() error {
	baseURL := ""
	// Runtime-image smoke should exercise the deployed server binary when it
	// is present; the in-process fallback keeps local go test/go run usable.
	if serverPath, err := exec.LookPath("aonohako"); err == nil {
		selfPath, selfErr := os.Executable()
		serverRealPath, _ := filepath.EvalSymlinks(serverPath)
		selfRealPath, _ := filepath.EvalSymlinks(selfPath)
		if selfErr != nil || serverRealPath == "" || selfRealPath == "" || serverRealPath != selfRealPath {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return fmt.Errorf("two-step server port allocation failed: %w", err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			if err := listener.Close(); err != nil {
				return fmt.Errorf("two-step server port release failed: %w", err)
			}
			var serverLog bytes.Buffer
			cmd := exec.Command(serverPath)
			cmd.Env = append(os.Environ(),
				"PORT="+strconv.Itoa(port),
				"AONOHAKO_DEPLOYMENT_TARGET=dev",
				"AONOHAKO_INBOUND_AUTH=none",
				"AONOHAKO_MAX_ACTIVE_RUNS=1",
				"AONOHAKO_MAX_PENDING_QUEUE=1",
			)
			cmd.Stdout = &serverLog
			cmd.Stderr = &serverLog
			if err := cmd.Start(); err != nil {
				return fmt.Errorf("two-step server start failed: %w", err)
			}
			waitCh := make(chan error, 1)
			go func() {
				waitCh <- cmd.Wait()
			}()
			cleanup := func() {
				if cmd.Process != nil {
					_ = cmd.Process.Signal(syscall.SIGTERM)
				}
				select {
				case <-waitCh:
				case <-time.After(5 * time.Second):
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
					<-waitCh
				}
			}
			baseURL = "http://127.0.0.1:" + strconv.Itoa(port)
			client := http.Client{Timeout: 200 * time.Millisecond}
			deadline := time.Now().Add(5 * time.Second)
			for {
				resp, err := client.Get(baseURL + "/healthz")
				if err == nil {
					_ = resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						break
					}
				}
				select {
				case err := <-waitCh:
					return fmt.Errorf("two-step server exited before healthz: %v: %s", err, strings.TrimSpace(serverLog.String()))
				default:
				}
				if time.Now().After(deadline) {
					cleanup()
					return fmt.Errorf("two-step server healthz timed out: %s", strings.TrimSpace(serverLog.String()))
				}
				time.Sleep(100 * time.Millisecond)
			}
			defer cleanup()
		}
	}
	if baseURL == "" {
		server := api.NewWithServices(
			config.Config{
				MaxActiveRuns:     1,
				MaxPendingQueue:   1,
				HeartbeatInterval: time.Second,
			},
			compile.New(),
			execute.New(),
		)
		httpServer := httptest.NewServer(server.Handler())
		defer httpServer.Close()
		baseURL = httpServer.URL
	}

	request := model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name: "encode.sh",
					DataB64: encodeScript(`#!/bin/sh
while IFS= read -r line; do
  printf 'encoded:%s\n' "$line"
done
`),
					Mode: "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name: "decode.sh",
					DataB64: encodeScript(`#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "${line#encoded:}"
done
`),
					Mode: "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				Stdin:     "two-step-ok\n",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
				Handoff:   &model.StepHandoff{ID: "encoded", From: "stdout", MaxBytes: 4096},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinFrom: "encoded",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		ExpectedStdout: "two-step-ok\n",
	}
	for attempt := 1; attempt <= twoStepStabilityRuns; attempt++ {
		resp, err := postExecuteRequest(baseURL, request)
		if err != nil {
			return fmt.Errorf("two-step execute attempt %d failed: %w", attempt, err)
		}
		if resp.Status != model.RunStatusAccepted {
			stepsJSON, _ := json.Marshal(resp.Steps)
			return fmt.Errorf("two-step execute attempt %d failed: status=%s reason=%s stdout=%q stderr=%q steps=%s", attempt, resp.Status, resp.Reason, resp.Stdout, resp.Stderr, stepsJSON)
		}
		if len(resp.Steps) != 2 || resp.Steps[0].Status != model.RunStatusAccepted || resp.Steps[1].Status != model.RunStatusAccepted {
			return fmt.Errorf("two-step execute attempt %d returned unexpected step results: %+v", attempt, resp.Steps)
		}
	}

	_, _ = fmt.Fprintln(os.Stdout, "two step ok")
	return nil
}

func runLanguageSecuritySuite() error {
	rawLanguages := strings.TrimSpace(os.Getenv("AONOHAKO_LANGUAGES"))
	if rawLanguages == "" {
		return fmt.Errorf("AONOHAKO_LANGUAGES is empty")
	}

	networkListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen network escape probe: %w", err)
	}
	defer networkListener.Close()
	go func() {
		for {
			conn, err := networkListener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	tcpAddr, ok := networkListener.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("network escape probe did not get a TCP address: %s", networkListener.Addr())
	}

	server := api.NewWithServices(
		config.Config{
			MaxActiveRuns:     1,
			MaxPendingQueue:   1,
			HeartbeatInterval: time.Second,
		},
		compile.New(),
		execute.New(),
	)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	cases := languageSecurityCases(tcpAddr.Port)
	seen := map[string]struct{}{}
	covered := 0
	for _, rawLanguage := range strings.Split(rawLanguages, ",") {
		language := strings.TrimSpace(rawLanguage)
		if language == "" {
			continue
		}
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}

		languageCases := cases[language]
		for _, tc := range languageCases {
			profile, ok := profiles.Resolve(tc.compileLang)
			if !ok {
				return fmt.Errorf("%s %s security selftest could not resolve compile profile %q", language, tc.name, tc.compileLang)
			}

			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang:       tc.compileLang,
				Sources:    tc.sources,
				EntryPoint: tc.entryPoint,
			})
			if err != nil {
				return fmt.Errorf("%s %s security compile request failed: %w", language, tc.name, err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("%s %s security compile failed: status=%s reason=%s stdout=%q stderr=%q", language, tc.name, compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}

			limits := tc.limits
			if limits.TimeMs <= 0 {
				limits.TimeMs = 6000
			}
			if limits.MemoryMB <= 0 {
				limits.MemoryMB = 512
			}
			if limits.OutputBytes <= 0 {
				limits.OutputBytes = 4096
			}

			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{
					Name:    artifact.Name,
					DataB64: artifact.DataB64,
					Mode:    artifact.Mode,
				})
			}

			runResp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:           profile.RunLang,
				Binaries:       binaries,
				EntryPoint:     tc.entryPoint,
				ExpectedStdout: tc.expectedStdout,
				Limits:         limits,
			})
			if err != nil {
				return fmt.Errorf("%s %s security execute request failed: %w", language, tc.name, err)
			}
			if runResp.Status != model.RunStatusAccepted {
				return fmt.Errorf("%s %s security execute failed: status=%s reason=%s stdout=%q stderr=%q", language, tc.name, runResp.Status, runResp.Reason, runResp.Stdout, runResp.Stderr)
			}
			covered++
		}
	}

	if covered == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "language security ok (no covered languages)")
		return nil
	}
	_, _ = fmt.Fprintf(os.Stdout, "language security ok (%d cases)\n", covered)
	return nil
}

func runRuntimeMemorySuite() error {
	rawLanguages := strings.TrimSpace(os.Getenv("AONOHAKO_LANGUAGES"))
	if rawLanguages == "" {
		return fmt.Errorf("AONOHAKO_LANGUAGES is empty")
	}

	server := api.NewWithServices(
		config.Config{
			MaxActiveRuns:     1,
			MaxPendingQueue:   1,
			HeartbeatInterval: time.Second,
		},
		compile.New(),
		execute.New(),
	)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	seen := map[string]struct{}{}
	strictCases := strictRuntimeMemoryCases()
	covered := 0
	for _, rawLanguage := range strings.Split(rawLanguages, ",") {
		language := strings.TrimSpace(rawLanguage)
		if language == "" {
			continue
		}
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}

		if tc, ok := strictCases[language]; ok {
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang:    tc.compileLang,
				Sources: tc.sources,
			})
			if err != nil {
				return fmt.Errorf("%s memory compile request failed: %w", language, err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("%s memory compile failed: status=%s reason=%q stdout=%q stderr=%q", language, compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			profile, ok := profiles.Resolve(tc.compileLang)
			if !ok {
				return fmt.Errorf("%s memory selftest could not resolve %s profile", language, tc.compileLang)
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     profile.RunLang,
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 6000, MemoryMB: tc.memoryMB, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("%s memory execute request failed: %w", language, err)
			}
			if resp.Status != model.RunStatusMLE ||
				(!strings.HasPrefix(resp.VerdictSource, "memory") && resp.VerdictSource != "address_space") {
				return fmt.Errorf("%s memory stress status=%s source=%q reason=%q stdout=%q stderr=%q", language, resp.Status, resp.VerdictSource, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
			continue
		}

		switch language {
		case "plain":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "C11",
				Sources: []model.Source{{
					Name: "Main.c",
					DataB64: encodeScript(`#include <stdint.h>

int main(void) {
    volatile uint64_t x = 0;
    for (;;) {
        x++;
    }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("plain cpu compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("plain cpu compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "binary",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 100, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("plain cpu execute request failed: %w", err)
			}
			if resp.Status != model.RunStatusTLE {
				return fmt.Errorf("plain cpu stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}

			compileResp, err = postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "C11",
				Sources: []model.Source{{
					Name: "Main.c",
					DataB64: encodeScript(`#include <stdint.h>
#include <stdlib.h>
#include <string.h>

int main(void) {
    const size_t step = 8 * 1024 * 1024;
    volatile uint64_t touched = 0;
    for (;;) {
        char *chunk = malloc(step);
        if (chunk == NULL) {
            return 75;
        }
        memset(chunk, 1, step);
        touched += (uint8_t)chunk[0];
    }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("plain memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("plain memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries = binaries[:0]
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err = postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "binary",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 4000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("plain memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("plain memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "javascript":
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang: "javascript",
				Binaries: []model.Binary{{
					Name: "Main.js",
					DataB64: encodeScript(`const chunks = [];
while (true) {
  chunks.push(Buffer.alloc(8 * 1024 * 1024, 1));
}
`),
				}},
				Limits: model.Limits{TimeMs: 4000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("javascript memory request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("javascript memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "typescript":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "TYPESCRIPT",
				Sources: []model.Source{{
					Name: "Main.ts",
					DataB64: encodeScript(`declare const Buffer: any;
const chunks: any[] = [];
while (true) {
  chunks.push(Buffer.alloc(8 * 1024 * 1024, 1));
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("typescript memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("typescript memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "javascript",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 4000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("typescript memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("typescript memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "deno":
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang: "deno",
				Binaries: []model.Binary{{
					Name: "Main.ts",
					DataB64: encodeScript(`const chunks: Uint8Array[] = [];
while (true) {
  chunks.push(new Uint8Array(8 * 1024 * 1024));
}
`),
				}},
				Limits: model.Limits{TimeMs: 4000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("deno memory request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("deno memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "python":
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "Main.py",
					DataB64: encodeScript(`chunks = []
while True:
    chunks.append(bytearray(8 * 1024 * 1024))
`),
				}},
				Limits: model.Limits{TimeMs: 4000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("python memory request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("python memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "pypy":
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang: "pypy",
				Binaries: []model.Binary{{
					Name: "Main.py",
					DataB64: encodeScript(`chunks = []
while True:
    chunks.append(bytearray(8 * 1024 * 1024))
`),
				}},
				Limits: model.Limits{TimeMs: 4000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("pypy memory request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("pypy memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "java":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "JAVA",
				Sources: []model.Source{{
					Name: "Main.java",
					DataB64: encodeScript(`import java.nio.ByteBuffer;
import java.util.ArrayList;
import java.util.List;

public class Main {
  public static void main(String[] args) {
    List<ByteBuffer> chunks = new ArrayList<>();
    while (true) {
      chunks.add(ByteBuffer.allocateDirect(8 * 1024 * 1024));
    }
  }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("java memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("java memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "java",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 6000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("java memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("java memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}

			compileResp, err = postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "JAVA",
				Sources: []model.Source{{
					Name: "Main.java",
					DataB64: encodeScript(`import java.lang.reflect.Proxy;
import java.net.URL;
import java.net.URLClassLoader;
import java.util.ArrayList;
import java.util.List;

public class Main {
  interface Marker {}

  public static void main(String[] args) {
    List<Class<?>> generated = new ArrayList<>();
    while (true) {
      ClassLoader loader = new URLClassLoader(new URL[0], Main.class.getClassLoader());
      Object proxy = Proxy.newProxyInstance(loader, new Class<?>[]{Marker.class}, (p, m, a) -> null);
      generated.add(proxy.getClass());
    }
  }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("java metaspace compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("java metaspace compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries = binaries[:0]
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err = postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "java",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 6000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("java metaspace execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("java metaspace stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}

			compileResp, err = postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "JAVA",
				Sources: []model.Source{{
					Name: "Main.java",
					DataB64: encodeScript(`import java.util.ArrayList;
import java.util.List;

public class Main {
  public static void main(String[] args) {
    List<Thread> threads = new ArrayList<>();
    try {
      while (true) {
        Thread t = new Thread(() -> {
          try {
            Thread.sleep(60000);
          } catch (InterruptedException ignored) {
          }
        });
        t.setDaemon(true);
        t.start();
        threads.add(t);
      }
    } catch (Throwable ignored) {
      System.exit(75);
    }
  }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("java thread compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("java thread compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries = binaries[:0]
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err = postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "java",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 6000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("java thread execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("java thread stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "groovy":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "GROOVY",
				Sources: []model.Source{{
					Name: "Main.groovy",
					DataB64: encodeScript(`class Main {
  static void main(String[] args) {
    def chunks = []
    while (true) {
      chunks.add(new byte[32 * 1024 * 1024])
    }
  }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("groovy memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("groovy memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "groovy",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 8000, MemoryMB: 768, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("groovy memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("groovy memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "scala":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "SCALA",
				Sources: []model.Source{{
					Name: "Main.scala",
					DataB64: encodeScript(`object Main {
  def main(args: Array[String]): Unit = {
    val chunks = new java.util.ArrayList[Array[Byte]]()
    while (true) {
      chunks.add(new Array[Byte](32 * 1024 * 1024))
    }
  }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("scala memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("scala memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "scala",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 8000, MemoryMB: 768, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("scala memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("scala memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "clojure":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "CLOJURE",
				Sources: []model.Source{{
					Name: "Main.clj",
					DataB64: encodeScript(`(def chunks (java.util.ArrayList.))
(while true
  (.add chunks (byte-array (* 32 1024 1024))))
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("clojure memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("clojure memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "clojure",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 8000, MemoryMB: 768, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("clojure memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("clojure memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "wasm":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "WASM",
				Sources: []model.Source{{
					Name: "Main.wat",
					DataB64: encodeScript(`(module
  (memory 1 65536)
  (export "memory" (memory 0))
  (func (export "_start")
    (loop $again
      i32.const 1
      memory.grow
      drop
      br $again)))
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("wasm memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("wasm memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "wasm",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 4000, MemoryMB: 64, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("wasm memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("wasm memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "csharp":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "CSHARP",
				Sources: []model.Source{{
					Name: "Program.cs",
					DataB64: encodeScript(`using System;
using System.Collections.Generic;

public static class Program {
  public static void Main() {
    var chunks = new List<byte[]>();
    while (true) {
      chunks.Add(new byte[8 * 1024 * 1024]);
    }
  }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("csharp memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("csharp memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "csharp",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 8000, MemoryMB: 128, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("csharp memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("csharp memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}

			compileResp, err = postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "CSHARP",
				Sources: []model.Source{{
					Name: "Program.cs",
					DataB64: encodeScript(`using System.IO;

public static class Program {
  public static void Main() {
    var data = new byte[65536];
    using var stream = File.OpenWrite("burst.bin");
    while (true) {
      stream.Write(data, 0, data.Length);
      stream.Flush();
    }
  }
}
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("csharp write-burst compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("csharp write-burst compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries = binaries[:0]
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err = postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "csharp",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 8000, MemoryMB: 128, OutputBytes: 1024, WorkspaceBytes: 128 << 10},
			})
			if err != nil {
				return fmt.Errorf("csharp write-burst execute request failed: %w", err)
			}
			if resp.Status != model.RunStatusWLE {
				return fmt.Errorf("csharp write-burst status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "fsharp":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "FSHARP",
				Sources: []model.Source{{
					Name: "Program.fs",
					DataB64: encodeScript(`[<EntryPoint>]
let main _ =
    let mutable chunks = Array.empty<byte[]>
    while true do
        chunks <- Array.append chunks [| Array.zeroCreate<byte> 8388608 |]
    0
`),
				}},
			})
			if err != nil {
				return fmt.Errorf("fsharp memory compile request failed: %w", err)
			}
			if compileResp.Status != model.CompileStatusOK {
				return fmt.Errorf("fsharp memory compile failed: status=%s reason=%q stdout=%q stderr=%q", compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
			}
			binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
			for _, artifact := range compileResp.Artifacts {
				binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
			}
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang:     "fsharp",
				Binaries: binaries,
				Limits:   model.Limits{TimeMs: 8000, MemoryMB: 128, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("fsharp memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("fsharp memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		case "uhmlang":
			resp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
				Lang: "uhmlang",
				Binaries: []model.Binary{{
					Name:    "Main.uhm",
					DataB64: encodeScript(uhmlangMemoryStressProgram()),
				}},
				Limits: model.Limits{TimeMs: 6000, MemoryMB: 16, OutputBytes: 1024},
			})
			if err != nil {
				return fmt.Errorf("uhmlang memory execute request failed: %w", err)
			}
			if resp.Status == model.RunStatusAccepted || resp.Status == model.RunStatusTLE {
				return fmt.Errorf("uhmlang memory stress status=%s reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
			}
			covered++
		}
	}

	if covered == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "runtime resource ok (no covered languages)")
		return nil
	}
	_, _ = fmt.Fprintf(os.Stdout, "runtime resource ok (%d cases)\n", covered)
	return nil
}

func postCompileRequest(baseURL string, req model.CompileRequest) (model.CompileResponse, error) {
	return postSSEJSON[model.CompileResponse](baseURL+"/compile", req)
}

func postExecuteRequest(baseURL string, req model.RunRequest) (model.RunResponse, error) {
	return postSSEJSON[model.RunResponse](baseURL+"/execute", req)
}

func postSSEJSON[T any](url string, payload any) (T, error) {
	var zero T

	body, err := json.Marshal(payload)
	if err != nil {
		return zero, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return zero, fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	reader := bufio.NewReader(resp.Body)
	eventName := ""
	dataLines := make([]string, 0, 4)
	lastErr := ""
	dispatch := func() (T, bool, error) {
		var out T
		if eventName == "" {
			dataLines = dataLines[:0]
			return out, false, nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		switch eventName {
		case "error":
			var message struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(payload), &message); err == nil {
				lastErr = strings.TrimSpace(message.Message)
			}
			return out, false, nil
		case "result":
			if err := json.Unmarshal([]byte(payload), &out); err != nil {
				return out, false, err
			}
			return out, true, nil
		default:
			return out, false, nil
		}
	}

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return zero, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			out, done, dispatchErr := dispatch()
			if dispatchErr != nil {
				return zero, dispatchErr
			}
			if done {
				return out, nil
			}
			eventName = ""
		} else if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if err == io.EOF {
			break
		}
	}

	if strings.TrimSpace(lastErr) != "" {
		return zero, fmt.Errorf("stream ended without result: %s", lastErr)
	}
	return zero, fmt.Errorf("stream ended without result")
}

func languageSecurityCases(tcpPort int) map[string][]languageSecurityCase {
	source := func(name, body string) model.Source {
		return model.Source{Name: name, DataB64: encodeScript(body)}
	}
	limits := model.Limits{TimeMs: 6000, MemoryMB: 512, OutputBytes: 4096}
	managedLimits := model.Limits{TimeMs: 10000, MemoryMB: 1024, OutputBytes: 4096}
	expectedProcessNetwork := "process:blocked\nnetwork:blocked\n"
	expectedNative := "network:blocked\nfork:blocked\nunshare:blocked\n"
	expectedCNative := "network:blocked\nfork:blocked\nunshare:blocked\nx32:blocked\n"
	expectedNativeProcess := "network:blocked\nprocess:blocked\nunshare:blocked\n"
	pythonProcessNetwork := fmt.Sprintf(`import socket
import subprocess

try:
    subprocess.run(["/bin/true"], check=True)
    print("process:leaked")
except Exception:
    print("process:blocked")

try:
    s = socket.create_connection(("127.0.0.1", %d), timeout=0.5)
    s.close()
    print("network:leaked")
except OSError:
    print("network:blocked")
`, tcpPort)
	nodeProcessNetwork := fmt.Sprintf(`const cp = require('child_process');
const net = require('net');

const result = cp.spawnSync('/bin/true');
console.log('process:' + (result.error ? 'blocked' : 'leaked'));

let finished = false;
function finish(label) {
  if (finished) {
    return;
  }
  finished = true;
  console.log('network:' + label);
  process.exit(0);
}

const socket = net.connect({ host: '127.0.0.1', port: %d }, () => {
  socket.destroy();
  finish('leaked');
});
	socket.on('error', () => finish('blocked'));
	setTimeout(() => {
	  socket.destroy();
	  finish('blocked');
	}, 500);
	`, tcpPort)
	typedNodeProcessNetwork := "declare const require: any;\n" +
		"declare const process: any;\n" +
		"declare function setTimeout(fn: () => void, ms: number): unknown;\n\n" +
		strings.ReplaceAll(nodeProcessNetwork, "function finish(label) {", "function finish(label: string) {")

	return map[string][]languageSecurityCase{
		"plain": {
			{
				name:           "native-syscall-denies",
				compileLang:    "C11",
				expectedStdout: expectedCNative,
				limits:         limits,
				sources: []model.Source{
					source("Main.c", `#define _GNU_SOURCE
#include <errno.h>
#include <sched.h>
#include <stdio.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>

static void print_check(const char *name, int blocked) {
    printf("%s:%s\n", name, blocked ? "blocked" : "leaked");
}

int main(void) {
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd >= 0) {
        close(fd);
        print_check("network", 0);
    } else {
        print_check("network", 1);
    }

    pid_t pid = fork();
    if (pid == 0) {
        _exit(0);
    }
    if (pid > 0) {
        int status = 0;
        waitpid(pid, &status, 0);
        print_check("fork", 0);
    } else {
        print_check("fork", 1);
    }

    if (unshare(CLONE_NEWNS) == 0) {
        print_check("unshare", 0);
    } else {
        print_check("unshare", 1);
    }

#if defined(__x86_64__) && defined(SYS_socket)
    errno = 0;
    long x32_rc = syscall(0x40000000UL | SYS_socket, AF_INET, SOCK_STREAM, 0);
    print_check("x32", x32_rc == -1 && errno == EPERM);
#else
    print_check("x32", 1);
#endif
    return 0;
}`),
				},
			},
		},
		"go": {
			{
				name:           "native-syscall-denies",
				compileLang:    "GO",
				expectedStdout: expectedNativeProcess,
				limits:         model.Limits{TimeMs: 12000, MemoryMB: 1536, OutputBytes: 4096},
				sources: []model.Source{
					source("Main.go", `package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

func printCheck(name string, blocked bool) {
	state := "leaked"
	if blocked {
		state = "blocked"
	}
	fmt.Printf("%s:%s\n", name, state)
}

func main() {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err == nil {
		_ = syscall.Close(fd)
		printCheck("network", false)
	} else {
		printCheck("network", true)
	}

	if err := exec.Command("/bin/true").Run(); err == nil {
		printCheck("process", false)
	} else {
		printCheck("process", true)
	}

	_, _, errno := syscall.RawSyscall(syscall.SYS_UNSHARE, uintptr(0x00020000), 0, 0)
	printCheck("unshare", errno != 0)
}`),
				},
			},
		},
		"rust": {
			{
				name:           "native-syscall-denies",
				compileLang:    "RUST2021",
				expectedStdout: expectedNative,
				limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024, OutputBytes: 4096},
				sources: []model.Source{
					source("Main.rs", `use std::ffi::c_int;

const AF_INET: c_int = 2;
const SOCK_STREAM: c_int = 1;
const CLONE_NEWNS: c_int = 0x00020000;

extern "C" {
    fn socket(domain: c_int, typ: c_int, protocol: c_int) -> c_int;
    fn close(fd: c_int) -> c_int;
    fn fork() -> c_int;
    fn waitpid(pid: c_int, status: *mut c_int, options: c_int) -> c_int;
    fn unshare(flags: c_int) -> c_int;
    fn _exit(status: c_int) -> !;
}

fn print_check(name: &str, blocked: bool) {
    println!("{}:{}", name, if blocked { "blocked" } else { "leaked" });
}

fn main() {
    unsafe {
        let fd = socket(AF_INET, SOCK_STREAM, 0);
        if fd >= 0 {
            close(fd);
            print_check("network", false);
        } else {
            print_check("network", true);
        }

        let pid = fork();
        if pid == 0 {
            _exit(0);
        }
        if pid > 0 {
            let mut status = 0;
            waitpid(pid, &mut status, 0);
            print_check("fork", false);
        } else {
            print_check("fork", true);
        }

        print_check("unshare", unshare(CLONE_NEWNS) != 0);
    }
}`),
				},
			},
		},
		"python": {
			{
				name:           "process-and-network-denies",
				compileLang:    "PYTHON3",
				expectedStdout: expectedProcessNetwork,
				limits:         limits,
				sources:        []model.Source{source("Main.py", pythonProcessNetwork)},
			},
		},
		"fennel": {
			{
				name:           "aot-artifact-removes-process-spawn-and-debug",
				compileLang:    "FENNEL",
				expectedStdout: "process:blocked\ndebug:blocked\n",
				limits:         limits,
				sources: []model.Source{
					source("Main.fnl", `(print (if os.execute "process:leaked" "process:blocked"))
(local (debug-ok _) (pcall require :debug))
(print (if debug-ok "debug:leaked" "debug:blocked"))`),
				},
			},
		},
		"pypy": {
			{
				name:           "process-and-network-denies",
				compileLang:    "PYPY3",
				expectedStdout: expectedProcessNetwork,
				limits:         managedLimits,
				sources:        []model.Source{source("Main.py", pythonProcessNetwork)},
			},
		},
		"javascript": {
			{
				name:           "process-and-network-denies",
				compileLang:    "JAVASCRIPT",
				expectedStdout: expectedProcessNetwork,
				limits:         managedLimits,
				sources:        []model.Source{source("Main.js", nodeProcessNetwork)},
			},
		},
		"typescript": {
			{
				name:           "process-and-network-denies",
				compileLang:    "TYPESCRIPT",
				expectedStdout: expectedProcessNetwork,
				limits:         managedLimits,
				sources: []model.Source{
					source("Main.ts", typedNodeProcessNetwork),
				},
			},
		},
		"coffeescript": {
			{
				name:           "process-and-network-denies",
				compileLang:    "COFFEESCRIPT",
				expectedStdout: expectedProcessNetwork,
				limits:         managedLimits,
				sources: []model.Source{
					source("Main.coffee", fmt.Sprintf(`cp = require 'child_process'
net = require 'net'

result = cp.spawnSync '/bin/true'
console.log 'process:' + (if result.error then 'blocked' else 'leaked')

finished = false
finish = (label) ->
  return if finished
  finished = true
  console.log 'network:' + label
  process.exit 0

socket = net.connect {host: '127.0.0.1', port: %d}, ->
  socket.destroy()
  finish 'leaked'
socket.on 'error', ->
  finish 'blocked'
setTimeout (->
  socket.destroy()
  finish 'blocked'
), 500
`, tcpPort)),
				},
			},
		},
		"deno": {
			{
				name:           "process-and-network-denies",
				compileLang:    "DENO",
				expectedStdout: expectedProcessNetwork,
				limits:         managedLimits,
				sources: []model.Source{
					source("Main.ts", fmt.Sprintf(`let processBlocked = false;
try {
  new Deno.Command("/bin/true").outputSync();
} catch (_) {
  processBlocked = true;
}
console.log(`+"`process:${processBlocked ? \"blocked\" : \"leaked\"}`"+`);

let networkBlocked = false;
try {
  const conn = await Deno.connect({ hostname: "127.0.0.1", port: %d });
  conn.close();
} catch (_) {
  networkBlocked = true;
}
console.log(`+"`network:${networkBlocked ? \"blocked\" : \"leaked\"}`"+`);
`, tcpPort)),
				},
			},
		},
		"java": {
			{
				name:           "process-and-network-denies",
				compileLang:    "JAVA11",
				expectedStdout: expectedProcessNetwork,
				limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024, OutputBytes: 4096},
				sources: []model.Source{
					source("Main.java", fmt.Sprintf(`import java.net.InetSocketAddress;
import java.net.Socket;

public class Main {
  public static void main(String[] args) {
    try {
      new ProcessBuilder("/bin/true").start().waitFor();
      System.out.println("process:leaked");
    } catch (Throwable t) {
      System.out.println("process:blocked");
    }

    try {
      Socket s = new Socket();
      s.connect(new InetSocketAddress("127.0.0.1", %d), 500);
      s.close();
      System.out.println("network:leaked");
    } catch (Throwable t) {
      System.out.println("network:blocked");
    }
  }
}
`, tcpPort)),
				},
			},
		},
		"ruby": {
			{
				name:           "process-and-network-denies",
				compileLang:    "RUBY",
				expectedStdout: expectedProcessNetwork,
				limits:         limits,
				sources: []model.Source{
					source("Main.rb", fmt.Sprintf(`require 'socket'

begin
  ok = system('/bin/true')
  puts "process:#{ok ? 'leaked' : 'blocked'}"
rescue
  puts 'process:blocked'
end

begin
  s = TCPSocket.new('127.0.0.1', %d)
  s.close
  puts 'network:leaked'
rescue
  puts 'network:blocked'
end
`, tcpPort)),
				},
			},
		},
		"perl": {
			{
				name:           "process-and-network-denies",
				compileLang:    "PERL",
				expectedStdout: expectedProcessNetwork,
				limits:         limits,
				sources: []model.Source{
					source("Main.pl", fmt.Sprintf(`use IO::Socket::INET;

my $rc = system('/bin/true');
print 'process:' . ($rc == 0 ? 'leaked' : 'blocked') . "\n";

my $s = IO::Socket::INET->new(PeerAddr => '127.0.0.1', PeerPort => %d, Proto => 'tcp', Timeout => 1);
print 'network:' . ($s ? 'leaked' : 'blocked') . "\n";
`, tcpPort)),
				},
			},
		},
		"php": {
			{
				name:           "process-and-network-denies",
				compileLang:    "PHP",
				expectedStdout: expectedProcessNetwork,
				limits:         limits,
				sources: []model.Source{
					source("Main.php", fmt.Sprintf(`<?php
$out = [];
$rc = 0;
@exec('/bin/true', $out, $rc);
echo 'process:' . ($rc === 0 ? 'leaked' : 'blocked') . "\n";

$errno = 0;
$errstr = '';
$s = @fsockopen('127.0.0.1', %d, $errno, $errstr, 1.0);
if ($s) {
    fclose($s);
    echo "network:leaked\n";
} else {
    echo "network:blocked\n";
}
`, tcpPort)),
				},
			},
		},
		"tcl": {
			{
				name:           "process-and-network-denies",
				compileLang:    "TCL",
				expectedStdout: expectedProcessNetwork,
				limits:         limits,
				sources: []model.Source{
					source("Main.tcl", fmt.Sprintf(`if {[catch {exec /bin/true}]} {
  puts "process:blocked"
} else {
  puts "process:leaked"
}

if {[catch {socket 127.0.0.1 %d} s]} {
  puts "network:blocked"
} else {
  close $s
  puts "network:leaked"
}
`, tcpPort)),
				},
			},
		},
	}
}

func compileExecuteCases() map[string]compileExecuteCase {
	source := func(name, body string) model.Source {
		return model.Source{Name: name, DataB64: encodeScript(body)}
	}
	whitespaceABProgram := func() string {
		space := " "
		tab := "\t"
		lf := "\n"
		number := func(value int) string {
			sign := space
			if value < 0 {
				sign = tab
				value = -value
			}
			bits := fmt.Sprintf("%b", value)
			var payload strings.Builder
			for _, ch := range bits {
				if ch == '0' {
					payload.WriteString(space)
				} else {
					payload.WriteString(tab)
				}
			}
			return sign + payload.String() + lf
		}
		push := func(value int) string {
			return space + space + number(value)
		}
		var program strings.Builder
		inputNumber := tab + lf + tab + tab
		retrieve := tab + tab + tab
		add := tab + space + space + space
		outputNumber := tab + lf + space + tab
		outputChar := tab + lf + space + space
		program.WriteString(push(0))
		program.WriteString(inputNumber)
		program.WriteString(push(1))
		program.WriteString(inputNumber)
		program.WriteString(push(0))
		program.WriteString(retrieve)
		program.WriteString(push(1))
		program.WriteString(retrieve)
		program.WriteString(add)
		program.WriteString(outputNumber)
		program.WriteString(push(10))
		program.WriteString(outputChar)
		program.WriteString(lf)
		program.WriteString(lf)
		program.WriteString(lf)
		return program.String()
	}

	return map[string]compileExecuteCase{
		"ada": {
			compileLang:     "ADA",
			compileVariants: []string{"ADA2012", "ADA2022"},
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.adb", `with Ada.Text_IO; use Ada.Text_IO;
with Ada.Integer_Text_IO; use Ada.Integer_Text_IO;
procedure Main is
  F : File_Type;
  A, B, Sum : Integer;
begin
  Get(A);
  Get(B);
  Create(F, Out_File, "same-folder.txt");
  Put(F, A + B, Width => 0);
  New_Line(F);
  Close(F);
  Open(F, In_File, "same-folder.txt");
  Get(F, Sum);
  Close(F);
  Put(Sum, Width => 0);
  New_Line;
end Main;`),
			},
		},
		"plain": {
			compileLang: "C11",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.c", `#include <stdio.h>
int main(void) {
    int a, b;
    if (scanf("%d%d", &a, &b) != 2) {
        return 1;
    }
    FILE *out = fopen("same-folder.txt", "w");
    if (out == NULL) {
        return 1;
    }
    fprintf(out, "%d\n", a + b);
    fclose(out);
    FILE *in = fopen("same-folder.txt", "r");
    if (in == NULL) {
        return 1;
    }
    int sum;
    if (fscanf(in, "%d", &sum) != 1) {
        fclose(in);
        return 1;
    }
    fclose(in);
    printf("%d\n", sum);
    return 0;
}`),
			},
		},
		"c": {
			compileLang:     "C11",
			compileVariants: []string{"C", "C89", "C99", "C17", "C18", "C23"},
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.c", `#include <stdio.h>
int main(void) {
    int a, b;
    if (scanf("%d%d", &a, &b) != 2) return 1;
    printf("%d\n", a + b);
    return 0;
}`),
			},
		},
		"cpp": {
			compileLang: "CPP17",
			compileVariants: []string{
				"CPP", "CPP98", "CPP03", "CPP11", "CPP14", "CPP20", "CPP23", "CPP26",
			},
			judgeIO: standardABJudgeIO,
			limits:  model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.cpp", `#include <iostream>
int main() {
    int a, b;
    if (!(std::cin >> a >> b)) return 1;
    std::cout << a + b << '\n';
    return 0;
}`),
			},
		},
		"aheui": {
			compileLang: "AHEUI",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.aheui", "방방다망반발따맣희"),
			},
		},
		"awk": {
			compileLang: "AWK",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.awk", `{ print $1 + $2; exit }`),
			},
		},
		"tcl": {
			compileLang: "TCL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.tcl", `scan [gets stdin] "%d %d" a b
puts [expr {$a + $b}]`),
			},
		},
		"asm": {
			compileLang: "ASM",
			judgeIO:     singleDigitABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.s", `.global _start
.section .bss
.lcomm buf, 4
.section .text
_start:
    xor %rax, %rax
    xor %rdi, %rdi
    lea buf(%rip), %rsi
    mov $4, %rdx
    syscall
    movzbl buf(%rip), %eax
    addb buf+2(%rip), %al
    subb $48, %al
    movb %al, buf(%rip)
    movb $10, buf+1(%rip)
    mov $1, %rax
    mov $1, %rdi
    lea buf(%rip), %rsi
    mov $2, %rdx
    syscall
    mov $60, %rax
    xor %rdi, %rdi
    syscall`),
			},
		},
		"bf": {
			compileLang: "BF",
			judgeIO:     twoDigitABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.bf", ",>,>,>,>,<[<<<+>>>-]++++++[<<<-------->>>-]<<<.>>>>[<<<+>>>-]++++++[<<<-------->>>-]<<<.>[-]++++++++++."),
			},
		},
		"befunge": {
			compileLang: "BEFUNGE",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.bef", `>&&+:91+/68*+,91+%68*+,52*,@`),
			},
		},
		"lolcode": {
			compileLang: "LOLCODE",
			judgeIO:     lineSeparatedABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.lol", `HAI 1.2
I HAS A X
I HAS A Y
GIMMEH X
GIMMEH Y
X IS NOW A NUMBR
Y IS NOW A NUMBR
VISIBLE SUM OF X AN Y
KTHXBYE
`),
			},
		},
		"apecode": {
			compileLang:    "APECODE",
			stdin:          "1\n3\n3 1 2\n",
			expectedStdout: "3 1 2\n",
			nonABReason:    "APECode implements the BAPC sorting-network protocol rather than general arithmetic",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.ape", `state main {
  return true;
}`),
			},
		},
		"j": {
			compileLang: "J",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.ijs", "input =: 0 \". (1!:1[3) -. CR,LF\necho +/ input\nexit 0\n"),
			},
		},
		"clojure": {
			compileLang: "CLOJURE",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.clj", `(require '[clojure.string :as str])
(let [[a b] (map parse-long (str/split (str/trim (slurp *in*)) #"\s+"))
      sum (+ a b)]
  (spit "same-folder.txt" sum)
  (println (str/trim (slurp "same-folder.txt"))))`),
			},
		},
		"coq": {
			compileLang: "COQ",
			nonABReason: "proof verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.v", `Theorem same_folder_ok : 1 = 1.
Proof. reflexivity. Qed.`),
			},
		},
		"rocq": {
			compileLang: "ROCQ",
			nonABReason: "proof verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.v", `Theorem same_folder_ok : 1 = 1.
Proof. reflexivity. Qed.`),
			},
		},
		"lean4": {
			compileLang:     "LEAN4",
			compileVariants: []string{"LEAN"},
			nonABReason:     "proof verification completes during compile; execute is intentionally a no-op",
			limits:          model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.lean", `theorem ok : True := by trivial`),
			},
		},
		"agda": {
			compileLang: "AGDA",
			nonABReason: "proof verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.agda", `module Main where
data Unit : Set where
  tt : Unit
ok : Unit
ok = tt`),
			},
		},
		"dafny": {
			compileLang: "DAFNY",
			nonABReason: "proof verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.dfy", `method Main() ensures true {
}`),
			},
		},
		"tla": {
			compileLang:     "TLA",
			compileVariants: []string{"TLAPLUS"},
			nonABReason:     "the runtime model-checks a specification and does not expose solution stdin/stdout",
			limits:          model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.tla", `---- MODULE Main ----
VARIABLE x
Init == x = 0
Next == x' = x
Spec == Init /\ [][Next]_x
====`),
				source("Main.cfg", `SPECIFICATION Spec
`),
			},
		},
		"why3": {
			compileLang:     "WHY3",
			compileVariants: []string{"WHYML"},
			nonABReason:     "proof verification completes during compile; execute is intentionally a no-op",
			limits:          model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.mlw", `theory Main
  goal G: true
end`),
			},
		},
		"isabelle": {
			compileLang: "ISABELLE",
			nonABReason: "proof verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 30000, MemoryMB: 3072},
			sources: []model.Source{
				source("ROOT", `session Aonohako = HOL +
  theories Aonohako_Main`),
				source("Aonohako_Main.thy", `theory Aonohako_Main
  imports Main
begin
theorem ok: True by simp
end`),
			},
		},
		"fstar": {
			compileLang: "FSTAR",
			nonABReason: "proof verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 20000, MemoryMB: 2048},
			sources: []model.Source{
				source("Main.fst", `module Main
let ok () : Lemma (1 + 1 == 2) = ()`),
			},
		},
		"alloy": {
			compileLang: "ALLOY",
			nonABReason: "model verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 20000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.als", `sig A {}
assert ok { all a: A | a = a }
check ok for 3
run { some A } for 1`),
			},
		},
		"acl2": {
			compileLang: "ACL2",
			nonABReason: "proof verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 20000, MemoryMB: 2048},
			sources: []model.Source{
				source("Main.lisp", `(in-package "ACL2")
(defthm plus-zero-right
  (implies (integerp x)
           (equal (+ x 0) x)))`),
			},
		},
		"kframework": {
			compileLang: "KFRAMEWORK",
			nonABReason: "definition verification completes during compile; execute is intentionally a no-op",
			limits:      model.Limits{TimeMs: 60000, MemoryMB: 4096},
			sources: []model.Source{
				source("Main.k", `module MAIN
  imports INT
  syntax Exp ::= Int
endmodule`),
			},
		},
		"csharp": {
			compileLang:     "CSHARP",
			compileAttempts: 8,
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Program.cs", `var values = Array.ConvertAll(
    Console.In.ReadToEnd().Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries),
    int.Parse);
System.IO.File.WriteAllText("same-folder.txt", (values[0] + values[1]).ToString());
Console.WriteLine(System.IO.File.ReadAllText("same-folder.txt"));`),
			},
		},
		"crystal": {
			compileLang: "CRYSTAL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.cr", `values = STDIN.gets_to_end.split.map(&.to_i)
puts values[0] + values[1]`),
			},
		},
		"cobol": {
			compileLang: "COBOL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.cob", `IDENTIFICATION DIVISION.
PROGRAM-ID. Main.
DATA DIVISION.
WORKING-STORAGE SECTION.
01 LINE-IN PIC X(80).
01 A PIC 9(9).
01 B PIC 9(9).
01 TOTAL PIC 9(9).
01 TOTAL-OUT PIC Z(8)9.
PROCEDURE DIVISION.
    ACCEPT LINE-IN.
    UNSTRING LINE-IN DELIMITED BY ALL SPACE INTO A B.
    COMPUTE TOTAL = A + B.
    MOVE TOTAL TO TOTAL-OUT.
    DISPLAY FUNCTION TRIM(TOTAL-OUT).
    STOP RUN.`),
			},
		},
		"gnucobol": {
			compileLang: "GNUCOBOL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.cob", `IDENTIFICATION DIVISION.
PROGRAM-ID. Main.
DATA DIVISION.
WORKING-STORAGE SECTION.
01 LINE-IN PIC X(80).
01 A PIC 9(9).
01 B PIC 9(9).
01 TOTAL PIC 9(9).
01 TOTAL-OUT PIC Z(8)9.
PROCEDURE DIVISION.
    ACCEPT LINE-IN.
    UNSTRING LINE-IN DELIMITED BY ALL SPACE INTO A B.
    COMPUTE TOTAL = A + B.
    MOVE TOTAL TO TOTAL-OUT.
    DISPLAY FUNCTION TRIM(TOTAL-OUT).
    STOP RUN.`),
			},
		},
		"cython": {
			compileLang: "CYTHON",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.pyx", `a, b = map(int, input().split())
print(a + b)`),
			},
		},
		"objective-c": {
			compileLang:     "OBJECTIVE_C",
			compileVariants: []string{"OBJC"},
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.m", `#include <stdio.h>
int main(void) {
    int a, b;
    if (scanf("%d%d", &a, &b) != 2) return 1;
    printf("%d\n", a + b);
    return 0;
}`),
			},
		},
		"objective-cpp": {
			compileLang:     "OBJECTIVE_CPP",
			compileVariants: []string{"OBJCPP"},
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.mm", `#include <iostream>
int main() {
    int a, b;
    if (!(std::cin >> a >> b)) return 1;
    std::cout << a + b << '\n';
    return 0;
}`),
			},
		},
		"vlang": {
			compileLang: "VLANG",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.v", `import os

fn main() {
  values := os.input('').fields()
  println(values[0].int() + values[1].int())
}`),
			},
		},
		"vala": {
			compileLang: "VALA",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.vala", `int main() {
  int a;
  int b;
  stdin.scanf("%d %d", out a, out b);
  stdout.printf("%d\n", a + b);
  return 0;
}`),
			},
		},
		"odin": {
			compileLang: "ODIN",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("main.odin", `package main
import "core:fmt"
import "core:os"
import "core:strconv"
import "core:strings"
main :: proc() {
  data, err := os.read_entire_file_from_file(os.stdin, context.allocator)
  if err != nil {
    return
  }
  defer delete(data)
  fields, _ := strings.fields(string(data), context.allocator)
  defer delete(fields)
  a, _ := strconv.parse_int(fields[0])
  b, _ := strconv.parse_int(fields[1])
  fmt.println(a + b)
}`),
			},
		},
		"c3": {
			compileLang: "C3",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.c3", `import std::io;
extern fn int scanf(char* format, ...);
fn void main() {
  int a;
  int b;
  scanf("%d %d", &a, &b);
  io::printfn("%d", a + b);
}`),
			},
		},
		"hare": {
			compileLang: "HARE",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.ha", `use bufio;
use fmt;
use os;
use strconv;
use strings;

export fn main() void = {
  const a_data = bufio::read_tok(os::stdin, ' ')! as []u8;
  defer free(a_data);
  const b_data = bufio::read_tok(os::stdin, '\n')! as []u8;
  defer free(b_data);
  const a = strconv::stoi(strings::fromutf8(a_data)!)!;
  const b = strconv::stoi(strings::fromutf8(b_data)!)!;
  fmt::println(a + b)!;
};`),
			},
		},
		"d": {
			compileLang: "D",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.d", `import std.conv : to;
import std.file : readText, writeFile = write;
import std.stdio : readf, write;

void main() {
    int a, b;
    readf(" %d %d", &a, &b);
    writeFile("same-folder.txt", (a + b).to!string);
    write(readText("same-folder.txt"), "\n");
}`),
			},
		},
		"dart": {
			compileLang: "DART",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.dart", `import 'dart:io';

void main() {
  final values = stdin.readLineSync()!.trim().split(RegExp(r'\s+')).map(int.parse).toList();
  File('same-folder.txt').writeAsStringSync('${values[0] + values[1]}');
  stdout.writeln(File('same-folder.txt').readAsStringSync());
}`),
			},
		},
		"elixir": {
			compileLang: "ELIXIR",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.exs", `sum =
  IO.read(:stdio, :eof)
  |> String.split()
  |> Enum.map(&String.to_integer/1)
  |> Enum.sum()

File.write!("same-folder.txt", Integer.to_string(sum))
IO.puts(File.read!("same-folder.txt"))`),
			},
		},
		"erlang": {
			compileLang: "ERLANG",
			entryPoint:  "main:main",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("main.erl", `-module(main).
-export([main/0]).

main() ->
    {ok, [A, B]} = io:fread("", "~d ~d"),
    ok = file:write_file("same-folder.txt", integer_to_binary(A + B)),
    {ok, Data} = file:read_file("same-folder.txt"),
    io:format("~s~n", [Data]).`),
			},
		},
		"mercury": {
			compileLang: "MERCURY",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("main.m", `:- module main.
:- interface.
:- import_module io.
:- pred main(io::di, io::uo) is det.
:- implementation.
:- import_module int, list, string.

main(!IO) :-
    io.read_line_as_string(Result, !IO),
    ( if
        Result = ok(Line),
        string.words(Line) = [AString, BString],
        string.to_int(AString, A),
        string.to_int(BString, B)
      then
        io.write_int(A + B, !IO),
        io.nl(!IO)
      else
        io.set_exit_status(1, !IO)
    ).
`),
			},
		},
		"malbolge": {
			compileLang:    "MALBOLGE",
			expectedStdout: "Hello World!",
			nonABReason:    "the canonical reference conformance vector is used to exercise Malbolge self-modification",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.mal", "('&%:9]!~}|z2Vxwv-,POqponl$Hjig%eB@@>}=<M:9wv6WsU2T|nm-,jcL(I&%$#\"`CB]V?Tx<uVtT`Rpo3NlF.Jh++FdbCBA@?]!~|4XzyTT43Qsqq(Lnmkj\"Fhg${z@>"),
			},
		},
		"fortran": {
			compileLang: "FORTRAN",
			compileVariants: []string{
				"FORTRAN95", "FORTRAN2003", "FORTRAN2008", "FORTRAN2018",
			},
			judgeIO: standardABJudgeIO,
			limits:  model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.f90", `program main
  implicit none
  integer :: a, b, total
  character(len=32) :: output
  read(*,*) a, b
  open(unit=10, file='same-folder.txt', status='replace', action='write')
  write(10, *) a + b
  close(10)
  open(unit=11, file='same-folder.txt', status='old', action='read')
  read(11, *) total
  close(11)
  write(output, '(I20)') total
  print '(A)', trim(adjustl(output))
end program main`),
			},
		},
		"fsharp": {
			compileLang:     "FSHARP",
			compileAttempts: 20,
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Program.fs", `open System.IO

[<EntryPoint>]
let main _ =
    let values =
        System.Console.In.ReadToEnd().Split(
            [|' '; '\t'; '\r'; '\n'|],
            System.StringSplitOptions.RemoveEmptyEntries)
        |> Array.map int
    File.WriteAllText("same-folder.txt", string (values[0] + values[1]))
    printfn "%s" (File.ReadAllText("same-folder.txt"))
    0`),
			},
		},
		"gdl": {
			compileLang: "GDL",
			nonABReason: "the runtime reserves stdin for GDL compile and entrypoint commands",
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.pro", `pro main
end`),
			},
		},
		"gleam": {
			compileLang: "GLEAM",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.gleam", `import gleam/int
import gleam/io

@external(erlang, "aonohako_input", "read_sum")
fn read_sum() -> Int

pub fn main() {
  read_sum()
  |> int.to_string
  |> io.println
}`),
				source("src/aonohako_input.erl", `-module(aonohako_input).
-export([read_sum/0]).

read_sum() ->
    {ok, [A, B]} = io:fread("", "~d ~d"),
    A + B.
`),
			},
		},
		"sml": {
			compileLang: "SML",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.sml", `fun scanInt () = valOf (TextIO.scanStream (Int.scan StringCvt.DEC) TextIO.stdIn)
val a = scanInt ()
val b = scanInt ()
val _ = print (Int.toString (a + b) ^ "\n")
`),
			},
		},
		"go": {
			compileLang: "GO",
			judgeIO:     standardABJudgeIO,
			// Saet's default 32 MiB problem limit becomes 1120 MiB after the
			// Go runtime reserve. This used to produce an unsafe 1184 MiB
			// RLIMIT_AS and fail before main.
			limits: model.Limits{TimeMs: 8000, MemoryMB: 1120},
			sources: []model.Source{
				source("main.go", `package main

import (
	"fmt"
	"os"
)

func main() {
	var a, b int
	if _, err := fmt.Fscan(os.Stdin, &a, &b); err != nil {
		panic(err)
	}
	if err := os.WriteFile("same-folder.txt", []byte(fmt.Sprintf("%d\n", a+b)), 0o644); err != nil {
		panic(err)
	}
	data, err := os.ReadFile("same-folder.txt")
	if err != nil {
		panic(err)
	}
	fmt.Print(string(data))
}`),
			},
		},
		"groovy": {
			compileLang: "GROOVY",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.groovy", `class Main {
    static void main(String[] args) {
        def values = System.in.text.trim().split(/\s+/)*.toInteger()
        new File("same-folder.txt").text = (values[0] + values[1]).toString()
        println new File("same-folder.txt").text.trim()
    }
}`),
			},
		},
		"haskell": {
			compileLang: "HASKELL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.hs", `main :: IO ()
main = do
  values <- map read . words <$> getContents
  writeFile "same-folder.txt" (show (sum (take 2 values)))
  readFile "same-folder.txt" >>= putStrLn`),
			},
		},
		"idris2": {
			compileLang: "IDRIS2",
			judgeIO:     lineSeparatedABJudgeIO,
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.idr", `module Main

main : IO ()
main = do
  a <- getLine
  b <- getLine
  printLn (the Integer (cast a + cast b))
`),
			},
		},
		"haxe": {
			compileLang: "HAXE",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 10000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.hx", `class Main {
  static public function main() {
    var values = ~/\s+/g.split(Sys.stdin().readAll().toString());
    Sys.println(Std.parseInt(values[0]) + Std.parseInt(values[1]));
  }
}`),
			},
		},
		"java": {
			compileLang:     "JAVA11",
			compileVariants: []string{"JAVA", "JAVA8", "JAVA15", "JAVA17", "JAVA21"},
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.java", `import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.StringTokenizer;

public class Main {
  public static void main(String[] args) throws Exception {
    BufferedReader in = new BufferedReader(new InputStreamReader(System.in));
    StringTokenizer values = new StringTokenizer(in.readLine());
    int sum = Integer.parseInt(values.nextToken()) + Integer.parseInt(values.nextToken());
    Path path = Paths.get("same-folder.txt");
    Files.write(path, Integer.toString(sum).getBytes(StandardCharsets.UTF_8));
    System.out.println(new String(Files.readAllBytes(path), StandardCharsets.UTF_8).trim());
  }
}`),
			},
		},
		"javascript": {
			compileLang: "JAVASCRIPT",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.js", `const fs = require('fs');
const values = fs.readFileSync(0, 'utf8').trim().split(/\s+/).map(Number);
fs.writeFileSync('same-folder.txt', String(values[0] + values[1]));
console.log(fs.readFileSync('same-folder.txt', 'utf8'));`),
			},
		},
		"coffeescript": {
			compileLang: "COFFEESCRIPT",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.coffee", `fs = require 'fs'
values = fs.readFileSync(0, 'utf8').trim().split(/\s+/).map(Number)
console.log values[0] + values[1]`),
			},
		},
		"julia": {
			compileLang: "JULIA",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.jl", `using Statistics

values = parse.(Int, split(read(stdin, String)))
@assert mean([1, 2, 3]) == 2
open("same-folder.txt", "w") do io
    write(io, string(sum(values)))
end
println(read("same-folder.txt", String))`),
			},
		},
		"kotlin": {
			compileLang: "KOTLIN",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.kt", `fun main() {
  val values = readLine()!!.trim().split(Regex("\\s+")).map(String::toInt)
  println(values[0] + values[1])
}`),
			},
		},
		"lisp": {
			compileLang: "LISP",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.lisp", `(let ((sum (+ (read) (read))))
  (with-open-file (out "same-folder.txt"
                     :direction :output
                     :if-exists :supersede
                     :if-does-not-exist :create)
    (format out "~d" sum)))
(with-open-file (in "same-folder.txt" :direction :input)
  (format t "~a~%" (read-line in nil "")))`),
			},
		},
		"picolisp": {
			compileLang: "PICOLISP",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.l", `(in NIL
  (let (A (read) B (read))
    (prinl (+ A B))))`),
			},
		},
		"lua": {
			compileLang: "LUA",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.lua", `local a, b = io.read("*n", "*n")
local out = assert(io.open("same-folder.txt", "w"))
out:write(a + b)
out:close()
local input = assert(io.open("same-folder.txt", "r"))
local data = input:read("*a")
input:close()
print(data)`),
			},
		},
		"nasm": {
			compileLang: "NASM",
			judgeIO:     singleDigitABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.asm", `default rel
global _start
section .bss
buf: resb 4
section .text
_start:
    xor eax, eax
    xor edi, edi
    lea rsi, [rel buf]
    mov edx, 4
    syscall
    mov al, [rel buf]
    add al, [rel buf + 2]
    sub al, '0'
    mov [rel buf], al
    mov byte [rel buf + 1], 10
    mov rax, 1
    mov rdi, 1
    lea rsi, [rel buf]
    mov rdx, 2
    syscall
    mov rax, 60
    xor rdi, rdi
    syscall`),
			},
		},
		"nim": {
			compileLang: "NIM",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.nim", `import std/[os, strutils]

let values = stdin.readAll.splitWhitespace
writeFile("same-folder.txt", $(parseInt(values[0]) + parseInt(values[1])))
echo readFile("same-folder.txt").strip()`),
			},
		},
		"ocaml": {
			compileLang: "OCAML",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.ml", `let () =
  Scanf.scanf "%d %d" (fun a b ->
  let out = open_out "same-folder.txt" in
  Printf.fprintf out "%d\n" (a + b);
  close_out out;
  let input = open_in "same-folder.txt" in
  print_string (input_line input);
  print_newline ();
  close_in input)`),
			},
		},
		"octave": {
			compileLang: "OCTAVE",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.m", `values = fscanf(stdin, "%d", 2);
disp(sum(values));`),
			},
		},
		"pascal": {
			compileLang: "PASCAL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.pas", `program Main;
var
  F: Text;
  A, B, Total: LongInt;
begin
  ReadLn(A, B);
  Assign(F, 'same-folder.txt');
  Rewrite(F);
  Writeln(F, A + B);
  Close(F);
  Assign(F, 'same-folder.txt');
  Reset(F);
  ReadLn(F, Total);
  Close(F);
  Writeln(Total);
end.`),
			},
		},
		"delphi": {
			compileLang: "DELPHI",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.dpr", `program Main;
var
  F: TextFile;
  A, B, Total: LongInt;
begin
  ReadLn(A, B);
  AssignFile(F, 'same-folder.txt');
  Rewrite(F);
  Writeln(F, A + B);
  CloseFile(F);
  AssignFile(F, 'same-folder.txt');
  Reset(F);
  ReadLn(F, Total);
  CloseFile(F);
  Writeln(Total);
end.`),
			},
		},
		"objectpascal": {
			compileLang: "OBJECTPASCAL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.pas", `program Main;
var
  A, B: LongInt;
begin
  ReadLn(A, B);
  Writeln(A + B);
end.`),
			},
		},
		"perl": {
			compileLang: "PERL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.pl", `my @values = split /\s+/, do { local $/; <STDIN> };
open my $fh, '>', 'same-folder.txt' or die $!;
print {$fh} $values[0] + $values[1];
close $fh;
open my $rfh, '<', 'same-folder.txt' or die $!;
print scalar <$rfh>, "\n";
close $rfh;`),
			},
		},
		"php": {
			compileLang:     "PHP",
			compileVariants: []string{"PHP7", "PHP8"},
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.php", `<?php
$values = preg_split('/\s+/', trim(stream_get_contents(STDIN)));
file_put_contents('same-folder.txt', ((int)$values[0] + (int)$values[1]) . "\n");
echo file_get_contents('same-folder.txt');`),
			},
		},
		"prolog": {
			compileLang: "PROLOG",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.pl", `:- use_module(library(readutil)).

main :-
    read_line_to_string(user_input, Line),
    split_string(Line, " ", " ", Strings),
    maplist(number_string, Numbers, Strings),
    sum_list(Numbers, Sum),
    writeln(Sum).`),
			},
		},
		"pypy": {
			compileLang: "PYPY3",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.py", `from pathlib import Path

values = list(map(int, input().split()))
Path("same-folder.txt").write_text(str(sum(values)), encoding="utf-8")
print(Path("same-folder.txt").read_text(encoding="utf-8"))`),
			},
		},
		"python": {
			compileLang:       "PYTHON3",
			judgeIO:           standardABJudgeIO,
			limits:            model.Limits{TimeMs: 30000, MemoryMB: 1024},
			pythonLibraryMode: pythonpolicy.LibraryModeInstalled,
			sources: []model.Source{
				source("Main.py", `import pathlib
import numpy as np
import pandas as pd
import PIL.Image
import qiskit
import seaborn as sns

values = list(map(int, input().split()))
total = sum(values)
assert int(np.arange(5).sum()) == 10
assert int(pd.Series([1, 2, 3]).sum()) == 6
assert callable(PIL.Image.new)
assert callable(qiskit.QuantumCircuit)
assert sns.__version__
pathlib.Path("same-folder.txt").write_text(str(total), encoding="utf-8")
print(pathlib.Path("same-folder.txt").read_text(encoding="utf-8"))`),
			},
		},
		"r": {
			compileLang: "R",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.R", `values <- scan(file("stdin"), quiet = TRUE, nmax = 2)
writeLines(as.character(sum(values)), "same-folder.txt")
cat(readLines("same-folder.txt"), sep = "\n")
cat("\n")`),
			},
		},
		"raku": {
			compileLang: "RAKU",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.raku", `say $*IN.slurp.words.map(*.Int).sum;`),
			},
		},
		"racket": {
			compileLang: "RACKET",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.rkt", `#lang racket
(require racket/string)
(define sum (+ (read) (read)))
(call-with-output-file "same-folder.txt"
  (lambda (out) (displayln sum out))
  #:exists 'replace)
(displayln (string-trim (file->string "same-folder.txt")))`),
			},
		},
		"ruby": {
			compileLang: "RUBY",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.rb", `values = STDIN.read.split.map(&:to_i)
File.write("same-folder.txt", "#{values.sum}\n")
print File.read("same-folder.txt")`),
			},
		},
		"rust": {
			compileLang:     "RUST2024",
			compileVariants: []string{"RUST", "RUST2015", "RUST2018", "RUST2021"},
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("main.rs", `use std::{fs, io::{self, Read}};

fn main() {
    let mut input = String::new();
    io::stdin().read_to_string(&mut input).unwrap();
    let sum: i32 = input.split_whitespace().map(|value| value.parse::<i32>().unwrap()).sum();
    fs::write("same-folder.txt", format!("{}\n", sum)).unwrap();
    print!("{}", fs::read_to_string("same-folder.txt").unwrap());
}`),
			},
		},
		"scala": {
			compileLang: "SCALA",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.scala", `object Main extends App {
  val values = scala.io.Source.stdin.mkString.trim.split("\\s+").map(_.toInt)
  val path = new java.io.File("same-folder.txt")
  val writer = new java.io.PrintWriter(path, "UTF-8")
  writer.write((values(0) + values(1)).toString)
  writer.close()
  println(scala.io.Source.fromFile(path, "UTF-8").mkString.trim)
}`),
			},
		},
		"sqlite": {
			compileLang: "SQLITE",
			judgeIO:     sqlABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.sql", `select a + b from input;`),
			},
		},
		"sed": {
			compileLang: "SED",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.sed", `s/^20[[:space:]][[:space:]]*22$/42/
t
s/^7[[:space:]][[:space:]]*13$/20/`),
			},
		},
		"bc": {
			compileLang: "BC",
			judgeIO:     lineSeparatedABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.bc", "a=read()\nb=read()\na+b\n"),
			},
		},
		"scheme": {
			compileLang: "SCHEME",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.scm", `(import (scheme base) (scheme read) (scheme write))
(display (+ (read) (read)))
(newline)`),
			},
		},
		"swift": {
			compileLang: "SWIFT",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.swift", `import Foundation

let data = FileHandle.standardInput.readDataToEndOfFile()
let values = String(data: data, encoding: .utf8)!.split(whereSeparator: { $0.isWhitespace }).map { Int($0)! }
try! String(values[0] + values[1]).write(toFile: "same-folder.txt", atomically: true, encoding: .utf8)
print(try! String(contentsOfFile: "same-folder.txt").trimmingCharacters(in: .whitespacesAndNewlines))`),
			},
		},
		"typescript": {
			compileLang: "TYPESCRIPT",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.ts", `declare const require: any;
const fs = require('fs');
const values = fs.readFileSync(0, 'utf8').trim().split(/\s+/).map(Number);
fs.writeFileSync('same-folder.txt', String(values[0] + values[1]));
console.log(fs.readFileSync('same-folder.txt', 'utf8'));`),
			},
		},
		"uhmlang": {
			compileLang: "UHMLANG",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.uhm", "어떻게\n\n엄식?\n어엄식?\n\n동탄어?준... ....\n\n엄어,\n어엄어어.\n\n준.. ...\n식어어!\n\n이 사람이름이냐ㅋㅋ\n"),
			},
		},
		"vbnet": {
			compileLang:     "VBNET",
			compileVariants: []string{"VB"},
			compileAttempts: 8,
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Program.vb", `Imports System

Module Program
  Sub Main()
    Dim values = Array.ConvertAll(
      Console.In.ReadToEnd().Split(CType(Nothing, Char()), StringSplitOptions.RemoveEmptyEntries),
      AddressOf Integer.Parse)
    Console.WriteLine(values(0) + values(1))
  End Sub
End Module`),
			},
		},
		"vb6": {
			compileLang:    "VB6",
			expectedStdout: "ok\n",
			nonABReason:    "the compatibility runner supports literal Print statements only",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.bas", `Sub Main()
Print "ok"
End Sub`),
			},
		},
		"freebasic": {
			compileLang: "FREEBASIC",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.bas", `dim a as integer, b as integer
input ""; a, b
print ltrim(str(a + b))`),
			},
		},
		"classic-basic": {
			compileLang: "CLASSIC_BASIC",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.bas", `DIM A AS INTEGER, B AS INTEGER
INPUT ""; A, B
PRINT LTRIM$(STR$(A + B))`),
			},
		},
		"qbasic": {
			compileLang: "QBASIC",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.bas", `DIM A AS INTEGER, B AS INTEGER
INPUT ""; A, B
PRINT LTRIM$(STR$(A + B))`),
			},
		},
		"vhdl": {
			compileLang: "VHDL",
			entryPoint:  "main_tb",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.vhd", `use std.textio.all;

entity main_tb is
end entity;

architecture sim of main_tb is
begin
  process
    variable input_line : line;
    variable output_line : line;
    variable a : integer;
    variable b : integer;
  begin
    readline(input, input_line);
    read(input_line, a);
    read(input_line, b);
    write(output_line, a + b);
    writeline(output, output_line);
    wait;
  end process;
end architecture;`),
			},
		},
		"verilog": {
			compileLang: "VERILOG",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.v", `module main;
  integer a;
  integer b;
  integer rc;
  initial begin
    rc = $fscanf(32'h80000000, "%d %d", a, b);
    if (rc != 2) $finish(1);
    $display("%0d", a + b);
    $finish(0);
  end
endmodule`),
			},
		},
		"systemverilog": {
			compileLang: "SYSTEMVERILOG",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.sv", `module main;
  integer a;
  integer b;
  integer rc;
  initial begin
    rc = $fscanf(32'h80000000, "%d %d", a, b);
    if (rc != 2) $finish(1);
    $display("%0d", a + b);
    $finish(0);
  end
endmodule`),
			},
		},
		"cuda-ocelot": {
			compileLang: "CUDA_OCELOT",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 2048},
			sources: []model.Source{
				source("Main.cu", `#include <cstdio>
int main() {
    int a, b;
    if (std::scanf("%d%d", &a, &b) != 2) return 1;
    std::printf("%d\n", a + b);
    return 0;
}`),
			},
		},
		"carbon": {
			compileLang: "CARBON",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.carbon", `import Core library "io";

fn ReadInt() -> i32 {
  var value: i32 = 0;
  var c: i32 = Core.ReadChar();
  while (c == 32 or c == 10 or c == 13 or c == 9) {
    c = Core.ReadChar();
  }
  while (c >= 48 and c <= 57) {
    value = value * 10 + c - 48;
    c = Core.ReadChar();
  }
  return value;
}

fn Run() -> i32 {
  let a: i32 = ReadInt();
  let b: i32 = ReadInt();
  Core.Print(a + b);
  return 0;
}`),
			},
		},
		"graphql": {
			compileLang:    "GRAPHQL",
			expectedStdout: "{\"data\":{\"answer\":42}}\n",
			nonABReason:    "the restricted GraphQL runner reads a query artifact and has no stdin variables contract",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.graphql", `query { answer }`),
			},
		},
		"smalltalk": {
			compileLang:     "SMALLTALK",
			compileVariants: []string{"GST"},
			judgeIO:         standardABJudgeIO,
			limits:          model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.st", `| values |
values := stdin nextLine subStrings collect: [ :value | value asInteger ].
((values at: 1) + (values at: 2)) displayNl`),
			},
		},
		"golfscript": {
			compileLang:    "GOLFSCRIPT",
			expectedStdout: "ok\n",
			nonABReason:    "the sandboxed compatibility runner accepts string literals only",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.gs", `"ok\n"`),
			},
		},
		"mojo": {
			compileLang: "MOJO",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.mojo", `def main() raises:
    var nums = input().split()
    print(Int(nums[0]) + Int(nums[1]))`),
			},
		},
		"moonbit": {
			compileLang: "MOONBIT",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.mbt", `extern "C" fn getchar() -> Int = "getchar"

fn read_int() -> Int {
  let mut c = getchar()
  while c == 32 || c == 10 || c == 13 || c == 9 {
    c = getchar()
  }
  let mut value = 0
  while c >= 48 && c <= 57 {
    value = value * 10 + c - 48
    c = getchar()
  }
  value
}

fn main {
  let a = read_int()
  let b = read_int()
  println(a + b)
}`),
			},
		},
		"fennel": {
			compileLang: "FENNEL",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.fnl", `(when os.execute (os.execute "touch /tmp/fennel-exec-leak"))
(local line (io.read))
(local (a b) (line:match "(%-?%d+)%s+(%-?%d+)"))
(print (+ (tonumber a) (tonumber b)))`),
			},
		},
		"deno": {
			compileLang: "DENO",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.ts", `const values = (await new Response(Deno.stdin.readable).text()).trim().split(/\s+/).map(Number);
console.log(values[0] + values[1]);`),
			},
		},
		"elm": {
			compileLang: "ELM",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 15000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.elm", `port module Main exposing (main)
import Platform
import String
port stdin : (String -> msg) -> Sub msg
port stdout : String -> Cmd msg
type Msg = Input String
main : Program () () Msg
main =
    Platform.worker
        { init = \_ -> ( (), Cmd.none )
        , update = \msg model ->
            case msg of
                Input input ->
                    ( model
                    , input
                        |> String.words
                        |> List.filterMap String.toInt
                        |> List.sum
                        |> String.fromInt
                        |> (\sum -> stdout (sum ++ "\n"))
                    )
        , subscriptions = \_ -> stdin Input
        }
`),
			},
		},
		"rescript": {
			compileLang: "RESCRIPT",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.res", "%%raw(`const fs = require(\"fs\");\nconst values = fs.readFileSync(0, \"utf8\").trim().split(/\\s+/).map(Number);\nconsole.log(values[0] + values[1]);`)"),
			},
		},
		"purescript": {
			compileLang: "PURESCRIPT",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 20000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.purs", `module Main where
import Prelude
import Effect (Effect)
foreign import printSum :: Effect Unit
main :: Effect Unit
main = printSum
`),
				source("Main.js", `import { readFileSync } from "node:fs";
export const printSum = () => {
  const values = readFileSync(0, "utf8").trim().split(/\s+/).map(Number);
  console.log(values[0] + values[1]);
};`),
			},
		},
		"kotlin-jvm": {
			compileLang: "KOTLIN_JVM",
			compileVariants: []string{
				"KOTLIN_JVM8",
				"KOTLIN_JVM11",
				"KOTLIN_JVM17",
				"KOTLIN_JVM21",
				"KOTLIN_JAVA",
				"KOTLIN_JAVA8",
				"KOTLIN_JAVA11",
				"KOTLIN_JAVA17",
				"KOTLIN_JAVA21",
			},
			judgeIO: standardABJudgeIO,
			limits:  model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.kt", `fun main() {
  val values = readLine()!!.trim().split(Regex("\\s+")).map(String::toInt)
  println(Helper.sum(values[0], values[1]))
}`),
				source("Helper.java", `final class Helper {
  static int sum(int a, int b) {
    return a + b;
  }
}`),
			},
		},
		"duckdb": {
			compileLang: "DUCKDB",
			judgeIO:     sqlABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.sql", `select a + b from input;`),
			},
		},
		"bqn": {
			compileLang: "BQN",
			judgeIO:     lineSeparatedABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.bqn", `•Out •Fmt (•ParseFloat •GetLine @) + •ParseFloat •GetLine @`),
			},
		},
		"apl": {
			compileLang:     "APL",
			compileVariants: []string{"GNU_APL"},
			expectedStdout:  "ok\n",
			nonABReason:     "the KANAPL script runner does not expose stdin to the submitted program",
			limits:          model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.apl", `⎕←'ok'`),
			},
		},
		"uiua": {
			compileLang: "UIUA",
			judgeIO:     twoDigitABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.ua", `⋕ &rs 2 0
◌ &rs 1 0
⋕ &rs 2 0
&p +`),
			},
		},
		"janet": {
			compileLang: "JANET",
			judgeIO:     lineSeparatedABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.janet", `(def a (scan-number (string/trim (string (file/read stdin :line)))))
(def b (scan-number (string/trim (string (file/read stdin :line)))))
(print (+ a b))`),
			},
		},
		"forth": {
			compileLang: "FORTH",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.fs", `create buf 128 allot
buf 128 stdin read-line throw drop
buf swap evaluate + 0 .r cr`),
			},
		},
		"gforth": {
			compileLang: "GFORTH",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.fs", `create buf 128 allot
buf 128 stdin read-line throw drop
buf swap evaluate + 0 .r cr`),
			},
		},
		"wasm": {
			compileLang: "WASM",
			judgeIO:     singleDigitABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.wat", `(module
  (import "wasi_snapshot_preview1" "fd_read"
    (func $fd_read (param i32 i32 i32 i32) (result i32)))
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))
  (memory 1)
  (export "memory" (memory 0))
  (func (export "_start")
    i32.const 0
    i32.const 64
    i32.store
    i32.const 4
    i32.const 4
    i32.store
    i32.const 0
    i32.const 0
    i32.const 1
    i32.const 8
    call $fd_read
    drop
    i32.const 80
    i32.const 64
    i32.load8_u
    i32.const 66
    i32.load8_u
    i32.add
    i32.const 48
    i32.sub
    i32.store8
    i32.const 81
    i32.const 10
    i32.store8
    i32.const 16
    i32.const 80
    i32.store
    i32.const 20
    i32.const 2
    i32.store
    i32.const 1
    i32.const 16
    i32.const 1
    i32.const 24
    call $fd_write
    drop))`),
			},
		},
		"whitespace": {
			compileLang: "WHITESPACE",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.ws", whitespaceABProgram()),
			},
		},
		"zerolang": {
			compileLang:    "ZEROLANG",
			expectedStdout: "ok\n",
			nonABReason:    "Zerolang v0.3.4 does not expose a stable stdin capability in its canonical source projection",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.0", `pub fn main(world: World) -> Void raises {
  check world.out.write("ok\n")
}`),
			},
		},
		"zig": {
			compileLang: "ZIG",
			judgeIO:     standardABJudgeIO,
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.zig", `const std = @import("std");

pub fn main() !void {
    var input_buf: [64]u8 = undefined;
    const input_len = try std.io.getStdIn().reader().readAll(&input_buf);
    var tokens = std.mem.tokenizeAny(u8, input_buf[0..input_len], " \r\n\t");
    const a = try std.fmt.parseInt(i64, tokens.next().?, 10);
    const b = try std.fmt.parseInt(i64, tokens.next().?, 10);
    var sum_buf: [32]u8 = undefined;
    const sum = try std.fmt.bufPrint(&sum_buf, "{}", .{a + b});
    try std.fs.cwd().writeFile(.{ .sub_path = "same-folder.txt", .data = sum });
    const data = try std.fs.cwd().readFileAlloc(std.heap.page_allocator, "same-folder.txt", 32);
    defer std.heap.page_allocator.free(data);
    try std.io.getStdOut().writer().print("{s}\n", .{data});
}`),
			},
		},
	}
}

func runDirectImagePermissionChecks() error {
	allowedExecutableTools := map[string]struct{}{}
	for _, tool := range strings.Fields(os.Getenv("AONOHAKO_SANDBOX_TOOLS")) {
		allowedExecutableTools[tool] = struct{}{}
	}

	if owned, err := sandboxIdentityOwnedImagePaths("/"); err != nil {
		return fmt.Errorf("sandbox-owned-image-paths: %w", err)
	} else if len(owned) != 0 {
		return fmt.Errorf("sandbox-owned-image-paths: unexpected uid/gid 65532 paths: %s", strings.Join(owned, ", "))
	}
	if owned, err := security.CommunicationIdentityOwnedImagePaths("/"); err != nil {
		return fmt.Errorf("communication-sandbox-owned-image-paths: %w", err)
	} else if len(owned) != 0 {
		paths := make([]string, 0, len(owned))
		for _, item := range owned {
			paths = append(paths, fmt.Sprintf("%s(uid=%d,gid=%d)", item.Path, item.UID, item.GID))
		}
		return fmt.Errorf("communication-sandbox-owned-image-paths: unexpected reserved identity paths: %s", strings.Join(paths, ", "))
	}

	protectedOut, protectedErr, err := runAsSandboxUser(
		"if [ -x /var/aonohako/protected ]; then echo leaked; else echo blocked; fi; "+
			"if [ -r /var/aonohako/protected/probe.txt ]; then echo leaked; else echo blocked; fi; "+
			"if [ -x /root ]; then echo leaked; else echo blocked; fi",
		"",
	)
	if err != nil {
		return fmt.Errorf("protected-paths-are-not-readable: %w\n%s", err, protectedErr)
	}
	if protectedOut != "blocked\nblocked\nblocked\n" {
		return fmt.Errorf("protected-paths-are-not-readable: unexpected stdout %q stderr %q", protectedOut, protectedErr)
	}

	imageOut, imageErr, err := runAsSandboxUser(
		"for p in /etc/debian_version /etc/os-release /etc/passwd /etc/group /etc/shells /etc/login.defs /var/lib/dpkg/status; do if [ -r \"$p\" ]; then echo leaked; else echo blocked; fi; done; "+
			"for d in /usr/share/doc /usr/share/common-licenses /usr/share/bash-completion /var/cache/debconf /etc/apt; do "+
			"if cd \"$d\" 2>/dev/null; then echo leaked; else echo blocked; fi; "+
			"done",
		"",
	)
	if err != nil {
		return fmt.Errorf("image-metadata-paths-are-not-readable: %w\n%s", err, imageErr)
	}
	if imageOut != strings.Repeat("blocked\n", 12) {
		return fmt.Errorf("image-metadata-paths-are-not-readable: unexpected stdout %q stderr %q", imageOut, imageErr)
	}

	toolOut, toolErr, err := runAsSandboxUser(
		"for p in "+
			"/usr/bin/apt /usr/bin/apt-get /usr/bin/apt-cache /usr/bin/apt-config "+
			"/usr/bin/dpkg /usr/bin/dpkg-query /usr/bin/dpkg-deb "+
			"/usr/bin/curl /usr/bin/wget /usr/bin/git /usr/local/bin/git "+
			"/usr/bin/pip /usr/bin/pip3 /usr/local/bin/pip /usr/local/bin/pip3 "+
			"/usr/local/bin/npm /usr/local/bin/npx /opt/node-*/bin/npm /opt/node-*/bin/npx "+
			"/usr/local/bin/yarn /usr/local/bin/pnpm "+
			"/usr/bin/gem /usr/local/bin/gem /usr/bin/bundle /usr/bin/bundler /usr/local/bin/bundle /usr/local/bin/bundler "+
			"/usr/bin/ssh /usr/bin/scp /usr/bin/sftp /usr/local/bin/ssh /usr/local/bin/scp /usr/local/bin/sftp "+
			"/usr/bin/rsync /usr/local/bin/rsync "+
			"/bin/nc /usr/bin/nc /usr/local/bin/nc /bin/netcat /usr/bin/netcat /usr/local/bin/netcat /usr/bin/ncat /usr/local/bin/ncat "+
			"/usr/bin/socat /usr/local/bin/socat /usr/bin/telnet /usr/local/bin/telnet /usr/bin/ftp /usr/local/bin/ftp /usr/bin/lftp /usr/local/bin/lftp "+
			"/usr/bin/gdb /usr/local/bin/gdb /usr/bin/gdbserver /usr/local/bin/gdbserver /usr/bin/strace /usr/local/bin/strace /usr/bin/ltrace /usr/local/bin/ltrace "+
			"/usr/bin/tcpdump /usr/local/bin/tcpdump /usr/bin/tshark /usr/local/bin/tshark /usr/bin/wireshark /usr/local/bin/wireshark /usr/bin/nmap /usr/local/bin/nmap "+
			"/usr/bin/dig /usr/local/bin/dig /usr/bin/nslookup /usr/local/bin/nslookup /usr/bin/host /usr/local/bin/host "+
			"/bin/ip /usr/bin/ip /usr/local/bin/ip /bin/ss /usr/bin/ss /usr/local/bin/ss /sbin/ifconfig /usr/sbin/ifconfig /usr/bin/ifconfig /usr/local/bin/ifconfig "+
			"/sbin/route /usr/sbin/route /usr/bin/route /usr/local/bin/route /bin/ping /usr/bin/ping /usr/local/bin/ping /bin/ping6 /usr/bin/ping6 /usr/local/bin/ping6 "+
			"/usr/bin/traceroute /usr/local/bin/traceroute /usr/bin/tracepath /usr/local/bin/tracepath /usr/bin/arp /usr/local/bin/arp /usr/bin/arping /usr/local/bin/arping; do "+
			"if [ -e \"$p\" ]; then "+
			"if [ -x \"$p\" ]; then echo \"$p leaked\"; else echo \"$p blocked\"; fi; "+
			"fi; "+
			"done",
		"",
	)
	if err != nil {
		return fmt.Errorf("image-package-tools-are-not-executable: %w\n%s", err, toolErr)
	}
	toolFields := strings.Fields(strings.TrimSpace(toolOut))
	if len(toolFields) == 0 {
		return fmt.Errorf("image-package-tools-are-not-executable: no package tools were checked")
	}
	for i := 0; i+1 < len(toolFields); i += 2 {
		if _, ok := allowedExecutableTools[filepath.Base(toolFields[i])]; ok {
			if toolFields[i+1] != "leaked" {
				return fmt.Errorf("image-package-tools-are-not-executable: allowed tool %s was not executable: stdout %q stderr %q", toolFields[i], toolOut, toolErr)
			}
			continue
		}
		if toolFields[i+1] != "blocked" {
			return fmt.Errorf("image-package-tools-are-not-executable: unexpected stdout %q stderr %q", toolOut, toolErr)
		}
	}

	moduleOut, moduleErr, err := runAsSandboxUser(
		"for p in "+
			"/usr/lib/python*/dist-packages/pip /usr/local/lib/python*/dist-packages/pip "+
			"/usr/lib/python*/site-packages/pip /usr/local/lib/python*/site-packages/pip "+
			"/usr/local/lib/node_modules/npm /opt/node-*/lib/node_modules/npm; do "+
			"if [ -e \"$p\" ]; then "+
			"if [ -r \"$p\" ] || [ -x \"$p\" ]; then echo \"$p leaked\"; else echo \"$p blocked\"; fi; "+
			"fi; "+
			"done",
		"",
	)
	if err != nil {
		return fmt.Errorf("image-package-module-paths-are-not-readable: %w\n%s", err, moduleErr)
	}
	moduleFields := strings.Fields(strings.TrimSpace(moduleOut))
	if len(moduleFields)%2 != 0 {
		return fmt.Errorf("image-package-module-paths-are-not-readable: unexpected stdout %q stderr %q", moduleOut, moduleErr)
	}
	for i := 0; i+1 < len(moduleFields); i += 2 {
		if moduleFields[i+1] != "blocked" {
			return fmt.Errorf("image-package-module-paths-are-not-readable: unexpected stdout %q stderr %q", moduleOut, moduleErr)
		}
	}

	if os.Getenv("AONOHAKO_PYTHON_LIBRARY_ISOLATION") == "true" {
		if got := strings.TrimSpace(os.Getenv("AONOHAKO_PYTHON_EXTERNAL_LIBRARY_GID")); got != strconv.FormatUint(uint64(pythonpolicy.ExternalLibraryGID), 10) {
			return fmt.Errorf("python-library-gid: got %q, want %d", got, pythonpolicy.ExternalLibraryGID)
		}
		pythonOut, pythonErr, err := runAsSandboxUser(
			"checked=0; "+
				"for p in /usr/lib/python*/dist-packages /usr/local/lib/python*/dist-packages /usr/lib/python*/site-packages /usr/local/lib/python*/site-packages /usr/share/python-wheels /usr/local/lib/aonohako/python; do "+
				"if [ -e \"$p\" ]; then checked=$((checked+1)); if [ -r \"$p\" ] || [ -x \"$p\" ]; then echo \"$p leaked\"; else echo \"$p blocked\"; fi; fi; "+
				"done; "+
				"if [ \"$checked\" -eq 0 ]; then exit 9; fi",
			"",
		)
		if err != nil {
			return fmt.Errorf("python-library-paths-are-not-readable: %w\n%s", err, pythonErr)
		}
		pythonFields := strings.Fields(strings.TrimSpace(pythonOut))
		if len(pythonFields) == 0 || len(pythonFields)%2 != 0 {
			return fmt.Errorf("python-library-paths-are-not-readable: unexpected stdout %q stderr %q", pythonOut, pythonErr)
		}
		for i := 0; i+1 < len(pythonFields); i += 2 {
			if pythonFields[i+1] != "blocked" {
				return fmt.Errorf("python-library-paths-are-not-readable: unexpected stdout %q stderr %q", pythonOut, pythonErr)
			}
		}

		importOut, importErr, err := runAsSandboxUser(
			"if python3 -c 'import numpy' >/dev/null 2>&1; then echo leaked; else echo blocked; fi",
			"",
		)
		if err != nil || importOut != "blocked\n" {
			return fmt.Errorf("python-library-import-without-group: stdout %q stderr %q err %v", importOut, importErr, err)
		}
		importOut, importErr, err = runAsSandboxUserWithGroups(
			"python3 -c 'import numpy; print(\"allowed\")'",
			"",
			[]uint32{pythonpolicy.ExternalLibraryGID},
		)
		if err != nil || importOut != "allowed\n" {
			return fmt.Errorf("python-library-import-with-group: stdout %q stderr %q err %v", importOut, importErr, err)
		}
	}

	scratchOut, scratchErr, err := runAsSandboxUser(
		"for p in /tmp /var/tmp /run/lock /dev/shm /dev/mqueue; do "+
			"if [ -e \"$p\" ]; then "+
			"if [ -w \"$p\" ]; then echo \"$p leaked\"; else echo \"$p blocked\"; fi; "+
			"fi; "+
			"done",
		"",
	)
	if err != nil {
		return fmt.Errorf("global-scratch-dirs-are-not-writable: %w\n%s", err, scratchErr)
	}
	lines := strings.Fields(strings.TrimSpace(scratchOut))
	if len(lines) == 0 {
		return fmt.Errorf("global-scratch-dirs-are-not-writable: no scratch dirs were checked")
	}
	for i := 0; i+1 < len(lines); i += 2 {
		if lines[i+1] != "blocked" {
			return fmt.Errorf("global-scratch-dirs-are-not-writable: unexpected stdout %q stderr %q", scratchOut, scratchErr)
		}
	}

	workDir, err := os.MkdirTemp("", "aonohako-selftest-work-*")
	if err != nil {
		return fmt.Errorf("mktemp selftest workdir: %w", err)
	}
	defer os.RemoveAll(workDir)
	if err := os.Chmod(workDir, 0o755); err != nil {
		return fmt.Errorf("chmod selftest workdir: %w", err)
	}

	boxDir := filepath.Join(workDir, "box")
	if err := os.MkdirAll(boxDir, 0o777); err != nil {
		return fmt.Errorf("mkdir selftest box: %w", err)
	}
	if err := os.Chmod(boxDir, 0o777|os.ModeSticky); err != nil {
		return fmt.Errorf("chmod selftest box: %w", err)
	}
	probePath := filepath.Join(boxDir, "probe")
	if err := os.WriteFile(probePath, []byte("immutable\n"), 0o555); err != nil {
		return fmt.Errorf("write selftest probe: %w", err)
	}

	workspaceOut, workspaceErr, err := runAsSandboxUser(
		"if printf mutated > probe 2>/dev/null; then echo overwrote; else echo blocked; fi; printf ok > note.txt; cat note.txt",
		boxDir,
	)
	if err != nil {
		return fmt.Errorf("workspace-is-writable-but-submission-is-immutable: %w\n%s", err, workspaceErr)
	}
	if workspaceOut != "blocked\nok" && workspaceOut != "blocked\nok\n" {
		return fmt.Errorf("workspace-is-writable-but-submission-is-immutable: unexpected stdout %q stderr %q", workspaceOut, workspaceErr)
	}
	return nil
}

func sandboxIdentityOwnedImagePaths(root string) ([]string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("root stat does not expose device information")
	}
	rootDevice := rootStat.Dev
	var owned []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("stat does not expose ownership for %s", path)
		}
		if stat.Dev != rootDevice {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if stat.Uid == 65532 || stat.Gid == 65532 {
			owned = append(owned, path)
		}
		return nil
	})
	return owned, err
}

func runAsSandboxUser(script, dir string) (string, string, error) {
	return runAsSandboxUserWithGroups(script, dir, nil)
}

func runAsSandboxUserWithGroups(script, dir string, supplementaryGroups []uint32) (string, string, error) {
	cmd := exec.Command("/bin/sh", "-lc", script)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"HOME=/tmp",
	}
	if os.Geteuid() == 0 {
		groups := append([]uint32{65532}, supplementaryGroups...)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: 65532, Gid: 65532, Groups: groups},
		}
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func runSuiteCases(cases []suiteCase) error {
	svc := execute.New()
	for _, tc := range cases {
		resp := svc.Run(context.Background(), &tc.req, execute.Hooks{})
		if resp.Status != model.RunStatusAccepted {
			return fmt.Errorf("%s: expected Accepted, got %+v", tc.name, resp)
		}
		if tc.check != nil {
			if err := tc.check(resp); err != nil {
				return err
			}
		}
	}
	return nil
}

func encodeScript(body string) string {
	return base64.StdEncoding.EncodeToString([]byte(body))
}

func uhmlangMemoryStressProgram() string {
	var program strings.Builder
	program.WriteString("어떻게\n")
	for i := 1; i <= 3000; i++ {
		program.WriteString(strings.Repeat("어", i))
		program.WriteString("엄.\n")
	}
	program.WriteString("식.ㅋ\n이 사람이름이냐ㅋㅋ\n")
	return program.String()
}
