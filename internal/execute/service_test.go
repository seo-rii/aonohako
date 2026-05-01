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

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/workspacequota"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestBuildEmbeddedRunnerPassesCgroupParent(t *testing.T) {
	runner, err := Build(config.Config{
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetSelfHosted,
				ExecutionTransport: platform.ExecutionTransportEmbedded,
				SandboxBackend:     platform.SandboxBackendHelper,
			},
			Cgroup: config.CgroupConfig{ParentDir: "/sys/fs/cgroup/aonohako"},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	service, ok := runner.(*Service)
	if !ok {
		t.Fatalf("Build() returned %T, want *Service", runner)
	}
	if service.cgroupParentDir != "/sys/fs/cgroup/aonohako" {
		t.Fatalf("cgroupParentDir = %q", service.cgroupParentDir)
	}
}

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

func TestStreamImageEventsSkipsOversizedPayloads(t *testing.T) {
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

	line := fmt.Sprintf("{\"mime\":\"image/png\",\"b64\":%q,\"ts\":123}\n", strings.Repeat("x", maxImageEventBytes+1))
	if err := os.WriteFile(imgPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write image file: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if events != 0 {
		t.Fatalf("expected oversized image payload to be skipped, got %d events", events)
	}
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

func TestRunRejectsNetworkEnabledRequestsOnCloudRun(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "cloudrun")
	workRoot := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-cloudrun-network-test-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		t.Fatalf("mkdir work root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workRoot) })
	t.Setenv("AONOHAKO_WORK_ROOT", workRoot)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\necho ok\n"),
			Mode:    "exec",
		}},
		Limits:        model.Limits{TimeMs: 1000, MemoryMB: 128},
		EnableNetwork: true,
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail {
		t.Fatalf("expected network-enabled run to be rejected, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "enable_network=true") {
		t.Fatalf("expected rejection reason to mention enable_network, got %+v", resp)
	}
}

func TestRunAllowsOutboundNetworkWhenEnabledOutsideCloudRun(t *testing.T) {
	requireSandboxSupport(t)
	t.Setenv("AONOHAKO_EXECUTION_MODE", "local-root")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		accepted <- struct{}{}
	}()

	address := listener.Addr().String()
	script := fmt.Sprintf(
		"import socket\nhost, port = %q.split(':')\ns = socket.create_connection((host, int(port)), timeout=1)\nprint('connected')\ns.close()\n",
		address,
	)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "connected\n",
		EnableNetwork:  true,
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected sandboxed process to connect to local tcp listener")
	}
}

func TestRunBlocksUnixSocketConnectWhenNetworkEnabled(t *testing.T) {
	requireSandboxSupport(t)
	t.Setenv("AONOHAKO_EXECUTION_MODE", "local-root")

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
		EnableNetwork:  true,
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunRejectsTooManyBinaries(t *testing.T) {
	svc := New()
	binaries := make([]model.Binary, 0, maxBinaryFiles+1)
	for i := 0; i < maxBinaryFiles+1; i++ {
		binaries = append(binaries, model.Binary{
			Name:    fmt.Sprintf("Main%d.txt", i),
			DataB64: b64("text"),
		})
	}

	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang:     "text",
		Binaries: binaries,
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusInitFail {
		t.Fatalf("expected init failure for too many binaries, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "too many binaries") {
		t.Fatalf("expected too many binaries reason, got %+v", resp)
	}
}

func TestRunRejectsTooManySidecarOutputs(t *testing.T) {
	svc := New()
	sidecarOutputs := make([]model.OutputFile, 0, maxSidecarOutputSpecs+1)
	for i := 0; i < maxSidecarOutputSpecs+1; i++ {
		sidecarOutputs = append(sidecarOutputs, model.OutputFile{Path: fmt.Sprintf("artifact-%d.txt", i)})
	}

	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "text",
		Binaries: []model.Binary{{
			Name:    "Main.txt",
			DataB64: b64("ok"),
		}},
		SidecarOutputs: sidecarOutputs,
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusInitFail {
		t.Fatalf("expected init failure for too many sidecar outputs, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "too many sidecar outputs") {
		t.Fatalf("expected too many sidecar outputs reason, got %+v", resp)
	}
}

func TestCappedBufferTracksTruncation(t *testing.T) {
	buf := cappedBuffer{limit: 4}
	if n, err := buf.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("Write returned n=%d err=%v", n, err)
	}
	if string(buf.Bytes()) != "abcd" {
		t.Fatalf("buffer content = %q", string(buf.Bytes()))
	}
	if !buf.Truncated() {
		t.Fatalf("expected truncated flag")
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
		SidecarOutputs: []model.OutputFile{{Path: "result.txt"}, {Path: "missing.txt"}},
	}

	resp := svc.Run(context.Background(), req, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected accepted run, got %+v", resp)
	}
	if len(resp.SidecarOutputs) != 1 {
		t.Fatalf("expected one sidecar output, got %d", len(resp.SidecarOutputs))
	}
	if len(resp.SidecarErrors) != 1 || resp.SidecarErrors[0].Path != "missing.txt" {
		t.Fatalf("expected one missing sidecar diagnostic, got %+v", resp.SidecarErrors)
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.SidecarOutputs[0].DataB64)
	if err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if strings.TrimSpace(string(decoded)) != "sidecar" {
		t.Fatalf("unexpected sidecar content: %q", string(decoded))
	}
}

func TestRunCapturesNonImageSidecarWithoutWaitingForTimeout(t *testing.T) {
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
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 128},
		SidecarOutputs: []model.OutputFile{{Path: "result.txt"}},
	}

	var imageEvents int
	start := time.Now()
	resp := svc.Run(context.Background(), req, Hooks{
		OnImage: func(mime, b64 string, ts int64) {
			imageEvents++
		},
	})
	elapsed := time.Since(start)

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected accepted run, got %+v", resp)
	}
	if imageEvents != 0 {
		t.Fatalf("expected non-image sidecar not to emit image events, got %d", imageEvents)
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("sidecar run took %s; expected it not to wait for wall timeout", elapsed)
	}
}

func TestRunFlushesImageSidecarOnFastExit(t *testing.T) {
	forceDirectMode(t)
	svc := New()
	req := &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\nmkdir -p __img__\nprintf '%s\\n' '{\"mime\":\"image/png\",\"b64\":\"abc\",\"ts\":123}' > __img__/images.jsonl\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 128},
		SidecarOutputs: []model.OutputFile{{Path: "__img__/images.jsonl"}},
	}

	var mu sync.Mutex
	var images []string
	resp := svc.Run(context.Background(), req, Hooks{
		OnImage: func(mime, b64 string, ts int64) {
			mu.Lock()
			images = append(images, mime+":"+b64)
			mu.Unlock()
		},
	})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected accepted run, got %+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(images) != 1 || images[0] != "image/png:abc" {
		t.Fatalf("expected one flushed image event, got %v", images)
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
	if resp.VerdictSource != "file_output" {
		t.Fatalf("verdict_source = %q, want file_output", resp.VerdictSource)
	}
}

func TestRunPythonEntrypointReadsAuxiliaryCSVFile(t *testing.T) {
	requireSandboxSupport(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang:       "python",
		EntryPoint: "src/main.py",
		Binaries: []model.Binary{
			{Name: "tools/ignored.py", DataB64: b64("print('wrong')\n")},
			{Name: "data/input.csv", DataB64: b64("2,3\n")},
			{Name: "src/main.py", DataB64: b64("from pathlib import Path\nprint(sum(map(int, Path('data/input.csv').read_text().strip().split(','))))\n")},
		},
		ExpectedStdout: "5\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
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
	if resp.VerdictSource != "file_output" {
		t.Fatalf("verdict_source = %q, want file_output", resp.VerdictSource)
	}
}

func TestEvaluateRunStatusReportsVerdictSource(t *testing.T) {
	exitCode := 1
	tests := []struct {
		name        string
		req         model.RunRequest
		res         execResult
		judgeOut    []byte
		judgeSource string
		wantStatus  string
		wantSource  string
	}{
		{
			name:        "stdout accepted",
			req:         model.RunRequest{ExpectedStdout: "ok\n", Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}},
			res:         execResult{Status: "OK"},
			judgeOut:    []byte("ok\n"),
			judgeSource: "stdout",
			wantStatus:  model.RunStatusAccepted,
			wantSource:  "stdout",
		},
		{
			name:        "file output wrong answer",
			req:         model.RunRequest{ExpectedStdout: "ok\n", Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}},
			res:         execResult{Status: "OK"},
			judgeOut:    []byte("bad\n"),
			judgeSource: "file_output",
			wantStatus:  model.RunStatusWA,
			wantSource:  "file_output",
		},
		{
			name:       "reported memory exceeds limit",
			req:        model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}},
			res:        execResult{Status: "OK", MemoryKB: 65 * 1024},
			wantStatus: model.RunStatusMLE,
			wantSource: "memory_reported",
		},
		{
			name:       "exit code runtime error",
			req:        model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}},
			res:        execResult{Status: "OK", ExitCode: &exitCode},
			wantStatus: model.RunStatusRE,
			wantSource: "exit_code",
		},
		{
			name:       "resource verdict preserves source",
			req:        model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}},
			res:        execResult{Status: model.RunStatusTLE, VerdictSource: "cpu_time", Reason: "cpu time limit exceeded"},
			wantStatus: model.RunStatusTLE,
			wantSource: "cpu_time",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, _, _, source := evaluateRunStatus(context.Background(), Workspace{}, &tc.req, tc.res, tc.judgeOut, tc.judgeSource, config.DefaultRuntimeTuningConfig(), "")
			if status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
			if source != tc.wantSource {
				t.Fatalf("source = %q, want %q", source, tc.wantSource)
			}
		})
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

func TestRunBlocksProcessGroupEscapeAttempts(t *testing.T) {
	requireSandboxSupport(t)

	code := `
#include <errno.h>
#include <stdio.h>
#include <unistd.h>

int main(void) {
	if (setpgid(0, 0) == 0) {
		puts("escaped");
		return 0;
	}
	puts(errno == EPERM ? "blocked" : "error");
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
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksMemoryLockAndSysVSharedMemory(t *testing.T) {
	requireSandboxSupport(t)

	code := `
#include <errno.h>
#include <stdio.h>
#include <sys/ipc.h>
#include <sys/mman.h>
#include <sys/shm.h>
#include <sys/syscall.h>
#include <unistd.h>

int main(void) {
	errno = 0;
	long lock_rc = syscall(SYS_mlockall, MCL_CURRENT);
	int lock_blocked = lock_rc == -1 && errno == EPERM;
	errno = 0;
	long shm_rc = syscall(SYS_shmget, IPC_PRIVATE, 4096, IPC_CREAT | 0600);
	int shm_blocked = shm_rc == -1 && errno == EPERM;
	if (lock_blocked && shm_blocked) {
		puts("blocked");
		return 0;
	}
	printf("unexpected:%ld:%ld:%d\n", lock_rc, shm_rc, errno);
	return 1;
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
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunAllowsPrlimitQueriesNeededByManagedRuntimes(t *testing.T) {
	requireSandboxSupport(t)

	binPath := buildCTestBinary(t, `#define _GNU_SOURCE
#include <stdio.h>
#include <sys/resource.h>

int main(void) {
	struct rlimit lim;
	if (prlimit(0, RLIMIT_STACK, NULL, &lim) != 0) {
		perror("prlimit");
		return 1;
	}
	puts("ok");
	return 0;
}
`)
	payload, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", binPath, err)
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "probe",
			DataB64: base64.StdEncoding.EncodeToString(payload),
			Mode:    "exec",
		}},
		ExpectedStdout: "ok\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestExecuteSandboxAllowsLocalUnixSocketPairsForManagedRuntimes(t *testing.T) {
	requireSandboxSupport(t)

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	for _, lang := range []string{"erlang", "wasm"} {
		t.Run(lang, func(t *testing.T) {
			result := executeSandboxCommand(
				context.Background(),
				ws,
				[]string{
					python,
					"-c",
					"import socket, sys\na, b = socket.socketpair()\na.sendmsg([b'ok'])\ndata, _, _, _ = b.recvmsg(2)\nsys.exit(0 if data == b'ok' else 1)\n",
				},
				&model.RunRequest{
					Lang:   lang,
					Limits: model.Limits{TimeMs: 2000, MemoryMB: 256},
				},
				Hooks{},
				1024,
				config.DefaultRuntimeTuningConfig(),
				"",
			)
			if result.Status != model.RunStatusAccepted {
				t.Fatalf("expected Accepted, got %+v", result)
			}
		})
	}
}

func TestExecuteSandboxBlocksUnixSocketConnectForManagedRuntimeSocketAllowance(t *testing.T) {
	requireSandboxSupport(t)

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	socketPath := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()

	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\n    s.settimeout(0.5)\n    s.connect(%q)\n    print('connected')\nexcept OSError:\n    print('blocked')\n",
		socketPath,
	)

	result := executeSandboxCommand(
		context.Background(),
		ws,
		[]string{python, "-c", script},
		&model.RunRequest{
			Lang:           "wasm",
			ExpectedStdout: "blocked\n",
			Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
		},
		Hooks{},
		1024,
		config.DefaultRuntimeTuningConfig(),
		"",
	)
	if result.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", result)
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
	t.Setenv("AONOHAKO_EXECUTION_MODE", "cloudrun")
	workRoot := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-cloudrun-test-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		t.Fatalf("mkdir work root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workRoot) })
	t.Setenv("AONOHAKO_WORK_ROOT", workRoot)
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
		t.Fatalf("expected Accepted on Cloud Run path, got %+v", resp)
	}
}

func TestRunRequiresRootOutsideCloudRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires non-root host mode")
	}
	t.Setenv("AONOHAKO_EXECUTION_MODE", "local-dev")

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

func TestRunSandboxEnvironmentDoesNotInheritParentSecrets(t *testing.T) {
	requireSandboxSupport(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	for _, key := range []string{
		"AONOHAKO_API_BEARER_TOKEN",
		"AONOHAKO_REMOTE_RUNNER_TOKEN",
		"AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET",
		"DATABASE_URL",
		"CUSTOM_SECRET",
	} {
		t.Setenv(key, "should-not-enter-sandbox")
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: b64(
				"import os\n" +
					"keys = ['AONOHAKO_API_BEARER_TOKEN', 'AONOHAKO_REMOTE_RUNNER_TOKEN', 'AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET', 'DATABASE_URL', 'CUSTOM_SECRET']\n" +
					"leaked = [key for key in keys if os.environ.get(key)]\n" +
					"print('leak:' + ','.join(leaked) if leaked else 'clean')\n",
			),
		}},
		ExpectedStdout: "clean\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted without inherited secrets, got %+v", resp)
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

func TestRunBlocksFilesystemMetadataSyscalls(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: b64(
				"import errno, os, sys\n" +
					"open('owned.txt', 'w').close()\n" +
					"checks = [\n" +
					"    ('chmod', lambda: os.chmod('owned.txt', 0o777)),\n" +
					"    ('chown', lambda: os.chown('owned.txt', os.getuid(), os.getgid())),\n" +
					"    ('mknod', lambda: os.mknod('node')),\n" +
					"]\n" +
					"for name, action in checks:\n" +
					"    try:\n" +
					"        action()\n" +
					"        print(name + ':escaped')\n" +
					"        sys.exit(1)\n" +
					"    except OSError as exc:\n" +
					"        if exc.errno not in (errno.EPERM, errno.EACCES, errno.ENOSYS):\n" +
					"            print(name + ':error:' + str(exc.errno))\n" +
					"            sys.exit(1)\n" +
					"print('blocked')\n",
			),
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

func TestRunBlocksKernelAttackSurfaceSyscalls(t *testing.T) {
	forceDirectMode(t)

	code := `
#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

static int check(const char *name, long nr) {
	errno = 0;
	long rc = syscall(nr, 0, 0, 0, 0, 0, 0);
	if (rc == -1 && (errno == EPERM || errno == EACCES || errno == ENOSYS)) {
		return 0;
	}
	printf("%s:%ld:%s\n", name, rc, strerror(errno));
	return 1;
}

static int check_personality(void) {
	errno = 0;
	long query_rc = syscall(SYS_personality, 0xffffffffUL, 0, 0, 0, 0, 0);
	if (query_rc == -1) {
		printf("personality_query:%s\n", strerror(errno));
		return 1;
	}
	errno = 0;
	long set_rc = syscall(SYS_personality, 0, 0, 0, 0, 0, 0);
	if (set_rc == -1 && errno == EPERM) {
		return 0;
	}
	printf("personality_set:%ld:%s\n", set_rc, strerror(errno));
	return 1;
}

int main(void) {
	int failed = 0;
#ifdef SYS_bpf
	failed |= check("bpf", SYS_bpf);
#endif
#ifdef SYS_userfaultfd
	failed |= check("userfaultfd", SYS_userfaultfd);
#endif
#ifdef SYS_io_uring_setup
	failed |= check("io_uring_setup", SYS_io_uring_setup);
#endif
#ifdef SYS_perf_event_open
	failed |= check("perf_event_open", SYS_perf_event_open);
#endif
#ifdef SYS_cachestat
	failed |= check("cachestat", SYS_cachestat);
#endif
#ifdef SYS_open_by_handle_at
	failed |= check("open_by_handle_at", SYS_open_by_handle_at);
#endif
#ifdef SYS_name_to_handle_at
	failed |= check("name_to_handle_at", SYS_name_to_handle_at);
#endif
#ifdef SYS_fanotify_init
	failed |= check("fanotify_init", SYS_fanotify_init);
#endif
#ifdef SYS_fanotify_mark
	failed |= check("fanotify_mark", SYS_fanotify_mark);
#endif
#ifdef SYS_lookup_dcookie
	failed |= check("lookup_dcookie", SYS_lookup_dcookie);
#endif
#ifdef SYS_add_key
	failed |= check("add_key", SYS_add_key);
#endif
#ifdef SYS_request_key
	failed |= check("request_key", SYS_request_key);
#endif
#ifdef SYS_keyctl
	failed |= check("keyctl", SYS_keyctl);
#endif
#ifdef SYS_init_module
	failed |= check("init_module", SYS_init_module);
#endif
#ifdef SYS_finit_module
	failed |= check("finit_module", SYS_finit_module);
#endif
#ifdef SYS_delete_module
	failed |= check("delete_module", SYS_delete_module);
#endif
#ifdef SYS_kexec_load
	failed |= check("kexec_load", SYS_kexec_load);
#endif
#ifdef SYS_kexec_file_load
	failed |= check("kexec_file_load", SYS_kexec_file_load);
#endif
#ifdef SYS_acct
	failed |= check("acct", SYS_acct);
#endif
#ifdef SYS_nfsservctl
	failed |= check("nfsservctl", SYS_nfsservctl);
#endif
#ifdef SYS_quotactl
	failed |= check("quotactl", SYS_quotactl);
#endif
#ifdef SYS_quotactl_fd
	failed |= check("quotactl_fd", SYS_quotactl_fd);
#endif
#ifdef SYS_process_madvise
	failed |= check("process_madvise", SYS_process_madvise);
#endif
#ifdef SYS_process_mrelease
	failed |= check("process_mrelease", SYS_process_mrelease);
#endif
#ifdef SYS_get_mempolicy
	failed |= check("get_mempolicy", SYS_get_mempolicy);
#endif
#ifdef SYS_mbind
	failed |= check("mbind", SYS_mbind);
#endif
#ifdef SYS_set_mempolicy
	failed |= check("set_mempolicy", SYS_set_mempolicy);
#endif
#ifdef SYS_set_mempolicy_home_node
	failed |= check("set_mempolicy_home_node", SYS_set_mempolicy_home_node);
#endif
#ifdef SYS_migrate_pages
	failed |= check("migrate_pages", SYS_migrate_pages);
#endif
#ifdef SYS_move_pages
	failed |= check("move_pages", SYS_move_pages);
#endif
#ifdef SYS_kcmp
	failed |= check("kcmp", SYS_kcmp);
#endif
#ifdef SYS_seccomp
	failed |= check("seccomp", SYS_seccomp);
#endif
#ifdef SYS_landlock_create_ruleset
	failed |= check("landlock_create_ruleset", SYS_landlock_create_ruleset);
#endif
#ifdef SYS_landlock_add_rule
	failed |= check("landlock_add_rule", SYS_landlock_add_rule);
#endif
#ifdef SYS_landlock_restrict_self
	failed |= check("landlock_restrict_self", SYS_landlock_restrict_self);
#endif
#ifdef SYS_lsm_get_self_attr
	failed |= check("lsm_get_self_attr", SYS_lsm_get_self_attr);
#endif
#ifdef SYS_lsm_set_self_attr
	failed |= check("lsm_set_self_attr", SYS_lsm_set_self_attr);
#endif
#ifdef SYS_lsm_list_modules
	failed |= check("lsm_list_modules", SYS_lsm_list_modules);
#endif
#ifdef SYS_clock_settime
	failed |= check("clock_settime", SYS_clock_settime);
#endif
#ifdef SYS_settimeofday
	failed |= check("settimeofday", SYS_settimeofday);
#endif
#ifdef SYS_adjtimex
	failed |= check("adjtimex", SYS_adjtimex);
#endif
#ifdef SYS_syslog
	failed |= check("syslog", SYS_syslog);
#endif
#ifdef SYS_reboot
	failed |= check("reboot", SYS_reboot);
#endif
#ifdef SYS_swapon
	failed |= check("swapon", SYS_swapon);
#endif
#ifdef SYS_swapoff
	failed |= check("swapoff", SYS_swapoff);
#endif
#ifdef SYS_memfd_create
	failed |= check("memfd_create", SYS_memfd_create);
#endif
#ifdef SYS_open_tree
	failed |= check("open_tree", SYS_open_tree);
#endif
#ifdef SYS_move_mount
	failed |= check("move_mount", SYS_move_mount);
#endif
#ifdef SYS_fsopen
	failed |= check("fsopen", SYS_fsopen);
#endif
#ifdef SYS_fsconfig
	failed |= check("fsconfig", SYS_fsconfig);
#endif
#ifdef SYS_fsmount
	failed |= check("fsmount", SYS_fsmount);
#endif
#ifdef SYS_fspick
	failed |= check("fspick", SYS_fspick);
#endif
#ifdef SYS_mount_setattr
	failed |= check("mount_setattr", SYS_mount_setattr);
#endif
#ifdef SYS_statmount
	failed |= check("statmount", SYS_statmount);
#endif
#ifdef SYS_listmount
	failed |= check("listmount", SYS_listmount);
#endif
#ifdef SYS_pidfd_open
	failed |= check("pidfd_open", SYS_pidfd_open);
#endif
#ifdef SYS_pidfd_getfd
	failed |= check("pidfd_getfd", SYS_pidfd_getfd);
#endif
#ifdef SYS_pidfd_send_signal
	failed |= check("pidfd_send_signal", SYS_pidfd_send_signal);
#endif
#ifdef SYS_fchmodat2
	failed |= check("fchmodat2", SYS_fchmodat2);
#endif
#ifdef SYS_personality
	failed |= check_personality();
#endif
	if (failed != 0) {
		return 1;
	}
	puts("blocked");
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
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksExecveatAttempts(t *testing.T) {
	forceDirectMode(t)

	code := `
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

extern char **environ;

int main(void) {
	char *argv[] = {"/bin/true", NULL};
	long rc = syscall(SYS_execveat, AT_FDCWD, "/bin/true", argv, environ, 0);
	if (rc == -1 && (errno == EPERM || errno == EACCES)) {
		puts("blocked");
		return 0;
	}
	printf("unexpected:%ld:%s\n", rc, strerror(errno));
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
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
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

func TestRunBlocksProcEnvironRead(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"from pathlib import Path\ntry:\n    Path('/proc/1/environ').read_bytes()\n    print('leaked')\nexcept Exception:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksSensitiveProcSymlinksOutsideSandbox(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"from pathlib import Path\nchecks = [\n    ('root-link', lambda: Path('/proc/1/root').readlink()),\n    ('cwd-link', lambda: Path('/proc/1/cwd').readlink()),\n    ('exe-link', lambda: Path('/proc/1/exe').readlink()),\n    ('root-passwd', lambda: Path('/proc/1/root/etc/passwd').read_text()),\n]\nfor name, action in checks:\n    try:\n        action()\n        print(name + ':leaked')\n    except Exception:\n        print(name + ':blocked')\n",
			)),
		}},
		ExpectedStdout: "root-link:blocked\ncwd-link:blocked\nexe-link:blocked\nroot-passwd:blocked\n",
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

	outputs, errs := captureSidecarOutputs(ws, []model.OutputFile{{Path: "large.txt"}})
	if len(outputs) != 0 {
		t.Fatalf("expected oversized sidecar to be ignored, got %d outputs", len(outputs))
	}
	if len(errs) != 1 || errs[0].Reason != "file too large" {
		t.Fatalf("expected sidecar size diagnostic, got %+v", errs)
	}
}

func TestWriteTempFileCreatesSandboxReadableFile(t *testing.T) {
	dir := t.TempDir()
	path, err := writeTempFile(dir, "spj-input-*", "content")
	if err != nil {
		t.Fatalf("writeTempFile: %v", err)
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat temp file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Fatalf("temp file mode = %o, want 0444", got)
	}
}

func TestRunSPJUsesCleanWorkspaceAndReadableFiles(t *testing.T) {
	requireSandboxSupport(t)

	spj := `#!/usr/bin/env python3
import importlib
import os
import sys

if ".spj" not in os.getcwd().split(os.sep):
    raise SystemExit(2)
for path in sys.argv[1:4]:
    with open(path, "rb") as handle:
        handle.read()
try:
    importlib.import_module("evil")
except ModuleNotFoundError:
    pass
else:
    raise SystemExit(3)
raise SystemExit(0)
`

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte("open('evil.py', 'w').write('raise SystemExit(99)\\n')\nprint('42')\n")),
		}},
		ExpectedStdout: "42\n",
		SPJ: &model.SPJSpec{
			Binary: &model.Binary{
				Name:    "spj.py",
				DataB64: base64.StdEncoding.EncodeToString([]byte(spj)),
			},
			Lang: "python",
		},
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected SPJ to accept with clean workspace, got %+v", resp)
	}
}

func TestRunSPJUsesDedicatedLimits(t *testing.T) {
	requireSandboxSupport(t)

	spj := "#!/usr/bin/env python3\nimport time\ntime.sleep(0.5)\n"
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte("print('42')\n")),
		}},
		ExpectedStdout: "42\n",
		SPJ: &model.SPJSpec{
			Binary: &model.Binary{
				Name:    "spj.py",
				DataB64: base64.StdEncoding.EncodeToString([]byte(spj)),
			},
			Lang:   "python",
			Limits: &model.Limits{TimeMs: 50, MemoryMB: 128},
		},
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusRE {
		t.Fatalf("expected SPJ timeout to be reported as Runtime Error, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "spj failed: Time Limit Exceeded") {
		t.Fatalf("expected SPJ timeout reason, got %+v", resp)
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

func TestRunTreatsCoqAsCompileValidatedLanguage(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "coq",
		Binaries: []model.Binary{{
			Name:    "Main.v",
			DataB64: base64.StdEncoding.EncodeToString([]byte("Theorem same_folder_ok : 1 = 1.\nProof. reflexivity. Qed.\n")),
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected coq execute to be a compile-validated no-op, got %+v", resp)
	}
	if resp.ExitCode == nil || *resp.ExitCode != 0 {
		t.Fatalf("expected zero exit code, got %+v", resp)
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
	if !strings.HasPrefix(resp.VerdictSource, "memory") {
		t.Fatalf("verdict_source = %q, want memory-derived source", resp.VerdictSource)
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
	if resp.VerdictSource != "address_space" {
		t.Fatalf("verdict_source = %q, want address_space", resp.VerdictSource)
	}
}

func TestRunMarksMemoryLimitExceededForMmapRSSSpike(t *testing.T) {
	forceDirectMode(t)

	code := `
#include <stdio.h>
#include <sys/mman.h>
#include <unistd.h>

int main(void) {
	const size_t bytes = 96 * 1024 * 1024;
	char *p = mmap(NULL, bytes, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (p == MAP_FAILED) {
		perror("mmap");
		return 2;
	}
	for (size_t i = 0; i < bytes; i += 4096) {
		p[i] = 1;
	}
	usleep(200000);
	puts("survived");
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
		Limits: model.Limits{TimeMs: 2000, MemoryMB: 32},
	}, Hooks{})

	if resp.Status != model.RunStatusMLE {
		t.Fatalf("expected MLE from mmap RSS spike, got %+v", resp)
	}
	if resp.VerdictSource != "memory_rss" && resp.VerdictSource != "memory_reported" {
		t.Fatalf("verdict_source = %q, want memory_rss or memory_reported", resp.VerdictSource)
	}
	if resp.MemoryKB <= 32*1024 {
		t.Fatalf("expected sampled RSS over memory limit, got %+v", resp)
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
	if resp.VerdictSource != "workspace_bytes" {
		t.Fatalf("verdict_source = %q, want workspace_bytes", resp.VerdictSource)
	}
}

func TestRunMarksWorkspaceEntryLimitExceeded(t *testing.T) {
	forceDirectMode(t)

	script := fmt.Sprintf("from pathlib import Path\nimport time\nfor i in range(%d):\n    Path(f'f{i:05d}.txt').touch()\nwhile True:\n    time.sleep(1)\n", workspacequota.MaxEntries+16)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 12000, MemoryMB: 256, WorkspaceBytes: defaultWorkspaceBytes},
	}, Hooks{})

	if resp.Status != model.RunStatusWLE {
		t.Fatalf("expected workspace limit status from entry-count exhaustion, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "workspace entry limit exceeded") {
		t.Fatalf("expected workspace entry diagnostic, got %+v", resp)
	}
	if resp.VerdictSource != "workspace_entries" {
		t.Fatalf("verdict_source = %q, want workspace_entries", resp.VerdictSource)
	}
}

func TestRunMarksWorkspaceDepthLimitExceeded(t *testing.T) {
	forceDirectMode(t)

	script := fmt.Sprintf("from pathlib import Path\nimport time\npath = Path('root')\nfor i in range(%d):\n    path = path / f'd{i:02d}'\npath.mkdir(parents=True)\nwhile True:\n    time.sleep(1)\n", workspacequota.MaxDepth+8)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 12000, MemoryMB: 256, WorkspaceBytes: defaultWorkspaceBytes},
	}, Hooks{})

	if resp.Status != model.RunStatusWLE {
		t.Fatalf("expected workspace limit status from depth exhaustion, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "workspace depth exceeded") {
		t.Fatalf("expected workspace depth diagnostic, got %+v", resp)
	}
	if resp.VerdictSource != "workspace_depth" {
		t.Fatalf("verdict_source = %q, want workspace_depth", resp.VerdictSource)
	}
}

func TestRunFailsClosedWhenWorkspaceScanFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can traverse unreadable directories")
	}
	forceDirectMode(t)

	script := "import os, time\nos.mkdir('hidden', 0)\nwhile True:\n    time.sleep(1)\n"
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 12000, MemoryMB: 256, WorkspaceBytes: defaultWorkspaceBytes},
	}, Hooks{})

	if resp.Status != model.RunStatusWLE {
		t.Fatalf("expected workspace limit status from scan failure, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "workspace scan failed") {
		t.Fatalf("expected workspace scan diagnostic, got %+v", resp)
	}
	if resp.VerdictSource != "workspace_scan" {
		t.Fatalf("verdict_source = %q, want workspace_scan", resp.VerdictSource)
	}
}
