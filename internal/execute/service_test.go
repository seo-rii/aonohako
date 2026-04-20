package execute

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/model"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestMaterializeRejectsPathEscape(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	req := &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "../escape",
			DataB64: b64("#!/bin/sh\necho nope"),
			Mode:    "exec",
		}},
	}
	_, _, err = materializeFiles(ws, req)
	if err == nil {
		t.Fatalf("expected path validation error")
	}
}

func TestStreamImageEvents(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	imgDir := filepath.Join(workDir, "__img__")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	imgPath := filepath.Join(imgDir, "images.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	events := 0
	go streamImageEvents(ctx, ws, "__img__/images.jsonl", func(mime, b64 string, ts int64) {
		mu.Lock()
		events++
		mu.Unlock()
	})

	line := "{\"mime\":\"image/png\",\"b64\":\"abc\",\"ts\":123}\n"
	if err := os.WriteFile(imgPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write image file: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := events
		mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected at least one image event")
}

func TestStreamImageEventsSkipsInvalidLines(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	imgDir := filepath.Join(workDir, "__img__")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	imgPath := filepath.Join(imgDir, "images.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var events []string
	go streamImageEvents(ctx, ws, "__img__/images.jsonl", func(mime, b64 string, ts int64) {
		mu.Lock()
		events = append(events, mime+":"+b64)
		mu.Unlock()
	})

	lines := strings.Join([]string{
		"{\"mime\":\"image/png\",\"b64\":\"ok1\",\"ts\":1}",
		"not-json",
		"{\"mime\":\"\",\"b64\":\"missing\"}",
		"{\"mime\":\"image/jpeg\",\"b64\":\"ok2\",\"ts\":2}",
		"",
	}, "\n")
	if err := os.WriteFile(imgPath, []byte(lines), 0o644); err != nil {
		t.Fatalf("write image file: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(events)
		mu.Unlock()
		if count == 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected exactly two valid events, got %v", events)
}

func TestStreamImageEventsRejectsSymlinkEscape(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "secret.jsonl")
	line := "{\"mime\":\"image/png\",\"b64\":\"escaped\",\"ts\":123}\n"
	if err := os.WriteFile(outside, []byte(line), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	imgPath := filepath.Join(ws.BoxDir, "__img__", "images.jsonl")
	if err := os.MkdirAll(filepath.Dir(imgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, imgPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var events []string
	go streamImageEvents(ctx, ws, "__img__/images.jsonl", func(mime, b64 string, ts int64) {
		mu.Lock()
		events = append(events, mime+":"+b64)
		mu.Unlock()
	})

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Fatalf("expected no events from symlink escape, got %v", events)
	}
}

func TestRunReturnsTLEOnParentCancel(t *testing.T) {
	forceDirectMode(t)
	svc := New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	req := &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: b64("import time\ntime.sleep(5)\n"),
		}},
		Limits: model.Limits{TimeMs: 10000, MemoryMB: 128},
	}

	resp := svc.Run(ctx, req, Hooks{})
	if resp.Status != model.RunStatusTLE {
		t.Fatalf("expected TLE after cancel, got %+v", resp)
	}
}

func TestRunCapturesSidecarOutput(t *testing.T) {
	forceDirectMode(t)
	svc := New()
	req := &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\necho sidecar > result.txt\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
		SidecarOutputs: []model.OutputFile{{Path: "result.txt"}},
	}

	resp := svc.Run(context.Background(), req, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected accepted run, got %+v", resp)
	}
	if len(resp.SidecarOutputs) != 1 {
		t.Fatalf("expected one sidecar output, got %d", len(resp.SidecarOutputs))
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.SidecarOutputs[0].DataB64)
	if err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if strings.TrimSpace(string(decoded)) != "sidecar" {
		t.Fatalf("unexpected sidecar content: %q", string(decoded))
	}
}

func TestRunUsesRequestedFileOutputForJudging(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\nprintf ignored\\n\nprintf wanted\\n > output.txt\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "wanted\n",
		FileOutputs:    []model.OutputFile{{Path: "output.txt"}},
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected file output to be judged, got %+v", resp)
	}
}

func TestRunMissingRequestedFileOutputFailsExplicitly(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\nprintf fallback\\n\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "fallback\n",
		FileOutputs:    []model.OutputFile{{Path: "output.txt"}},
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusRE {
		t.Fatalf("expected runtime error on missing file output, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "file output capture failed") {
		t.Fatalf("expected explicit file output reason, got %+v", resp)
	}
}

func TestRunRejectsMultipleFileOutputs(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\nprintf ok\\n > output.txt\n"),
			Mode:    "exec",
		}},
		FileOutputs: []model.OutputFile{{Path: "output.txt"}, {Path: "extra.txt"}},
		Limits:      model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusInitFail {
		t.Fatalf("expected init failure for multiple file outputs, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "at most one file output") {
		t.Fatalf("expected multiple file outputs reason, got %+v", resp)
	}
}

func b64Raw(v []byte) string {
	return base64.StdEncoding.EncodeToString(v)
}

func TestRunSignalTerminationIsRuntimeError(t *testing.T) {
	forceDirectMode(t)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nkill -SEGV $$\n")),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})

	if resp.Status != model.RunStatusRE {
		t.Fatalf("status=%q want=%q (resp=%+v)", resp.Status, model.RunStatusRE, resp)
	}
}

func TestRunDefaultOutputLimitExceedsLegacyCap(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	output := strings.Repeat("a", 4096)
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\ni=0\nwhile [ \"$i\" -lt 4096 ]; do\n  printf a\n  i=$((i+1))\ndone\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusWA {
		t.Fatalf("expected WA, got %+v", resp)
	}
	if resp.Stdout != output {
		t.Fatalf("unexpected stdout length=%d", len(resp.Stdout))
	}
}

func TestRunOutputLimitUsesConfiguredUnifiedCap(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	var stdoutLog string
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\ni=0\nwhile [ \"$i\" -lt 9000 ]; do\n  printf a\n  i=$((i+1))\ndone\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 8192},
	}, Hooks{
		OnLog: func(stream, msg string) {
			if stream == "stdout" {
				stdoutLog = msg
			}
		},
	})

	want := strings.Repeat("a", 8192)
	if resp.Status != model.RunStatusWA {
		t.Fatalf("expected WA, got %+v", resp)
	}
	if resp.Stdout != want {
		t.Fatalf("unexpected stdout length=%d", len(resp.Stdout))
	}
	if stdoutLog != want {
		t.Fatalf("unexpected stdout log length=%d", len(stdoutLog))
	}
}

func TestRunRequestOutputLimitOverridesLegacyEnv(t *testing.T) {
	forceDirectMode(t)
	t.Setenv("AONOHAKO_MAX_OUTPUT_BYTES", "2048")
	t.Setenv("GO_MAX_OUTPUT_BYTES", "1024")

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\ni=0\nwhile [ \"$i\" -lt 9000 ]; do\n  printf a\n  i=$((i+1))\ndone\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 8192},
	}, Hooks{})

	want := strings.Repeat("a", 8192)
	if resp.Status != model.RunStatusWA {
		t.Fatalf("expected WA, got %+v", resp)
	}
	if resp.Stdout != want {
		t.Fatalf("unexpected stdout length=%d", len(resp.Stdout))
	}
}

func TestRunBlocksNetworkWhenDisabled(t *testing.T) {
	requireSandboxSupport(t)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"import socket\ntry:\n    s = socket.socket()\n    s.settimeout(0.5)\n    s.connect(('1.1.1.1', 53))\n    print('connected')\nexcept OSError:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksUnixSocketConnectWhenNetworkDisabled(t *testing.T) {
	requireSandboxSupport(t)

	socketPath := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()

	svc := New()
	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\n    s.settimeout(0.5)\n    s.connect(%q)\n    print('connected')\nexcept OSError:\n    print('blocked')\n",
		socketPath,
	)
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksUnixDatagramSendWhenNetworkDisabled(t *testing.T) {
	requireSandboxSupport(t)

	socketPath := filepath.Join(t.TempDir(), "control-dgram.sock")
	addr := &net.UnixAddr{Name: socketPath, Net: "unixgram"}
	listener, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatalf("listen unixgram socket: %v", err)
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0o777); err != nil {
		t.Fatalf("chmod unixgram socket: %v", err)
	}

	svc := New()
	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)\n    s.sendto(b'escape', %q)\n    print('sent')\nexcept OSError:\n    print('blocked')\n",
		socketPath,
	)
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}

	_ = listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 64)
	if n, _, err := listener.ReadFromUnix(buf); err == nil {
		t.Fatalf("expected no datagram delivery, got %q", string(buf[:n]))
	}
}

func TestRunBlocksUnixDatagramSendToAccessibleSocketWhenNetworkDisabled(t *testing.T) {
	requireSandboxSupport(t)

	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-open-dgram-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(socketPath)
	addr := &net.UnixAddr{Name: socketPath, Net: "unixgram"}
	listener, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatalf("listen unixgram socket: %v", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o777); err != nil {
		t.Fatalf("chmod unixgram socket: %v", err)
	}

	svc := New()
	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)\n    s.sendto(b'escape', %q)\n    print('sent')\nexcept OSError:\n    print('blocked')\n",
		socketPath,
	)
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}

	_ = listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 64)
	if n, _, err := listener.ReadFromUnix(buf); err == nil {
		t.Fatalf("expected no datagram delivery to accessible socket, got %q", string(buf[:n]))
	}
}

func TestRunBlocksSocketPairCreationWhenNetworkDisabled(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"import socket\ntry:\n    socket.socketpair()\n    print('created')\nexcept OSError:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksNamespaceEscapeAttempts(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"import ctypes\nlibc = ctypes.CDLL(None, use_errno=True)\ntry:\n    rc = libc.unshare(0x00020000)\n    if rc == 0:\n        print('escaped')\n    else:\n        print('blocked')\nexcept Exception:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksNetworkOnCloudRunWithoutDirectModeFallback(t *testing.T) {
	t.Setenv("K_SERVICE", "aonohako-runner")

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"import socket\ntry:\n    s = socket.socket()\n    s.settimeout(0.5)\n    s.connect(('1.1.1.1', 53))\n    print('connected')\nexcept OSError:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted on Cloud Run path, got %+v", resp)
	}
}

func TestRunRequiresRootOutsideCloudRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires non-root host mode")
	}
	t.Setenv("K_SERVICE", "")
	t.Setenv("CLOUD_RUN_JOB", "")
	t.Setenv("CLOUD_RUN_WORKER_POOL", "")

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\necho ok\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "ok\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})

	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "sandbox requires root") {
		t.Fatalf("expected root requirement failure, got %+v", resp)
	}
}

func TestRunPreventsRemovingOrReplacingSubmittedFiles(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{
			{
				Name: "main.py",
				DataB64: base64.StdEncoding.EncodeToString([]byte(
					"from pathlib import Path\nimport os\ntry:\n    os.unlink('data.txt')\n    print('unlinked')\nexcept OSError:\n    print('blocked-unlink')\nPath('swap.txt').write_text('mutated\\n')\ntry:\n    os.replace('swap.txt', 'data.txt')\n    print('replaced')\nexcept OSError:\n    print('blocked-replace')\nprint(Path('data.txt').read_text(), end='')\n",
				)),
			},
			{
				Name:    "data.txt",
				DataB64: base64.StdEncoding.EncodeToString([]byte("original\n")),
			},
		},
		ExpectedStdout: "blocked-unlink\nblocked-replace\noriginal\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksWritesOutsideWorkspaceTempDirs(t *testing.T) {
	requireSandboxSupport(t)
	for _, dir := range []string{"/tmp", "/var/tmp"} {
		info, err := os.Stat(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			t.Fatalf("stat %s: %v", dir, err)
		}
		if info.Mode().Perm()&0o022 != 0 {
			t.Skip("global scratch hardening is validated by runtime image selftests")
		}
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"from pathlib import Path\nfor target in ['/tmp/aonohako-outside.txt', '/var/tmp/aonohako-outside.txt']:\n    try:\n        Path(target).write_text('escape')\n        print('wrote')\n    except OSError:\n        print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\nblocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256, WorkspaceBytes: 8 << 10},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunCannotSignalSiblingProcess(t *testing.T) {
	requireSandboxSupport(t)

	target := exec.Command("sleep", "10")
	if err := target.Start(); err != nil {
		t.Fatalf("start target process: %v", err)
	}
	defer func() {
		_ = target.Process.Kill()
		_, _ = target.Process.Wait()
	}()

	svc := New()
	script := fmt.Sprintf(
		"import os, signal\ntry:\n    os.kill(%d, signal.SIGTERM)\n    print('signaled')\nexcept OSError:\n    print('blocked')\n",
		target.Process.Pid,
	)
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
	if err := target.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("sandbox should not signal sibling process: %v", err)
	}
}

func TestRunCannotReadHostPathOutsideSandbox(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned filesystem isolation is verified in container smoke tests")
	}
	requireSandboxSupport(t)
	secretDir := t.TempDir()
	if err := os.Chmod(secretDir, 0o700); err != nil {
		t.Fatalf("chmod secret dir: %v", err)
	}
	secretPath := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top-secret"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	script := fmt.Sprintf("from pathlib import Path\ntry:\n    Path(%q).read_text()\n    print('leaked')\nexcept Exception:\n    print('blocked')\n", secretPath)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunExposesOnlySafeDevices(t *testing.T) {
	requireSandboxSupport(t)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"try:\n    open('/dev/kmsg', 'rb')\n    print('leaked')\nexcept Exception:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksForkAttempts(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"import subprocess\ntry:\n    subprocess.run(['sh', '-c', 'exit 0'], check=True)\n    print('forked')\nexcept Exception:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksProcFDBrowsingOutsideSandbox(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"import os\ntry:\n    os.readlink('/proc/1/fd/1')\n    print('leaked')\nexcept Exception:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunPreventsOverwritingSubmittedFilesButAllowsNewFiles(t *testing.T) {
	forceDirectMode(t)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"from pathlib import Path\ntry:\n    Path('main.py').write_text('mutated')\n    print('overwrote')\nexcept OSError:\n    print('blocked')\nPath('note.txt').write_text('new')\nprint(Path('note.txt').read_text())\n",
			)),
		}},
		ExpectedStdout: "blocked\nnew\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunShellScriptsRemainStableAtLowMemoryLimit(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	req := &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nprintf 'one\\ntwo\\nthree\\n'\n")),
			Mode:    "exec",
		}},
		ExpectedStdout: "one\ntwo\nthree\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
	}

	for i := 0; i < 100; i++ {
		resp := svc.Run(context.Background(), req, Hooks{})
		if resp.Status != model.RunStatusAccepted {
			t.Fatalf("iteration %d: expected Accepted, got %+v", i, resp)
		}
	}
}

func TestRunDirectModeDoesNotRequireUnshareBinary(t *testing.T) {
	requireSandboxSupport(t)
	t.Setenv("PATH", t.TempDir())

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho ok\n")),
			Mode:    "exec",
		}},
		ExpectedStdout: "ok\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted without unshare in direct mode, got %+v", resp)
	}
}

func TestRunBlocksThreadStorms(t *testing.T) {
	requireSandboxSupport(t)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"import threading\nimport time\nthreading.stack_size(65536)\nthreads=[]\ntry:\n    for _ in range(2000):\n        t = threading.Thread(target=time.sleep, args=(0.2,))\n        t.start()\n        threads.append(t)\n    print('spawned')\nexcept Exception:\n    print('blocked')\nfinally:\n    for t in threads:\n        t.join()\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 4000, MemoryMB: 512},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunEnforcesProcessCPUTimeAcrossThreads(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("needs at least 2 CPUs to distinguish total cpu time from wall time")
	}
	forceDirectMode(t)

	code := `
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <time.h>

static atomic_int stop_flag = 0;

static uint64_t mono_ns(void) {
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

static void* spin(void* arg) {
	volatile uint64_t x = (uintptr_t)arg + 1;
	while (!atomic_load(&stop_flag)) {
		x = x * 2862933555777941757ull + 3037000493ull;
	}
	return (void*)(uintptr_t)x;
}

int main(void) {
	pthread_t threads[4];
	for (int i = 0; i < 4; ++i) {
		if (pthread_create(&threads[i], NULL, spin, (void*)(uintptr_t)i) != 0) {
			puts("thread-error");
			return 1;
		}
	}
	uint64_t start = mono_ns();
	while (mono_ns() - start < 60000000ull) {
	}
	atomic_store(&stop_flag, 1);
	for (int i = 0; i < 4; ++i) {
		pthread_join(threads[i], NULL);
	}
	puts("finished");
	return 0;
}
`

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "runner",
			DataB64: buildCTestBinary(t, code, "-pthread"),
			Mode:    "exec",
		}},
		ExpectedStdout: "finished\n",
		Limits:         model.Limits{TimeMs: 100, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusTLE {
		t.Fatalf("expected TLE from summed process cpu time, got %+v", resp)
	}
}

func TestMaterializeRejectsOversizedBinary(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	req := &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "big.bin",
			DataB64: b64Raw(bytes.Repeat([]byte("x"), 17<<20)),
			Mode:    "exec",
		}},
	}
	if _, _, err := materializeFiles(ws, req); err == nil {
		t.Fatalf("expected oversized binary error")
	}
}

func TestCaptureSidecarOutputsSkipsOversizedFile(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	largePath := filepath.Join(ws.BoxDir, "large.txt")
	if err := os.WriteFile(largePath, bytes.Repeat([]byte("z"), 9<<20), 0o644); err != nil {
		t.Fatalf("write large sidecar: %v", err)
	}

	outputs := captureSidecarOutputs(ws, []model.OutputFile{{Path: "large.txt"}})
	if len(outputs) != 0 {
		t.Fatalf("expected oversized sidecar to be ignored, got %d outputs", len(outputs))
	}
}

func TestRunSleepMostlyConsumesWallTimeNotCPUTime(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte("import time\ntime.sleep(0.2)\n")),
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
	if resp.WallTimeMs < 150 {
		t.Fatalf("expected wall time to include sleep, got %+v", resp)
	}
	if resp.CPUTimeMs > 50 {
		t.Fatalf("expected cpu time to stay low for sleep, got %+v", resp)
	}
	if resp.TimeMs != resp.WallTimeMs {
		t.Fatalf("time_ms should match wall_time_ms, got %+v", resp)
	}
}

func TestRunReportsMemoryUsageForPythonAllocation(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"buf = bytearray(32 * 1024 * 1024)\nprint(len(buf))\n",
			)),
		}},
		ExpectedStdout: "33554432\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
	if resp.MemoryKB < 20*1024 {
		t.Fatalf("expected noticeable rss after allocation, got %+v", resp)
	}
}

func TestRunMarksMemoryLimitExceededEvenIfProgramHandlesAllocationFailure(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"try:\n    buf = bytearray(96 * 1024 * 1024)\n    print('allocated')\nexcept MemoryError:\n    print('memoryerror')\n",
			)),
		}},
		ExpectedStdout: "memoryerror\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 32},
	}, Hooks{})

	if resp.Status != model.RunStatusMLE {
		t.Fatalf("expected MLE, got %+v", resp)
	}
	if resp.MemoryKB <= 32*1024 {
		t.Fatalf("expected rss to exceed configured limit, got %+v", resp)
	}
}

func TestRunMarksMemoryLimitExceededOnAddressSpaceFailureWithoutRSSSpike(t *testing.T) {
	forceDirectMode(t)

	code := `
#include <stdio.h>
#include <sys/mman.h>
#include <unistd.h>

int main(void) {
	for (int i = 0; i < 64; ++i) {
		void* p = mmap(NULL, 8 * 1024 * 1024, PROT_NONE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
		if (p == MAP_FAILED) {
			usleep(50000);
			puts("enomem");
			return 0;
		}
	}
	usleep(50000);
	puts("mapped-all");
	return 0;
}
`

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "runner",
			DataB64: buildCTestBinary(t, code),
			Mode:    "exec",
		}},
		ExpectedStdout: "enomem\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 32},
	}, Hooks{})

	if resp.Status != model.RunStatusMLE {
		t.Fatalf("expected MLE from address-space exhaustion, got %+v", resp)
	}
}

func TestRunMarksWorkspaceQuotaExceeded(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\ni=0\nwhile [ \"$i\" -lt 8 ]; do\n  j=0\n  : > \"chunk-$i.bin\"\n  while [ \"$j\" -lt 4096 ]; do\n    printf x >> \"chunk-$i.bin\"\n    j=$((j+1))\n  done\n  i=$((i+1))\ndone\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128, WorkspaceBytes: 16 << 10},
	}, Hooks{})

	if resp.Status != model.RunStatusWLE {
		t.Fatalf("expected workspace limit status from workspace quota exhaustion, got %+v", resp)
	}
}
