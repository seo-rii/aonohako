package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	"aonohako/internal/profiles"
	"aonohako/internal/sandbox"

	"golang.org/x/sys/unix"
)

type suiteCase struct {
	name  string
	req   model.RunRequest
	check func(model.RunResponse) error
}

type compileExecuteCase struct {
	compileLang    string
	entryPoint     string
	expectedStdout string
	limits         model.Limits
	sources        []model.Source
}

const selftestUsage = "usage: aonohako-selftest image-permissions|permissions|compile-security|compile-execute|runtime-memory|cgroup-preflight|deployment-contract"

func main() {
	if sandbox.MaybeRunFromEnv() {
		return
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
	summary := struct {
		DeploymentTarget              platform.DeploymentTarget     `json:"deployment_target"`
		ExecutionTransport            platform.ExecutionTransport   `json:"execution_transport"`
		SandboxBackend                platform.SandboxBackend       `json:"sandbox_backend"`
		Contract                      string                        `json:"contract"`
		RequiresRootParent            bool                          `json:"requires_root_parent"`
		RequiresDedicatedWorkRoot     bool                          `json:"requires_dedicated_work_root"`
		RequiresSingleActiveRun       bool                          `json:"requires_single_active_run"`
		DelegatesIsolation            bool                          `json:"delegates_isolation"`
		Capabilities                  []platform.SecurityCapability `json:"capabilities,omitempty"`
		MissingCapabilities           []platform.SecurityCapability `json:"missing_capabilities,omitempty"`
		MaxActiveRuns                 int                           `json:"max_active_runs"`
		MaxPendingQueue               int                           `json:"max_pending_queue"`
		MaxActiveStreams              int                           `json:"max_active_streams"`
		MaxPrincipalStreams           int                           `json:"max_principal_streams"`
		MaxPrincipalRequestsPerMinute int                           `json:"max_principal_requests_per_minute"`
		HeartbeatIntervalSec          int64                         `json:"heartbeat_interval_sec"`
		RemoteSSEIdleTimeoutSec       int64                         `json:"remote_sse_idle_timeout_sec"`
		TrustedRunnerIngress          bool                          `json:"trusted_runner_ingress"`
		TrustedPlatformHeaders        bool                          `json:"trusted_platform_headers"`
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
		RequiresRootParent:            contract.RequiresRootParent,
		RequiresDedicatedWorkRoot:     contract.RequiresDedicatedWorkRoot,
		RequiresSingleActiveRun:       contract.RequiresSingleActiveRun,
		DelegatesIsolation:            contract.DelegatesIsolation,
		Capabilities:                  contract.Capabilities,
		MissingCapabilities:           contract.MissingCapabilities,
		MaxActiveRuns:                 cfg.MaxActiveRuns,
		MaxPendingQueue:               cfg.MaxPendingQueue,
		MaxActiveStreams:              cfg.MaxActiveStreams,
		MaxPrincipalStreams:           cfg.MaxPrincipalStreams,
		MaxPrincipalRequestsPerMinute: cfg.MaxPrincipalRequestsPerMinute,
		HeartbeatIntervalSec:          int64(cfg.HeartbeatInterval / time.Second),
		RemoteSSEIdleTimeoutSec:       int64(cfg.Execution.Remote.SSEIdleTimeout / time.Second),
		TrustedRunnerIngress:          cfg.TrustedRunnerIngress,
		TrustedPlatformHeaders:        cfg.TrustedPlatformHeaders,
		InboundAuth:                   cfg.InboundAuth.Mode,
		PlatformPrincipalHMAC:         strings.TrimSpace(cfg.InboundAuth.PlatformPrincipalHMACSecret) != "",
		RemoteAuth:                    cfg.Execution.Remote.Auth,
		RemoteURLConfigured:           strings.TrimSpace(cfg.Execution.Remote.URL) != "",
		CgroupParentConfigured:        strings.TrimSpace(cfg.Execution.Cgroup.ParentDir) != "",
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
			MaxActiveRuns:     1,
			MaxPendingQueue:   1,
			HeartbeatInterval: time.Second,
		},
		compile.New(),
		execute.New(),
	)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	cases := compileExecuteCases()
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

		profile, ok := profiles.Resolve(tc.compileLang)
		if !ok {
			return fmt.Errorf("compile-execute selftest could not resolve compile profile %q", tc.compileLang)
		}

		compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
			Lang:       tc.compileLang,
			Sources:    tc.sources,
			EntryPoint: tc.entryPoint,
		})
		if err != nil {
			return fmt.Errorf("%s compile request failed: %w", language, err)
		}
		if compileResp.Status != model.CompileStatusOK {
			return fmt.Errorf("%s compile failed: status=%s reason=%s stdout=%q stderr=%q", language, compileResp.Status, compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
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

		runResp, err := postExecuteRequest(httpServer.URL, model.RunRequest{
			Lang:           profile.RunLang,
			Binaries:       binaries,
			EntryPoint:     tc.entryPoint,
			ExpectedStdout: tc.expectedStdout,
			Limits:         limits,
		})
		if err != nil {
			return fmt.Errorf("%s execute request failed: %w", language, err)
		}
		if runResp.Status != model.RunStatusAccepted {
			return fmt.Errorf("%s execute failed: status=%s reason=%s stdout=%q stderr=%q", language, runResp.Status, runResp.Reason, runResp.Stdout, runResp.Stderr)
		}
	}

	_, _ = fmt.Fprintln(os.Stdout, "compile execute ok")
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

		switch language {
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
			covered++
		case "fsharp":
			compileResp, err := postCompileRequest(httpServer.URL, model.CompileRequest{
				Lang: "FSHARP",
				Sources: []model.Source{{
					Name: "Program.fs",
					DataB64: encodeScript(`open System.Collections.Generic

[<EntryPoint>]
let main _ =
    let chunks = ResizeArray<byte[]>()
    while true do
        chunks.Add(Array.zeroCreate<byte> (8 * 1024 * 1024))
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
		}
	}

	if covered == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "runtime memory ok (no covered languages)")
		return nil
	}
	_, _ = fmt.Fprintf(os.Stdout, "runtime memory ok (%d cases)\n", covered)
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

func compileExecuteCases() map[string]compileExecuteCase {
	source := func(name, body string) model.Source {
		return model.Source{Name: name, DataB64: encodeScript(body)}
	}
	whitespaceProgram := func(text string) string {
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
		outChar := tab + lf + space + space
		var program strings.Builder
		for _, ch := range text {
			program.WriteString(push(int(ch)))
			program.WriteString(outChar)
		}
		program.WriteString(lf)
		program.WriteString(lf)
		program.WriteString(lf)
		return program.String()
	}

	return map[string]compileExecuteCase{
		"ada": {
			compileLang:    "ADA",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.adb", `with Ada.Text_IO; use Ada.Text_IO;
procedure Main is
  F : File_Type;
begin
  Create(F, Out_File, "same-folder.txt");
  Put_Line(F, "ok");
  Close(F);
  Open(F, In_File, "same-folder.txt");
  Put_Line(Get_Line(F));
  Close(F);
end Main;`),
			},
		},
		"plain": {
			compileLang:    "C11",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.c", `#include <stdio.h>
int main(void) {
    FILE *out = fopen("same-folder.txt", "w");
    if (out == NULL) {
        return 1;
    }
    fputs("ok\n", out);
    fclose(out);
    FILE *in = fopen("same-folder.txt", "r");
    if (in == NULL) {
        return 1;
    }
    char buf[16] = {0};
    if (fgets(buf, sizeof buf, in) == NULL) {
        fclose(in);
        return 1;
    }
    fclose(in);
    fputs(buf, stdout);
    return 0;
}`),
			},
		},
		"aheui": {
			compileLang:    "AHEUI",
			expectedStdout: "Hello, World!\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.aheui", "밞바밤밣받밞밞밞밦밞바밝밣바박박밦밞받밞받밞발밣받뱔희밞땨몋드떠받볋"),
			},
		},
		"asm": {
			compileLang:    "ASM",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.s", `.global _start
.section .text
_start:
    mov $1, %rax
    mov $1, %rdi
    lea msg(%rip), %rsi
    mov $3, %rdx
    syscall
    mov $60, %rax
    xor %rdi, %rdi
    syscall
.section .rodata
msg:
    .ascii "ok\n"`),
			},
		},
		"bf": {
			compileLang:    "BF",
			expectedStdout: "Hello World!\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.bf", "++++++++++[>+++++++>++++++++++>+++>+<<<<-]>++.>+.+++++++..+++.>++.<<+++++++++++++++.>.+++.------.--------.>+.>."),
			},
		},
		"clojure": {
			compileLang:    "CLOJURE",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.clj", `(require '[clojure.string :as str])
(spit "same-folder.txt" "ok")
(println (str/trim (slurp "same-folder.txt")))`),
			},
		},
		"coq": {
			compileLang: "COQ",
			limits:      model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.v", `Theorem same_folder_ok : 1 = 1.
Proof. reflexivity. Qed.`),
			},
		},
		"csharp": {
			compileLang:    "CSHARP",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Program.cs", `System.IO.File.WriteAllText("same-folder.txt", "ok");
Console.WriteLine(System.IO.File.ReadAllText("same-folder.txt"));`),
			},
		},
		"d": {
			compileLang:    "D",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.d", `import std.file : readText, write;
import std.stdio : writeln;

void main() {
    write("same-folder.txt", "ok");
    writeln(readText("same-folder.txt"));
}`),
			},
		},
		"dart": {
			compileLang:    "DART",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.dart", `import 'dart:io';

void main() {
  File('same-folder.txt').writeAsStringSync('ok');
  stdout.writeln(File('same-folder.txt').readAsStringSync().trim());
}`),
			},
		},
		"elixir": {
			compileLang:    "ELIXIR",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.exs", `File.write!("same-folder.txt", "ok")
IO.puts(File.read!("same-folder.txt"))`),
			},
		},
		"erlang": {
			compileLang:    "ERLANG",
			entryPoint:     "main:main",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("main.erl", `-module(main).
-export([main/0]).

main() ->
    ok = file:write_file("same-folder.txt", <<"ok">>),
    {ok, Data} = file:read_file("same-folder.txt"),
    io:format("~s~n", [Data]).`),
			},
		},
		"fortran": {
			compileLang:    "FORTRAN",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.f90", `program main
  implicit none
  character(len=32) :: line
  open(unit=10, file='same-folder.txt', status='replace', action='write')
  write(10, '(A)') 'ok'
  close(10)
  open(unit=11, file='same-folder.txt', status='old', action='read')
  read(11, '(A)') line
  close(11)
  print '(A)', trim(line)
end program main`),
			},
		},
		"fsharp": {
			compileLang:    "FSHARP",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Program.fs", `open System.IO

[<EntryPoint>]
let main _ =
    File.WriteAllText("same-folder.txt", "ok")
    printfn "%s" (File.ReadAllText("same-folder.txt"))
    0`),
			},
		},
		"go": {
			compileLang:    "GO",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("main.go", `package main

import (
	"fmt"
	"os"
)

func main() {
	if err := os.WriteFile("same-folder.txt", []byte("ok\n"), 0o644); err != nil {
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
			compileLang:    "GROOVY",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.groovy", `class Main {
    static void main(String[] args) {
        new File("same-folder.txt").text = "ok"
        println new File("same-folder.txt").text.trim()
    }
}`),
			},
		},
		"haskell": {
			compileLang:    "HASKELL",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.hs", `main :: IO ()
main = do
  writeFile "same-folder.txt" "ok"
  readFile "same-folder.txt" >>= putStrLn`),
			},
		},
		"java": {
			compileLang:    "JAVA11",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 768},
			sources: []model.Source{
				source("Main.java", `import java.nio.file.Files;
import java.nio.file.Path;

public class Main {
  public static void main(String[] args) throws Exception {
    Path path = Path.of("same-folder.txt");
    Files.writeString(path, "ok");
    System.out.println(Files.readString(path).trim());
  }
}`),
			},
		},
		"javascript": {
			compileLang:    "JAVASCRIPT",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.js", `const fs = require('fs');
fs.writeFileSync('same-folder.txt', 'ok');
console.log(fs.readFileSync('same-folder.txt', 'utf8'));`),
			},
		},
		"julia": {
			compileLang:    "JULIA",
			expectedStdout: "2.0\n",
			limits:         model.Limits{TimeMs: 15000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.jl", `using Statistics

open("same-folder.txt", "w") do io
    write(io, string(mean([1, 2, 3])))
end
println(read("same-folder.txt", String))`),
			},
		},
		"kotlin": {
			compileLang:    "KOTLIN",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.kt", `fun main() {
  println("ok")
}`),
			},
		},
		"lisp": {
			compileLang:    "LISP",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.lisp", `(with-open-file (out "same-folder.txt"
                     :direction :output
                     :if-exists :supersede
                     :if-does-not-exist :create)
  (write-line "ok" out))
(with-open-file (in "same-folder.txt" :direction :input)
  (format t "~a~%" (read-line in nil "")))`),
			},
		},
		"lua": {
			compileLang:    "LUA",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.lua", `local out = assert(io.open("same-folder.txt", "w"))
out:write("ok")
out:close()
local input = assert(io.open("same-folder.txt", "r"))
local data = input:read("*a")
input:close()
print(data)`),
			},
		},
		"nasm": {
			compileLang:    "NASM",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.asm", `default rel
global _start
section .text
_start:
    mov rax, 1
    mov rdi, 1
    lea rsi, [rel msg]
    mov rdx, msg_len
    syscall
    mov rax, 60
    xor rdi, rdi
    syscall
section .rodata
msg: db "ok", 10
msg_len equ $ - msg`),
			},
		},
		"nim": {
			compileLang:    "NIM",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.nim", `import std/[os, strutils]

writeFile("same-folder.txt", "ok")
echo readFile("same-folder.txt").strip()`),
			},
		},
		"ocaml": {
			compileLang:    "OCAML",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.ml", `let () =
  let out = open_out "same-folder.txt" in
  output_string out "ok\n";
  close_out out;
  let input = open_in "same-folder.txt" in
  print_string (input_line input);
  print_newline ();
  close_in input`),
			},
		},
		"pascal": {
			compileLang:    "PASCAL",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.pas", `program Main;
var
  F: Text;
  Line: string;
begin
  Assign(F, 'same-folder.txt');
  Rewrite(F);
  Writeln(F, 'ok');
  Close(F);
  Assign(F, 'same-folder.txt');
  Reset(F);
  ReadLn(F, Line);
  Close(F);
  Writeln(Line);
end.`),
			},
		},
		"perl": {
			compileLang:    "PERL",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.pl", `open my $fh, '>', 'same-folder.txt' or die $!;
print {$fh} "ok";
close $fh;
open my $rfh, '<', 'same-folder.txt' or die $!;
print scalar <$rfh>;
close $rfh;`),
			},
		},
		"php": {
			compileLang:    "PHP",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.php", `<?php
file_put_contents('same-folder.txt', "ok\n");
echo file_get_contents('same-folder.txt');`),
			},
		},
		"prolog": {
			compileLang:    "PROLOG",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.pl", `:- use_module(library(readutil)).

main :-
    open('same-folder.txt', write, Out),
    write(Out, 'ok'),
    close(Out),
    open('same-folder.txt', read, In),
    read_line_to_string(In, Line),
    close(In),
    writeln(Line).`),
			},
		},
		"pypy": {
			compileLang:    "PYPY3",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.py", `from pathlib import Path

Path("same-folder.txt").write_text("ok", encoding="utf-8")
print(Path("same-folder.txt").read_text(encoding="utf-8"))`),
			},
		},
		"python": {
			compileLang:    "PYTHON3",
			expectedStdout: "10\n",
			limits:         model.Limits{TimeMs: 30000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.py", `import pathlib
import numpy as np
import pandas as pd
import PIL.Image
import qiskit
import seaborn as sns

total = int(np.arange(5).sum())
assert int(pd.Series([1, 2, 3]).sum()) == 6
assert callable(PIL.Image.new)
assert callable(qiskit.QuantumCircuit)
assert sns.__version__
pathlib.Path("same-folder.txt").write_text(str(total), encoding="utf-8")
print(pathlib.Path("same-folder.txt").read_text(encoding="utf-8"))`),
			},
		},
		"r": {
			compileLang:    "R",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.R", `writeLines("ok", "same-folder.txt")
cat(readLines("same-folder.txt"), sep = "\n")`),
			},
		},
		"racket": {
			compileLang:    "RACKET",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 10000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.rkt", `#lang racket
(require racket/string)
(call-with-output-file "same-folder.txt"
  (lambda (out) (displayln "ok" out))
  #:exists 'replace)
(displayln (string-trim (file->string "same-folder.txt")))`),
			},
		},
		"ruby": {
			compileLang:    "RUBY",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.rb", `File.write("same-folder.txt", "ok\n")
print File.read("same-folder.txt")`),
			},
		},
		"rust": {
			compileLang:    "RUST2024",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 8000, MemoryMB: 1024},
			sources: []model.Source{
				source("main.rs", `use std::fs;

fn main() {
    fs::write("same-folder.txt", "ok\n").unwrap();
    print!("{}", fs::read_to_string("same-folder.txt").unwrap());
}`),
			},
		},
		"scala": {
			compileLang:    "SCALA",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1536},
			sources: []model.Source{
				source("Main.scala", `object Main extends App {
  val path = new java.io.File("same-folder.txt")
  val writer = new java.io.PrintWriter(path, "UTF-8")
  writer.write("ok")
  writer.close()
  println(scala.io.Source.fromFile(path, "UTF-8").mkString.trim)
}`),
			},
		},
		"sqlite": {
			compileLang:    "SQLITE",
			expectedStdout: "6\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.sql", `create table numbers(v integer);
insert into numbers(v) values (1),(2),(3);
select sum(v) from numbers;`),
			},
		},
		"swift": {
			compileLang:    "SWIFT",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.swift", `import Foundation

try! "ok".write(toFile: "same-folder.txt", atomically: true, encoding: .utf8)
print(try! String(contentsOfFile: "same-folder.txt").trimmingCharacters(in: .whitespacesAndNewlines))`),
			},
		},
		"typescript": {
			compileLang:    "TYPESCRIPT",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.ts", `declare const require: any;
const fs = require('fs');
fs.writeFileSync('same-folder.txt', 'ok');
console.log(fs.readFileSync('same-folder.txt', 'utf8'));`),
			},
		},
		"uhmlang": {
			compileLang:    "UHMLANG",
			expectedStdout: "X\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.uhm", "어떻게\n식........... ........ㅋ\n이 사람이름이냐ㅋㅋ\n"),
			},
		},
		"wasm": {
			compileLang:    "WASM",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.wat", `(module
  (import "wasi_snapshot_preview1" "fd_write"
    (func $fd_write (param i32 i32 i32 i32) (result i32)))
  (memory 1)
  (export "memory" (memory 0))
  (data (i32.const 8) "ok\n")
  (func (export "_start")
    i32.const 0
    i32.const 8
    i32.store
    i32.const 4
    i32.const 3
    i32.store
    i32.const 1
    i32.const 0
    i32.const 1
    i32.const 20
    call $fd_write
    drop))`),
			},
		},
		"whitespace": {
			compileLang:    "WHITESPACE",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 6000, MemoryMB: 512},
			sources: []model.Source{
				source("Main.ws", whitespaceProgram("ok\n")),
			},
		},
		"zig": {
			compileLang:    "ZIG",
			expectedStdout: "ok\n",
			limits:         model.Limits{TimeMs: 12000, MemoryMB: 1024},
			sources: []model.Source{
				source("Main.zig", `const std = @import("std");

pub fn main() !void {
    try std.fs.cwd().writeFile(.{ .sub_path = "same-folder.txt", .data = "ok" });
    const data = try std.fs.cwd().readFileAlloc(std.heap.page_allocator, "same-folder.txt", 16);
    defer std.heap.page_allocator.free(data);
    try std.io.getStdOut().writer().print("{s}\n", .{data});
}`),
			},
		},
	}
}

func runDirectImagePermissionChecks() error {
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
		"for p in /etc/debian_version /etc/os-release /var/lib/dpkg/status; do if [ -r \"$p\" ]; then echo leaked; else echo blocked; fi; done; "+
			"for d in /usr/share/doc /usr/share/common-licenses /usr/share/bash-completion /var/cache/debconf /etc/apt; do "+
			"if cd \"$d\" 2>/dev/null; then echo leaked; else echo blocked; fi; "+
			"done",
		"",
	)
	if err != nil {
		return fmt.Errorf("image-metadata-paths-are-not-readable: %w\n%s", err, imageErr)
	}
	if imageOut != "blocked\nblocked\nblocked\nblocked\nblocked\nblocked\nblocked\nblocked\n" {
		return fmt.Errorf("image-metadata-paths-are-not-readable: unexpected stdout %q stderr %q", imageOut, imageErr)
	}

	toolOut, toolErr, err := runAsSandboxUser(
		"for p in /usr/bin/apt /usr/bin/apt-get /usr/bin/apt-cache /usr/bin/apt-config /usr/bin/dpkg /usr/bin/dpkg-query /usr/bin/dpkg-deb /usr/bin/curl /usr/bin/wget; do "+
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
		if toolFields[i+1] != "blocked" {
			return fmt.Errorf("image-package-tools-are-not-executable: unexpected stdout %q stderr %q", toolOut, toolErr)
		}
	}

	scratchOut, scratchErr, err := runAsSandboxUser(
		"for p in /tmp /var/tmp /run/lock; do "+
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

func runAsSandboxUser(script, dir string) (string, string, error) {
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
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: 65532, Gid: 65532},
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
