package execute

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/runvalidation"
	"aonohako/internal/security"
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

func TestBuildRejectsSelfHostedEmbeddedRunnerWithoutCgroupParent(t *testing.T) {
	_, err := Build(config.Config{
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetSelfHosted,
				ExecutionTransport: platform.ExecutionTransportEmbedded,
				SandboxBackend:     platform.SandboxBackendHelper,
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a cgroup parent") {
		t.Fatalf("Build() error = %v", err)
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

func TestStreamImageEventsPrefersReservedRootImageOutput(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	rootPath := filepath.Join(workDir, "__img__", "images.jsonl")
	if err := os.MkdirAll(filepath.Dir(rootPath), 0o755); err != nil {
		t.Fatalf("mkdir root image dir: %v", err)
	}
	if err := os.WriteFile(rootPath, []byte("{\"mime\":\"image/png\",\"b64\":\"root\",\"ts\":1}\n"), 0o644); err != nil {
		t.Fatalf("write root image file: %v", err)
	}
	boxPath := filepath.Join(ws.BoxDir, "__img__", "images.jsonl")
	if err := os.MkdirAll(filepath.Dir(boxPath), 0o755); err != nil {
		t.Fatalf("mkdir box image dir: %v", err)
	}
	if err := os.WriteFile(boxPath, []byte("{\"mime\":\"image/png\",\"b64\":\"box\",\"ts\":1}\n"), 0o644); err != nil {
		t.Fatalf("write box image file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var events []string
	go streamImageEvents(ctx, ws, "__img__/images.jsonl", func(mime, b64 string, ts int64) {
		mu.Lock()
		events = append(events, b64)
		mu.Unlock()
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := append([]string(nil), events...)
		mu.Unlock()
		if len(got) > 0 {
			if got[0] != "root" {
				t.Fatalf("first image event = %q, want root", got[0])
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected root image event")
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

func TestRunRejectsNetworkEnabledRequestsWithoutEgressIsolation(t *testing.T) {
	svc := &Service{
		deploymentTarget: platform.DeploymentTargetSelfHosted,
		runtimeTuning:    config.DefaultRuntimeTuningConfig(),
	}
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang:          "binary",
		EnableNetwork: true,
		Limits:        model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "egress-isolated") {
		t.Fatalf("expected self-hosted network request to fail closed, got %+v", resp)
	}
}

func TestRunAllowsOutboundNetworkWhenEnabledInDev(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "local-dev")
	requireSandboxSupport(t)

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
	svc.networkEgressIsolated = true
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

func TestInteractivePipeWriterPropagatesTransportErrors(t *testing.T) {
	reader, pipeWriter := io.Pipe()
	writer := &interactivePipeWriter{w: pipeWriter}
	transportErr := errors.New("peer input failed")
	if err := reader.CloseWithError(transportErr); err != nil {
		t.Fatalf("CloseWithError() error = %v", err)
	}

	n, err := writer.Write([]byte("payload"))
	if n != 0 || !errors.Is(err, transportErr) {
		t.Fatalf("Write() = (%d, %v), want (0, peer input failed)", n, err)
	}
	if !errors.Is(writer.Err(), transportErr) {
		t.Fatalf("Err() = %v, want peer input failed", writer.Err())
	}
}

func TestInteractivePipeWriterReportsWriteAfterDestinationClose(t *testing.T) {
	reader, pipeWriter := io.Pipe()
	defer reader.Close()
	writer := &interactivePipeWriter{w: pipeWriter}
	writer.Close()

	n, err := writer.Write([]byte("payload"))
	if n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write() = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
	if !errors.Is(writer.Err(), io.ErrClosedPipe) {
		t.Fatalf("Err() = %v, want io.ErrClosedPipe", writer.Err())
	}
}

func TestTeeCaptureWriterPropagatesForwardErrors(t *testing.T) {
	capture := cappedBuffer{limit: 32}
	writer := teeCaptureWriter{capture: &capture, forward: failingWriter{}}

	n, err := writer.Write([]byte("payload"))
	if n != 0 || err == nil {
		t.Fatalf("Write() = (%d, %v), want forwarding error", n, err)
	}
	if string(capture.Bytes()) != "payload" {
		t.Fatalf("captured output = %q, want payload", capture.Bytes())
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

func TestRunTwoStepPipelineAcceptsStdoutHandoff(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "encode.sh",
					DataB64: b64("#!/bin/sh\nsed 's/^/encoded:/'\n"),
					Mode:    "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "decode.sh",
					DataB64: b64("#!/bin/sh\nsed 's/^encoded://'\n"),
					Mode:    "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				Stdin:     "answer\n",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
				Handoff:   &model.StepHandoff{ID: "encoded", From: "stdout", MaxBytes: 1024},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinFrom: "encoded",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		ExpectedStdout: "answer\n",
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
	if resp.VerdictSource != "step:decode:stdout" {
		t.Fatalf("verdict_source = %q, want step:decode:stdout", resp.VerdictSource)
	}
	if len(resp.Steps) != 2 {
		t.Fatalf("expected two step results, got %+v", resp.Steps)
	}
	if resp.Steps[0].Status != model.RunStatusAccepted || resp.Steps[0].HandoffBytes == 0 {
		t.Fatalf("unexpected first step result: %+v", resp.Steps[0])
	}
	if resp.Steps[1].Status != model.RunStatusAccepted {
		t.Fatalf("unexpected final step result: %+v", resp.Steps[1])
	}
}

func TestRunTwoStepPipelineCaptureLimitDoesNotReduceHandoffLimit(t *testing.T) {
	forceDirectMode(t)
	captureLimit := 1024
	handoff := strings.Repeat("h", 1536)

	resp := New().Run(context.Background(), &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "encode.sh",
					DataB64: b64(fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", handoff)),
					Mode:    "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "decode.sh",
					DataB64: b64("#!/bin/sh\ncat\n"),
					Mode:    "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 2048},
				Handoff:   &model.StepHandoff{ID: "encoded", From: "stdout", MaxBytes: 2048},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinFrom: "encoded",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 2048},
			},
		},
		ExpectedStdout: handoff,
		CaptureLimits:  &model.CaptureLimits{StdoutBytes: &captureLimit},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("capture limit changed pipeline verdict: %+v", resp)
	}
	if len(resp.Steps) != 2 ||
		resp.Steps[0].HandoffBytes != int64(len(handoff)) ||
		resp.Steps[0].Status != model.RunStatusAccepted ||
		resp.Steps[1].Status != model.RunStatusAccepted {
		t.Fatalf("capture limit changed handoff or step results: %+v", resp.Steps)
	}
}

func TestRunTwoStepPipelineComposesStdinParts(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "encode.sh",
					DataB64: b64("#!/bin/sh\ncat\n"),
					Mode:    "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "decode.sh",
					DataB64: b64("#!/bin/sh\nIFS= read -r mode\nprintf 'mode:%s\\n' \"$mode\"\ncat\n"),
					Mode:    "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				Stdin:     "answer\n",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
				Handoff:   &model.StepHandoff{ID: "encoded", From: "stdout", MaxBytes: 1024},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinParts: []model.StdinPart{
					{Type: "text", Data: "DECODE\n"},
					{Type: "handoff", ID: "encoded"},
				},
				Limits: model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		ExpectedStdout: "mode:DECODE\nanswer\n",
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
	if len(resp.Steps) != 2 || resp.Steps[1].Status != model.RunStatusAccepted {
		t.Fatalf("unexpected step results: %+v", resp.Steps)
	}
}

func TestRunConsumesLargeStdinURL(t *testing.T) {
	forceDirectMode(t)

	inputBytes := runvalidation.MaxTextFieldBytes + 1024
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	inputServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", inputBytes))
		for written := 0; written < inputBytes; {
			size := min(len(chunk), inputBytes-written)
			n, err := w.Write(chunk[:size])
			written += n
			if err != nil {
				return
			}
		}
	}))
	defer inputServer.Close()
	setStdinURLHTTPClientForTest(t, inputServer.URL)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "count.sh",
			DataB64: b64("#!/bin/sh\nwc -c | tr -d ' '\n"),
			Mode:    "exec",
		}},
		StdinURL:       "http://payload.example/input",
		ExpectedStdout: fmt.Sprintf("%d\n", inputBytes),
		Limits: model.Limits{
			TimeMs:         5000,
			MemoryMB:       128,
			WorkspaceBytes: int64(inputBytes + 4<<20),
		},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted for large stdin_url, got %+v", resp)
	}
}

func TestAssembleStepStdinPartsConsumesLargeDataURL(t *testing.T) {
	inputBytes := runvalidation.MaxTextFieldBytes + 1024
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	inputServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", inputBytes))
		for written := 0; written < inputBytes; {
			size := min(len(chunk), inputBytes-written)
			n, err := w.Write(chunk[:size])
			written += n
			if err != nil {
				return
			}
		}
	}))
	defer inputServer.Close()
	setStdinURLHTTPClientForTest(t, inputServer.URL)

	step := model.RunStep{
		ID: "encode",
		StdinParts: []model.StdinPart{{
			Type:    "text",
			DataURL: "http://payload.example/input",
		}},
		Limits: model.Limits{WorkspaceBytes: int64(inputBytes + 4<<20)},
	}
	stdinFile, err := assembleStepStdinParts(context.Background(), step, nil, t.TempDir(), stdinURLMaxBytes(step.Limits), nil)
	if err != nil {
		t.Fatalf("assembleStepStdinParts returned error: %v", err)
	}
	defer stdinFile.Close()

	info, err := stdinFile.Stat()
	if err != nil {
		t.Fatalf("stat stdin file: %v", err)
	}
	if info.Size() != int64(inputBytes) {
		t.Fatalf("stdin file size = %d, want %d", info.Size(), inputBytes)
	}
}

func TestRunStepPipelineBoundsCumulativeStdinURLDownloadTime(t *testing.T) {
	var requests atomic.Int64
	inputServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-time.After(80 * time.Millisecond):
			_, _ = io.WriteString(w, "x")
		case <-r.Context().Done():
		}
	}))
	defer inputServer.Close()
	setStdinURLHTTPClientForTest(t, inputServer.URL)

	svc := New()
	svc.stdinURLTimeout = 130 * time.Millisecond
	started := time.Now()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "encode.sh",
					DataB64: b64("#!/bin/sh\ncat\n"),
					Mode:    "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "decode.sh",
					DataB64: b64("#!/bin/sh\ncat\n"),
					Mode:    "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				StdinParts: []model.StdinPart{
					{Type: "text", DataURL: "http://payload.example/first"},
					{Type: "text", DataURL: "http://payload.example/second"},
				},
				Limits:  model.Limits{TimeMs: 1000, MemoryMB: 128},
				Handoff: &model.StepHandoff{ID: "encoded", From: "stdout", MaxBytes: 1024},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinFrom: "encoded",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		ExpectedStdout: "xx",
	}, Hooks{})

	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "deadline exceeded") {
		t.Fatalf("expected cumulative stdin URL deadline failure, got %+v", resp)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cumulative stdin URL timeout returned after %v", elapsed)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("download requests = %d, want 2", got)
	}
}

func TestServiceRunRejectsRequestWideBinaryBudget(t *testing.T) {
	maxSizeBinary := base64.StdEncoding.EncodeToString(make([]byte, runvalidation.MaxBinaryFileBytes))
	programs := make([]model.RunProgram, 4)
	for i := range programs {
		programs[i] = model.RunProgram{
			ID:   fmt.Sprintf("program-%d", i),
			Lang: "binary",
			Binaries: []model.Binary{{
				Name:    "Main",
				DataB64: maxSizeBinary,
				Mode:    "exec",
			}},
		}
	}

	resp := New().Run(context.Background(), &model.RunRequest{Programs: programs}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "binaries total size exceeded") {
		t.Fatalf("Service.Run over-budget response = %+v", resp)
	}
}

func TestRunStepPipelineDoesNotChargeSandboxTimeToStdinURLBudget(t *testing.T) {
	forceDirectMode(t)

	inputServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "prefix\n")
	}))
	defer inputServer.Close()
	setStdinURLHTTPClientForTest(t, inputServer.URL)

	svc := New()
	svc.stdinURLTimeout = 100 * time.Millisecond
	resp := svc.Run(context.Background(), &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "encode.sh",
					DataB64: b64("#!/bin/sh\nsleep 0.2\nprintf 'handoff\\n'\n"),
					Mode:    "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "decode.sh",
					DataB64: b64("#!/bin/sh\ncat\n"),
					Mode:    "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
				Handoff:   &model.StepHandoff{ID: "encoded", From: "stdout", MaxBytes: 1024},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinParts: []model.StdinPart{
					{Type: "text", DataURL: "http://payload.example/prefix"},
					{Type: "handoff", ID: "encoded"},
				},
				Limits: model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		ExpectedStdout: "prefix\nhandoff\n",
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("sandbox time consumed stdin URL budget: %+v", resp)
	}
}

func TestRunJavaScriptCanReadDevStdin(t *testing.T) {
	forceDirectMode(t)
	if err := exec.Command("sh", "-c", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin command -v node").Run(); err != nil {
		t.Skip("node is unavailable in the sandbox command PATH on this runner")
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "javascript",
		Binaries: []model.Binary{{
			Name: "main.js",
			DataB64: b64(
				"const fs = require('fs');\n" +
					"const input = fs.readFileSync('/dev/stdin', 'utf8');\n" +
					"process.stdout.write('node:' + input);\n",
			),
		}},
		Stdin:          "stdin-ok\n",
		ExpectedStdout: "node:stdin-ok\n",
		Limits:         model.Limits{TimeMs: 3000, MemoryMB: 192},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Node /dev/stdin read to be accepted, got %+v", resp)
	}
}

func TestRunTwoStepPipelineAcceptsFileHandoff(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "encode.sh",
					DataB64: b64("#!/bin/sh\nprintf 'encoded-file\\n' > encoded.txt\n"),
					Mode:    "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "decode.sh",
					DataB64: b64("#!/bin/sh\ncat\n"),
					Mode:    "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
				Handoff:   &model.StepHandoff{ID: "encoded", From: "file", Path: "encoded.txt", MaxBytes: 1024},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinFrom: "encoded",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		ExpectedStdout: "encoded-file\n",
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
	if resp.Steps[0].HandoffBytes != int64(len("encoded-file\n")) {
		t.Fatalf("handoff bytes = %d", resp.Steps[0].HandoffBytes)
	}
}

func TestRunTwoStepPipelineSeparatesFileHandoffFromStdoutFlood(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name: "encode.sh",
					DataB64: b64(`#!/bin/sh
printf 'encoded-file\n' > encoded.txt
i=0
while [ "$i" -lt 256 ]; do
  printf '0123456789abcdef0123456789abcdef\n'
  i=$((i + 1))
done
`),
					Mode: "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "decode.sh",
					DataB64: b64("#!/bin/sh\ncat\n"),
					Mode:    "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 1024},
				Handoff:   &model.StepHandoff{ID: "encoded", From: "file", Path: "encoded.txt", MaxBytes: 1024},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinFrom: "encoded",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		ExpectedStdout: "encoded-file\n",
	}, Hooks{})

	if resp.Status != model.RunStatusRE {
		t.Fatalf("expected intermediate stdout flood runtime error, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "stdout exceeded output limit") {
		t.Fatalf("reason = %q, want stdout output-limit diagnostic", resp.Reason)
	}
	if strings.Contains(resp.Reason, "handoff exceeded") {
		t.Fatalf("file handoff stdout flood should not be reported as handoff overflow: %q", resp.Reason)
	}
	if resp.VerdictSource != "step:encode:stdout" {
		t.Fatalf("verdict_source = %q, want step:encode:stdout", resp.VerdictSource)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].HandoffBytes != int64(len("encoded-file\n")) {
		t.Fatalf("unexpected step results: %+v", resp.Steps)
	}
}

func TestRunTwoStepPipelineRejectsOversizedHandoff(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "encoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "encode.sh",
					DataB64: b64("#!/bin/sh\nprintf 'abcdef\\n'\n"),
					Mode:    "exec",
				}},
			},
			{
				ID:   "decoder",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "decode.sh",
					DataB64: b64("#!/bin/sh\ncat\n"),
					Mode:    "exec",
				}},
			},
		},
		Steps: []model.RunStep{
			{
				ID:        "encode",
				ProgramID: "encoder",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
				Handoff:   &model.StepHandoff{ID: "encoded", From: "stdout", MaxBytes: 3},
			},
			{
				ID:        "decode",
				ProgramID: "decoder",
				StdinFrom: "encoded",
				Limits:    model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		ExpectedStdout: "abcdef\n",
	}, Hooks{})

	if resp.Status != model.RunStatusRE {
		t.Fatalf("expected handoff runtime error, got %+v", resp)
	}
	if !strings.Contains(resp.Reason, "handoff exceeded") {
		t.Fatalf("unexpected reason: %+v", resp)
	}
	if len(resp.Steps) != 1 {
		t.Fatalf("decoder should not run after handoff failure, got steps %+v", resp.Steps)
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
			status, _, _, source := evaluateRunStatus(context.Background(), Workspace{}, &tc.req, tc.res, tc.judgeOut, tc.judgeSource, "", nil, config.DefaultRuntimeTuningConfig(), "")
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

func TestRunLargeOutputUsesConfiguredCapForJudgeAndResponse(t *testing.T) {
	forceDirectMode(t)

	svc := New()
	outputLen := defaultMaxOutputBytes + 4096
	output := strings.Repeat("a", outputLen)
	script := fmt.Sprintf("#!/bin/sh\ni=0\nwhile [ \"$i\" -lt %d ]; do\n  printf a\n  i=$((i+1))\ndone\n", outputLen)

	var stdoutLog string
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64(script),
			Mode:    "exec",
		}},
		ExpectedStdout: output,
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: outputLen},
	}, Hooks{
		OnLog: func(stream, msg string) {
			if stream == "stdout" {
				stdoutLog = msg
			}
		},
	})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
	if stdoutLog != output {
		t.Fatalf("unexpected stdout log length=%d", len(stdoutLog))
	}

	resp = svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64(script),
			Mode:    "exec",
		}},
		ExpectedStdout: "different\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: outputLen},
	}, Hooks{})
	if resp.Status != model.RunStatusWA {
		t.Fatalf("expected WA, got %+v", resp)
	}
	if resp.Stdout != output {
		t.Fatalf("unexpected stdout response length=%d", len(resp.Stdout))
	}
}

func TestOutputLimitAllowsLargeJudgeCap(t *testing.T) {
	const expectedMaxOutputBytes = 64 << 20
	if hardMaxOutputBytes != expectedMaxOutputBytes {
		t.Fatalf("execute hard output cap = %d, want restored 64 MiB cap %d", hardMaxOutputBytes, expectedMaxOutputBytes)
	}
	if hardMaxOutputBytes != runvalidation.MaxOutputBytes {
		t.Fatalf("execute hard output cap = %d, validation cap = %d", hardMaxOutputBytes, runvalidation.MaxOutputBytes)
	}
	req := &model.RunRequest{Limits: model.Limits{OutputBytes: hardMaxOutputBytes}}
	if got := outputLimitBytes(req); got != hardMaxOutputBytes {
		t.Fatalf("outputLimitBytes() = %d, want %d", got, hardMaxOutputBytes)
	}
	if got := responseOutputLimitBytes(req); got != maxResponseOutputBytes {
		t.Fatalf("responseOutputLimitBytes() = %d, want %d", got, maxResponseOutputBytes)
	}
}

func TestResponseCaptureLimitCannotExceedExecutionLimit(t *testing.T) {
	executionLimit := 4096
	configuredLimit := 8192
	req := &model.RunRequest{
		Limits:        model.Limits{OutputBytes: executionLimit},
		CaptureLimits: &model.CaptureLimits{StdoutBytes: &configuredLimit},
	}
	if got := responseStdoutLimitBytes(req); got != executionLimit {
		t.Fatalf("responseStdoutLimitBytes() = %d, want execution limit %d", got, executionLimit)
	}
}

func TestRunCaptureLimitsClipStreamsIndependently(t *testing.T) {
	forceDirectMode(t)
	stdoutLimit := 1024
	stderrLimit := 0
	stdout := strings.Repeat("o", 2048)
	stderr := strings.Repeat("e", 2048)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nprintf '%%s' '%s' >&2\nexit 7\n", stdout, stderr)
	logs := map[string]string{}

	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64(script),
			Mode:    "exec",
		}},
		Limits: model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 4096},
		CaptureLimits: &model.CaptureLimits{
			StdoutBytes: &stdoutLimit,
			StderrBytes: &stderrLimit,
		},
	}, Hooks{OnLog: func(stream, msg string) {
		logs[stream] += msg
	}})

	if resp.Status != model.RunStatusRE || resp.VerdictSource != "exit_code" {
		t.Fatalf("response verdict = %+v, want exit-code runtime error", resp)
	}
	if resp.Stdout != stdout[:stdoutLimit] || resp.Stderr != "" {
		t.Fatalf("captured output lengths = (%d, %d), want (%d, 0)", len(resp.Stdout), len(resp.Stderr), stdoutLimit)
	}
	if !resp.StdoutTruncated || !resp.StderrTruncated {
		t.Fatalf("capture truncation flags = (%v, %v), want both true", resp.StdoutTruncated, resp.StderrTruncated)
	}
	if logs["stdout"] != stdout[:stdoutLimit] {
		t.Fatalf("stdout log length = %d, want %d", len(logs["stdout"]), stdoutLimit)
	}
	if _, ok := logs["stderr"]; ok {
		t.Fatalf("stderr_bytes=0 emitted a log: %q", logs["stderr"])
	}
}

func TestCaptureLimitZeroDoesNotDisableOutputLimitVerdict(t *testing.T) {
	forceDirectMode(t)
	stdoutLimit := 0
	output := strings.Repeat("x", 2048)
	var logs int

	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64(fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", output)),
			Mode:    "exec",
		}},
		ExpectedStdout: strings.Repeat("x", 1024),
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 1024},
		CaptureLimits:  &model.CaptureLimits{StdoutBytes: &stdoutLimit},
	}, Hooks{OnLog: func(string, string) {
		logs++
	}})

	if resp.Status != model.RunStatusWA ||
		resp.Reason != "stdout exceeded output limit" ||
		resp.VerdictSource != "stdout_limit" {
		t.Fatalf("capture limit changed output-limit verdict: %+v", resp)
	}
	if resp.Stdout != "" || !resp.StdoutTruncated {
		t.Fatalf("stdout capture = %q truncated=%v, want empty and truncated", resp.Stdout, resp.StdoutTruncated)
	}
	if logs != 0 {
		t.Fatalf("stdout_bytes=0 emitted %d logs", logs)
	}
}

func TestSandboxInitReasonHonorsZeroStderrCaptureLimit(t *testing.T) {
	forceDirectMode(t)
	stderrLimit := 0
	privateDiagnostic := strings.Repeat("private sandbox detail ", 64)

	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name: "run.sh",
			DataB64: b64(fmt.Sprintf(
				"#!/bin/sh\nprintf 'sandbox-init: %%s' '%s' >&2\nexit 120\n",
				privateDiagnostic,
			)),
			Mode: "exec",
		}},
		Limits:        model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 4096},
		CaptureLimits: &model.CaptureLimits{StderrBytes: &stderrLimit},
	}, Hooks{})

	if resp.Status != model.RunStatusInitFail ||
		resp.VerdictSource != "sandbox_init" ||
		resp.Reason != "sandbox initialization failed" {
		t.Fatalf("sandbox init response = %+v, want structural reason without stderr", resp)
	}
	if strings.Contains(resp.Reason, "private sandbox detail") {
		t.Fatalf("sandbox init reason leaked stderr: %q", resp.Reason)
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
		Lang:           "binary",
		Binaries:       []model.Binary{{Name: "runner", DataB64: buildCTestBinary(t, code), Mode: "exec"}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksSysVMessageQueuesAndSemaphores(t *testing.T) {
	requireSandboxSupport(t)

	code := `
#include <errno.h>
#include <stdio.h>
#include <sys/ipc.h>
#include <sys/msg.h>
#include <sys/sem.h>

int main(void) {
	errno = 0;
	int msg_id = msgget(IPC_PRIVATE, IPC_CREAT | 0600);
	int msg_errno = errno;
	errno = 0;
	int sem_id = semget(IPC_PRIVATE, 1, IPC_CREAT | 0600);
	int sem_errno = errno;
	if (msg_id == -1 && msg_errno == EPERM && sem_id == -1 && sem_errno == EPERM) {
		puts("blocked");
		return 0;
	}
	printf("unexpected msg=%d sem=%d msg_errno=%d sem_errno=%d\n", msg_id, sem_id, msg_errno, sem_errno);
	return 1;
}
`

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang:           "binary",
		Binaries:       []model.Binary{{Name: "runner", DataB64: buildCTestBinary(t, code), Mode: "exec"}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	msgID, semID := -1, -1
	if strings.HasPrefix(resp.Stdout, "unexpected ") {
		_, _ = fmt.Sscanf(resp.Stdout, "unexpected msg=%d sem=%d", &msgID, &semID)
	}
	defer func() {
		if msgID >= 0 {
			_, _, _ = syscall.Syscall(syscall.SYS_MSGCTL, uintptr(msgID), 0, 0)
		}
		if semID >= 0 {
			_, _, _ = syscall.Syscall6(syscall.SYS_SEMCTL, uintptr(semID), 0, 0, 0, 0, 0)
		}
	}()
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected SysV IPC creation to be blocked, got %+v", resp)
	}
}

func TestRunAllowsPrlimitQueriesNeededByManagedRuntimes(t *testing.T) {
	requireSandboxSupport(t)

	payload := buildCTestBinary(t, `#define _GNU_SOURCE
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

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang:           "binary",
		Binaries:       []model.Binary{{Name: "probe", DataB64: payload, Mode: "exec"}},
		ExpectedStdout: "ok\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunBlocksUnsafePrlimitAndQueuedSignalsAgainstSameUIDPeer(t *testing.T) {
	requireSandboxSupport(t)

	target := exec.Command("sleep", "10")
	target.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	if err := target.Start(); err != nil {
		t.Fatalf("start same-UID target process: %v", err)
	}
	defer func() {
		_ = target.Process.Kill()
		_, _ = target.Process.Wait()
	}()

	code := fmt.Sprintf(`
#define _GNU_SOURCE
#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <unistd.h>

int main(void) {
	const pid_t peer = %d;
	struct rlimit current;
	if (syscall(SYS_prlimit64, 0, RLIMIT_NOFILE, NULL, &current) != 0) {
		perror("safe prlimit query");
		return 2;
	}
	struct rlimit lowered = {16, 16};
	errno = 0;
	long self_set = syscall(SYS_prlimit64, 0, RLIMIT_NOFILE, &lowered, NULL);
	int self_errno = errno;
	errno = 0;
	long peer_query = syscall(SYS_prlimit64, peer, RLIMIT_NOFILE, NULL, &current);
	int peer_errno = errno;
	siginfo_t info = {0};
	info.si_signo = SIGCONT;
	info.si_code = SI_QUEUE;
	errno = 0;
	long queued = syscall(SYS_rt_sigqueueinfo, peer, SIGCONT, &info);
	int queued_errno = errno;
	errno = 0;
	long thread_queued = syscall(SYS_rt_tgsigqueueinfo, peer, peer, SIGCONT, &info);
	int thread_errno = errno;
	if (self_set == -1 && self_errno == EPERM &&
		peer_query == -1 && peer_errno == EPERM &&
		queued == -1 && queued_errno == EPERM &&
		thread_queued == -1 && thread_errno == EPERM) {
		puts("blocked");
		return 0;
	}
	printf("unexpected self=%%ld/%%d peer=%%ld/%%d queued=%%ld/%%d thread=%%ld/%%d\n",
		self_set, self_errno, peer_query, peer_errno, queued, queued_errno, thread_queued, thread_errno);
	return 1;
}
`, target.Process.Pid)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang:           "binary",
		Binaries:       []model.Binary{{Name: "runner", DataB64: buildCTestBinary(t, code), Mode: "exec"}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected unsafe peer-control syscalls to be blocked, got %+v", resp)
	}
	if err := target.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("same-UID target should remain alive: %v", err)
	}
}

func TestRunDefaultStackLimitUsesInheritedHardLimit(t *testing.T) {
	requireSandboxSupport(t)

	payload := buildCTestBinary(t, `#define _GNU_SOURCE
#include <stdio.h>
#include <sys/resource.h>

int main(void) {
	struct rlimit lim;
	if (prlimit(0, RLIMIT_STACK, NULL, &lim) != 0) {
		perror("prlimit");
		return 1;
	}
	if (lim.rlim_cur == RLIM_INFINITY) {
		puts("unlimited");
		return 0;
	}
	printf("%llu\n", (unsigned long long)lim.rlim_cur);
	return 0;
}
`)

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "probe",
			DataB64: payload,
			Mode:    "exec",
		}},
		ExpectedStdout: "unlimited\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted with unlimited default stack, got %+v", resp)
	}
}

func TestExecuteSandboxAllowsLocalUnixSocketPairsForManagedRuntimes(t *testing.T) {
	requireSandboxSupport(t)

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	workDir := sandboxAccessibleTempDir(t)
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
					"import socket, sys\na, b = socket.socketpair()\na.sendall(b'ok')\nsys.exit(0 if b.recv(2) == b'ok' else 1)\n",
				},
				&model.RunRequest{
					Lang:   lang,
					Limits: model.Limits{TimeMs: 2000, MemoryMB: 256},
				},
				nil,
				Hooks{},
				1024,
				config.DefaultRuntimeTuningConfig(),
				"",
			)
			if result.Status != "OK" {
				t.Fatalf("expected OK, got %+v", result)
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

	workDir := sandboxAccessibleTempDir(t)
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
		nil,
		Hooks{},
		1024,
		config.DefaultRuntimeTuningConfig(),
		"",
	)
	if result.Status != "OK" {
		t.Fatalf("expected OK, got %+v", result)
	}
}

func TestExecuteSandboxBlocksUnixSendmsgForManagedRuntimeSocketAllowance(t *testing.T) {
	requireSandboxSupport(t)

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-managed-dgram-%d.sock", time.Now().UnixNano()))
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

	workDir := sandboxAccessibleTempDir(t)
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)\n    s.sendmsg([b'escape'], [], 0, %q)\n    print('sent')\nexcept OSError:\n    print('blocked')\n",
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
		nil,
		Hooks{},
		1024,
		config.DefaultRuntimeTuningConfig(),
		"",
	)
	if result.Status != "OK" {
		t.Fatalf("expected OK, got %+v", result)
	}

	_ = listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 64)
	if n, _, err := listener.ReadFromUnix(buf); err == nil {
		t.Fatalf("expected no datagram delivery, got %q", string(buf[:n]))
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

func TestRunSandboxEnvironmentIncludesRuntimePythonPath(t *testing.T) {
	requireSandboxSupport(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: b64("import os\nprint(os.environ.get('PYTHONPATH', ''))\n"),
		}},
		ExpectedStdout: "/usr/local/lib/aonohako/python\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted with runtime PYTHONPATH, got %+v", resp)
	}
}

func TestRunSandboxEnablesImageCaptureOnlyForImageSidecar(t *testing.T) {
	requireSandboxSupport(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	svc := New()
	run := func(sidecars []model.OutputFile) string {
		t.Helper()
		resp := svc.Run(context.Background(), &model.RunRequest{
			Lang: "python",
			Binaries: []model.Binary{{
				Name:    "main.py",
				DataB64: b64("import os\nprint(os.environ.get('IMG_CAPTURE', ''))\n"),
			}},
			ExpectedStdout: "",
			Limits:         model.Limits{TimeMs: 1000, MemoryMB: 256},
			SidecarOutputs: sidecars,
		}, Hooks{})
		if resp.Status != model.RunStatusAccepted {
			t.Fatalf("expected Accepted, got %+v", resp)
		}
		return resp.Stdout
	}

	if got := run(nil); got != "\n" {
		t.Fatalf("IMG_CAPTURE without image sidecar = %q, want empty line", got)
	}
	if got := run([]model.OutputFile{{Path: "__img__/images.jsonl"}}); got != "1\n" {
		t.Fatalf("IMG_CAPTURE with image sidecar = %q, want 1", got)
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
	target.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
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

func TestRunSPJUsesFileArgumentsWithoutDuplicatingStdoutOnStdin(t *testing.T) {
	requireSandboxSupport(t)

	spj := `#!/usr/bin/env python3
import sys

stdin = sys.stdin.read()
with open(sys.argv[2], "r", encoding="utf-8") as handle:
    output = handle.read()
with open(sys.argv[3], "r", encoding="utf-8") as handle:
    answer = handle.read()
if stdin:
    raise SystemExit(3)
if output != "actual\n":
    raise SystemExit(4)
if answer != "official\n":
    raise SystemExit(5)
raise SystemExit(0)
`
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte("print('actual')\n")),
		}},
		ExpectedStdout: "official\n",
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
		t.Fatalf("expected SPJ to accept without stdin duplication, got %+v", resp)
	}
}

func TestRunSPJCanReadRequestedSidecarOutputs(t *testing.T) {
	requireSandboxSupport(t)

	contestant := `import json
import os

os.makedirs("__img__", exist_ok=True)
with open("__img__/images.jsonl", "w", encoding="utf-8") as handle:
    handle.write(json.dumps({"mime": "image/png", "b64": "abc", "ts": 123}) + "\n")
`
	spj := `#!/usr/bin/env python3
import json
import os
import sys

image_path = os.path.join("sidecar", "__img__", "images.jsonl")
with open(image_path, "r", encoding="utf-8") as handle:
    rows = [json.loads(line) for line in handle if line.strip()]
if rows != [{"mime": "image/png", "b64": "abc", "ts": 123}]:
    raise SystemExit(4)
with open(sys.argv[2], "r", encoding="utf-8") as handle:
    if handle.read() != "":
        raise SystemExit(5)
raise SystemExit(0)
`
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(contestant)),
		}},
		ExpectedStdout: "",
		SPJ: &model.SPJSpec{
			Binary: &model.Binary{
				Name:    "spj.py",
				DataB64: base64.StdEncoding.EncodeToString([]byte(spj)),
			},
			Lang:           "python",
			SidecarOutputs: []model.OutputFile{{Path: "__img__/images.jsonl"}},
		},
		SidecarOutputs: []model.OutputFile{{Path: "__img__/images.jsonl"}},
		Limits:         model.Limits{TimeMs: 3000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected SPJ to accept with image sidecar, got %+v", resp)
	}
}

func TestRunSPJCanReadTopLevelSidecarOutputsByDefault(t *testing.T) {
	requireSandboxSupport(t)

	contestant := `import json
import os

os.makedirs("__img__", exist_ok=True)
with open("__img__/images.jsonl", "w", encoding="utf-8") as handle:
    handle.write(json.dumps({"mime": "image/png", "b64": "abc", "ts": 123}) + "\n")
`
	spj := `#!/usr/bin/env python3
import json
import os

image_path = os.path.join("sidecar", "__img__", "images.jsonl")
with open(image_path, "r", encoding="utf-8") as handle:
    rows = [json.loads(line) for line in handle if line.strip()]
if rows != [{"mime": "image/png", "b64": "abc", "ts": 123}]:
    raise SystemExit(4)
raise SystemExit(0)
`
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(contestant)),
		}},
		ExpectedStdout: "",
		SPJ: &model.SPJSpec{
			Binary: &model.Binary{
				Name:    "spj.py",
				DataB64: base64.StdEncoding.EncodeToString([]byte(spj)),
			},
			Lang: "python",
		},
		SidecarOutputs: []model.OutputFile{{Path: "__img__/images.jsonl"}},
		Limits:         model.Limits{TimeMs: 3000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected SPJ to accept default image sidecar, got %+v", resp)
	}
}

func TestRunRejectsIncompleteSPJBeforeStartingSandbox(t *testing.T) {
	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "runner",
			DataB64: b64("#!/bin/sh\nexit 0\n"),
			Mode:    "exec",
		}},
		Limits: model.Limits{TimeMs: 1000, MemoryMB: 64},
		SPJ:    &model.SPJSpec{},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "spj.binary is required") {
		t.Fatalf("incomplete SPJ should fail closed before sandbox startup, got %+v", resp)
	}
}

func TestRunSPJUsesSingleStableStdinURLFetch(t *testing.T) {
	requireSandboxSupport(t)

	var requests atomic.Int64
	inputServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) != 1 {
			http.Error(w, "stdin URL fetched more than once", http.StatusGone)
			return
		}
		_, _ = w.Write([]byte("payload\n"))
	}))
	defer inputServer.Close()
	setStdinURLHTTPClientForTest(t, inputServer.URL)

	checker := `#!/usr/bin/env python3
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    judge_input = handle.read()
with open(sys.argv[2], "r", encoding="utf-8") as handle:
    actual = handle.read()
with open(sys.argv[3], "r", encoding="utf-8") as handle:
    official = handle.read()
if judge_input != "payload\n":
    raise SystemExit(2)
if actual != "actual:payload\n":
    raise SystemExit(3)
if official != "official\n":
    raise SystemExit(4)
`
	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: b64("import sys\nsys.stdout.write('actual:' + sys.stdin.read())\n"),
		}},
		StdinURL:       "http://payload.example/input",
		ExpectedStdout: "official\n",
		Limits:         model.Limits{TimeMs: 3000, MemoryMB: 128},
		SPJ: &model.SPJSpec{
			Binary: &model.Binary{Name: "checker.py", DataB64: b64(checker)},
			Lang:   "python",
		},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("SPJ should share the contestant's stable URL input, got %+v", resp)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("stdin URL requests = %d, want exactly 1", got)
	}
}

func TestRunStepSPJReceivesExactFinalStepStdin(t *testing.T) {
	requireSandboxSupport(t)

	tests := []struct {
		name          string
		producer      string
		handoff       model.StepHandoff
		consumerInput func() (string, []model.StdinPart)
		wantInput     string
	}{
		{
			name:     "stdout handoff with normalized identifiers",
			producer: "print('handoff', end='')\n",
			handoff:  model.StepHandoff{ID: " transfer ", From: "   "},
			consumerInput: func() (string, []model.StdinPart) {
				return " transfer ", nil
			},
			wantInput: "handoff",
		},
		{
			name:     "composed stdin parts",
			producer: "print('handoff', end='')\n",
			handoff:  model.StepHandoff{ID: "transfer", From: "stdout"},
			consumerInput: func() (string, []model.StdinPart) {
				return "", []model.StdinPart{{Type: "text", Data: "prefix:"}, {Type: "handoff", From: " transfer "}}
			},
			wantInput: "prefix:handoff",
		},
		{
			name:     "normalized file handoff",
			producer: "open('payload.txt', 'w', encoding='utf-8').write('file-handoff')\n",
			handoff:  model.StepHandoff{ID: "transfer", From: " FILE_OUTPUT ", Path: "payload.txt"},
			consumerInput: func() (string, []model.StdinPart) {
				return "transfer", nil
			},
			wantInput: "file-handoff",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdinFrom, stdinParts := tc.consumerInput()
			checker := fmt.Sprintf(`#!/usr/bin/env python3
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    judge_input = handle.read()
with open(sys.argv[2], "r", encoding="utf-8") as handle:
    actual = handle.read()
with open(sys.argv[3], "r", encoding="utf-8") as handle:
    official = handle.read()
if judge_input != %q:
    raise SystemExit(2)
if actual != "actual:" + %q:
    raise SystemExit(3)
if official != "official\n":
    raise SystemExit(4)
`, tc.wantInput, tc.wantInput)
			resp := New().Run(context.Background(), &model.RunRequest{
				Programs: []model.RunProgram{
					{ID: " producer ", Lang: "python", Binaries: []model.Binary{{Name: "producer.py", DataB64: b64(tc.producer)}}},
					{ID: "consumer", Lang: "python", Binaries: []model.Binary{{Name: "consumer.py", DataB64: b64("import sys\nsys.stdout.write('actual:' + sys.stdin.read())\n")}}},
				},
				Steps: []model.RunStep{
					{ID: " produce ", ProgramID: "producer", Limits: model.Limits{TimeMs: 2000, MemoryMB: 128}, Handoff: &tc.handoff},
					{ID: "consume", ProgramID: " consumer ", StdinFrom: stdinFrom, StdinParts: stdinParts, Limits: model.Limits{TimeMs: 2000, MemoryMB: 128}},
				},
				ExpectedStdout: "official\n",
				SPJ: &model.SPJSpec{
					Binary: &model.Binary{Name: "checker.py", DataB64: b64(checker)},
					Lang:   "python",
				},
			}, Hooks{})
			if resp.Status != model.RunStatusAccepted {
				t.Fatalf("SPJ should receive exact final-step stdin, got %+v", resp)
			}
		})
	}
}

func TestRunSPJRejectsNonFiniteScore(t *testing.T) {
	requireSandboxSupport(t)

	for _, score := range []string{"NaN", "+Inf", "-Inf"} {
		t.Run(score, func(t *testing.T) {
			checker := fmt.Sprintf("#!/usr/bin/env python3\nprint(%q)\n", score)
			resp := New().Run(context.Background(), &model.RunRequest{
				Lang:     "python",
				Binaries: []model.Binary{{Name: "main.py", DataB64: b64("print('actual')\n")}},
				Limits:   model.Limits{TimeMs: 3000, MemoryMB: 128},
				SPJ: &model.SPJSpec{
					Binary:    &model.Binary{Name: "checker.py", DataB64: b64(checker)},
					Lang:      "python",
					EmitScore: true,
				},
			}, Hooks{})
			if resp.Status != model.RunStatusRE || !strings.Contains(resp.Reason, "score out of range") {
				t.Fatalf("non-finite SPJ score should be rejected, got %+v", resp)
			}
		})
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

func TestRunInteractiveIOAcceptsInteractorVerdict(t *testing.T) {
	requireSandboxSupport(t)

	contestant := `import sys
n = int(sys.stdin.readline())
print(n + 1, flush=True)
`
	interactor := `import sys
input_path, output_path, answer_path = sys.argv[1:4]
with open(input_path, "r", encoding="utf-8") as handle:
    n = int(handle.read().strip())
with open(answer_path, "r", encoding="utf-8") as handle:
    expected = handle.read().strip()
print(n, flush=True)
line = sys.stdin.readline().strip()
with open(output_path, "w", encoding="utf-8") as handle:
    handle.write(line + "\n")
if line != expected:
    sys.stderr.write(f"expected {expected}, got {line}\n")
    raise SystemExit(1)
`

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(contestant)),
		}},
		Stdin:          "41\n",
		ExpectedStdout: "42\n",
		Interactor: &model.InteractorSpec{
			Lang: "python",
			Binaries: []model.Binary{{
				Name:    "interactor.py",
				DataB64: base64.StdEncoding.EncodeToString([]byte(interactor)),
			}},
		},
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected interactive run to accept, got %+v", resp)
	}
	if resp.Score == nil || *resp.Score != 1 {
		t.Fatalf("expected accepted interactive score 1, got %+v", resp.Score)
	}
	if len(resp.Steps) != 2 || resp.Steps[0].ID != "contestant" || resp.Steps[1].ID != "interactor" {
		t.Fatalf("expected contestant and interactor step results, got %+v", resp.Steps)
	}
}

func TestRunInteractiveKeepsJudgeFixturesUnreadableToContestant(t *testing.T) {
	requireSandboxSupport(t)

	contestant := `import os
import sys
import time

sys.stdin.readline()
compromised = []
try:
    with open(os.path.join(os.path.dirname(os.getcwd()), 'contestant-root-marker'), 'wb') as marker:
        marker.write(b'mutated')
    compromised.append('write-own-root')
except OSError:
    pass
try:
    with open('contestant-rename-source', 'wb') as source:
        source.write(b'mutated')
    os.rename('contestant-rename-source', '../contestant-root-renamed')
    compromised.append('rename-into-own-root')
except OSError:
    pass
deadline = time.monotonic() + 1.0
while time.monotonic() < deadline and not compromised:
    found_fixture_path = False
    for pid in os.listdir('/proc'):
        if not pid.isdigit():
            continue
        try:
            command = open('/proc/' + pid + '/cmdline', 'rb').read()
        except OSError:
            continue
        fixture_paths = [raw_path for raw_path in command.split(b'\0')
                         if b'interactive-input-' in raw_path or b'interactive-answer-' in raw_path]
        if not fixture_paths:
            continue
        found_fixture_path = True
        for raw_path in fixture_paths:
            try:
                with open(os.fsdecode(raw_path), 'rb') as fixture:
                    compromised.append('read-fixture')
            except OSError:
                pass
            fixture_path = os.fsdecode(raw_path)
            try:
                with open(os.path.join(os.path.dirname(fixture_path), 'contestant-marker'), 'wb') as marker:
                    marker.write(b'mutated')
                compromised.append('write-judge-tmp')
            except OSError:
                pass
            try:
                os.unlink(fixture_path)
                compromised.append('unlink-fixture')
            except OSError:
                pass
        for raw_path in command.split(b'\0'):
            if b'/box/' not in raw_path:
                continue
            try:
                box_path = os.path.dirname(os.fsdecode(raw_path))
                with open(os.path.join(box_path, 'contestant-marker'), 'wb') as marker:
                    marker.write(b'mutated')
                compromised.append('write-judge-box')
            except OSError:
                pass
    if found_fixture_path:
        break
    time.sleep(0.01)

print('compromised' if compromised else 'blocked', flush=True)
`
	interactor := `import sys

input_path, output_path, answer_path = sys.argv[1:4]
with open(input_path, 'r', encoding='utf-8') as fixture:
    if fixture.read() != 'fixture-secret\n':
        raise SystemExit(3)
with open(answer_path, 'r', encoding='utf-8') as fixture:
    if fixture.read() != 'judge-secret\n':
        raise SystemExit(3)

print('probe', flush=True)
result = sys.stdin.readline().strip()
with open(output_path, 'w', encoding='utf-8') as transcript:
    transcript.write(result + '\n')
if result != 'blocked':
    raise SystemExit(1)
`

	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: b64(contestant),
		}},
		Stdin:          "fixture-secret\n",
		ExpectedStdout: "judge-secret\n",
		Interactor: &model.InteractorSpec{
			Lang: "python",
			Binaries: []model.Binary{{
				Name:    "interactor.py",
				DataB64: b64(interactor),
			}},
		},
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("contestant read an interactive judge fixture or isolation failed: %+v", resp)
	}
}

func TestValidateInteractivePeerRuntimeIsolation(t *testing.T) {
	if err := validateInteractivePeerRuntimeIsolation("csharp", "fsharp"); err == nil {
		t.Fatal("two concurrent .NET peers must fail closed instead of racing shared CoreCLR state")
	}
	for _, pair := range [][2]string{{"csharp", "python"}, {"python", "vbnet"}, {"python", "java"}} {
		if err := validateInteractivePeerRuntimeIsolation(pair[0], pair[1]); err != nil {
			t.Fatalf("validateInteractivePeerRuntimeIsolation(%q, %q): %v", pair[0], pair[1], err)
		}
	}
}

func benchmarkSandboxIdentity() sandboxIdentity {
	if os.Geteuid() == 0 {
		return sandboxIdentity{uid: interactiveJudgeSandboxUID, gid: interactiveJudgeSandboxGID}
	}
	identity := sandboxIdentity{uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())}
	if groups, err := os.Getgroups(); err == nil {
		for _, gid := range groups {
			if gid != os.Getegid() {
				identity.gid = uint32(gid)
				break
			}
		}
	}
	return identity
}

func benchmarkInteractiveWorkspacePreparation(b *testing.B, separateRole bool) {
	identity := benchmarkSandboxIdentity()
	req := &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: b64("print('ok')\n"),
		}},
	}
	root := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		workDir, err := os.MkdirTemp(root, "interactive-bench-*")
		if err != nil {
			b.Fatal(err)
		}
		ws, err := prepareWorkspaceDirs(workDir)
		if err == nil {
			_, _, err = materializeFiles(ws, req)
		}
		if err == nil {
			if separateRole {
				err = hardenWorkspaceForIdentity(ws, "python3", identity)
			} else {
				err = os.Chmod(ws.RootDir, 0o755)
				for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
					if err == nil {
						err = os.Chown(dir, int(identity.uid), int(identity.gid))
					}
				}
				if err == nil {
					err = os.Chmod(ws.BoxDir, 0o777|os.ModeSticky)
				}
				for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
					if err == nil {
						err = os.Chmod(dir, 0o700)
					}
				}
			}
		}
		b.StopTimer()
		removeErr := os.RemoveAll(workDir)
		b.StartTimer()
		if err != nil {
			b.Fatal(err)
		}
		if removeErr != nil {
			b.Fatal(removeErr)
		}
	}
}

func BenchmarkInteractiveWorkspacePreparationSharedRole(b *testing.B) {
	benchmarkInteractiveWorkspacePreparation(b, false)
}

func BenchmarkInteractiveWorkspacePreparationSeparatedRole(b *testing.B) {
	benchmarkInteractiveWorkspacePreparation(b, true)
}

func benchmarkWorkspaceOwnershipHardening(b *testing.B, separateRole bool) {
	identity := benchmarkSandboxIdentity()
	ws, err := prepareWorkspaceDirs(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if separateRole {
			err = hardenWorkspaceForIdentity(ws, "python3", identity)
		} else {
			err = os.Chmod(ws.RootDir, 0o755)
			for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
				if err == nil {
					err = os.Chown(dir, int(identity.uid), int(identity.gid))
				}
			}
			if err == nil {
				err = os.Chmod(ws.BoxDir, 0o777|os.ModeSticky)
			}
			for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
				if err == nil {
					err = os.Chmod(dir, 0o700)
				}
			}
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWorkspaceOwnershipHardeningSharedRole(b *testing.B) {
	benchmarkWorkspaceOwnershipHardening(b, false)
}

func BenchmarkWorkspaceOwnershipHardeningSeparatedRole(b *testing.B) {
	benchmarkWorkspaceOwnershipHardening(b, true)
}

func TestRunInteractiveIOReportsInteractorWrongAnswer(t *testing.T) {
	requireSandboxSupport(t)

	contestant := `import sys
n = int(sys.stdin.readline())
print(n + 2, flush=True)
`
	interactor := `import sys
input_path, output_path, answer_path = sys.argv[1:4]
with open(input_path, "r", encoding="utf-8") as handle:
    n = int(handle.read().strip())
with open(answer_path, "r", encoding="utf-8") as handle:
    expected = handle.read().strip()
print(n, flush=True)
line = sys.stdin.readline().strip()
with open(output_path, "w", encoding="utf-8") as handle:
    handle.write(line + "\n")
if line != expected:
    sys.stderr.write(f"expected {expected}, got {line}\n")
    raise SystemExit(1)
`

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(contestant)),
		}},
		Stdin:          "41\n",
		ExpectedStdout: "42\n",
		Interactor: &model.InteractorSpec{
			Lang: "python",
			Binaries: []model.Binary{{
				Name:    "interactor.py",
				DataB64: base64.StdEncoding.EncodeToString([]byte(interactor)),
			}},
		},
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 128},
	}, Hooks{})

	if resp.Status != model.RunStatusWA {
		t.Fatalf("expected interactive wrong answer, got %+v", resp)
	}
	if resp.Score == nil || *resp.Score != 0 {
		t.Fatalf("expected wrong-answer interactive score 0, got %+v", resp.Score)
	}
	if !strings.Contains(resp.Reason, "expected 42, got 43") {
		t.Fatalf("expected interactor stderr in reason, got %+v", resp)
	}
	if strings.TrimSpace(resp.Stdout) != "43" {
		t.Fatalf("expected contestant protocol output on WA, got %+v", resp)
	}
}

func TestRunInteractiveUsesInteractorOutputLimit(t *testing.T) {
	requireSandboxSupport(t)

	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: b64("import time\ntime.sleep(10)\n"),
		}},
		Interactor: &model.InteractorSpec{
			Lang: "python",
			Binaries: []model.Binary{{
				Name:    "interactor.py",
				DataB64: b64("import sys\nsys.stderr.write('0123456789abcdef')\nraise SystemExit(3)\n"),
			}},
			Limits: &model.Limits{OutputBytes: 5},
		},
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 128, OutputBytes: 1024},
	}, Hooks{})
	if resp.Status != model.RunStatusRE {
		t.Fatalf("expected interactor failure, got %+v", resp)
	}
	if len(resp.Steps) != 2 || resp.Steps[1].Stderr != "01234" || !resp.Steps[1].StderrTruncated {
		t.Fatalf("interactor output should use its 5-byte cap, got %+v", resp.Steps)
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
