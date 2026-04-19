package execute

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestRunReturnsTLEOnParentCancel(t *testing.T) {
	forceDirectMode(t)
	svc := New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	req := &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\nsleep 5\n"),
			Mode:    "exec",
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

func TestRunBlocksNetworkWhenDisabled(t *testing.T) {
	requireSandboxSupport(t)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name: "main.py",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"import socket\ns = socket.socket()\ns.settimeout(0.5)\ntry:\n    s.connect(('1.1.1.1', 53))\n    print('connected')\nexcept OSError:\n    print('blocked')\n",
			)),
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 256},
	}, Hooks{})

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("expected Accepted, got %+v", resp)
	}
}

func TestRunCannotReadHostPathOutsideSandbox(t *testing.T) {
	requireSandboxSupport(t)
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top-secret"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	script := fmt.Sprintf("#!/bin/sh\nif cat %q >/dev/null 2>&1; then echo leaked; else echo blocked; fi\n", secretPath)
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: base64.StdEncoding.EncodeToString([]byte(script)),
			Mode:    "exec",
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
		Lang: "binary",
		Binaries: []model.Binary{{
			Name: "run.sh",
			DataB64: base64.StdEncoding.EncodeToString([]byte(
				"#!/bin/sh\nif [ -c /dev/null ] && [ ! -e /dev/kmsg ]; then echo blocked; else echo leaked; fi\n",
			)),
			Mode: "exec",
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

func TestRunDirectModeRequiresDeclaredNetworkPolicy(t *testing.T) {
	t.Setenv("AONOHAKO_UNSHARE_ENABLED", "0")
	t.Setenv("AONOHAKO_NETWORK_POLICY", "")

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

	if resp.Status != model.RunStatusInitFail {
		t.Fatalf("expected init failure without declared network policy, got %+v", resp)
	}
	if !strings.Contains(strings.ToLower(resp.Reason), "network") {
		t.Fatalf("expected network-related reason, got %+v", resp)
	}
}

func TestRunDirectModeDoesNotRequireUnshareBinary(t *testing.T) {
	t.Setenv("AONOHAKO_UNSHARE_ENABLED", "0")
	t.Setenv("AONOHAKO_NETWORK_POLICY", "blocked")
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
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nsleep 0.2\n")),
			Mode:    "exec",
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
