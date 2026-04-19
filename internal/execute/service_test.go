package execute

import (
	"bytes"
	"context"
	"encoding/base64"
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
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("AONOHAKO_UNSHARE_ENABLED", "0")

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

func TestRunFallsBackWhenUnshareFails(t *testing.T) {
	binDir := t.TempDir()
	unsharePath := filepath.Join(binDir, "unshare")
	script := "#!/bin/sh\necho 'unshare: cannot open /proc/self/uid_map: Permission denied' >&2\nexit 1\n"
	if err := os.WriteFile(unsharePath, []byte(script), 0o755); err != nil {
		t.Fatalf("write unshare shim: %v", err)
	}

	t.Setenv("PATH", binDir)
	t.Setenv("AONOHAKO_UNSHARE_ENABLED", "1")

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
		t.Fatalf("status=%q want=%q (stderr=%q reason=%q)", resp.Status, model.RunStatusAccepted, resp.Stderr, resp.Reason)
	}
}

func TestRunSkipsUnshareWhenDisabled(t *testing.T) {
	binDir := t.TempDir()
	unsharePath := filepath.Join(binDir, "unshare")
	script := "#!/bin/sh\necho 'unshare: should not run' >&2\nexit 1\n"
	if err := os.WriteFile(unsharePath, []byte(script), 0o755); err != nil {
		t.Fatalf("write unshare shim: %v", err)
	}

	t.Setenv("PATH", binDir)
	t.Setenv("AONOHAKO_UNSHARE_ENABLED", "0")

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
		t.Fatalf("status=%q want=%q (stderr=%q reason=%q)", resp.Status, model.RunStatusAccepted, resp.Stderr, resp.Reason)
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
