package execute

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/timing"
)

func shortCaseTestWorkRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("AONOHAKO_WORK_ROOT", root)
	t.Cleanup(func() {
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Errorf("frozen input was not cleaned up: entries=%v err=%v", entries, err)
		}
	})
	return root
}

func TestShortCaseBatchFreezesStdinURLAndSuppressesReplayHooks(t *testing.T) {
	shortCaseTestWorkRoot(t)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "input %d", downloads.Add(1))
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	req := &model.RunRequest{StdinURL: "http://payload.example/input", Stdin: "not URL input", Limits: model.Limits{WorkspaceBytes: 128}}
	runs, logs := 0, 0
	var readers []*os.File
	response := runShortCaseBatch(context.Background(), req, Hooks{OnLog: func(string, string) { logs++ }}, func(_ context.Context, frozen *model.RunRequest, stdin io.Reader, maxBytes int64, hooks Hooks) model.RunResponse {
		runs++
		prepared, ok := stdin.(*sandboxPreparedStdin)
		if !ok || prepared.file == nil || frozen.Stdin != "" {
			t.Fatalf("URL input was not kept in a prepared file: %T", stdin)
		}
		readers = append(readers, prepared.file)
		if _, err := prepared.file.Write([]byte("mutate fixture")); err == nil {
			t.Fatal("a sample can mutate the frozen input")
		}
		data, err := io.ReadAll(stdin)
		if err != nil || string(data) != "input 1" || frozen.StdinURL != "" || maxBytes != 128 {
			t.Fatalf("run %d immutable input failed: %q, %v, limit=%d", runs, data, err, maxBytes)
		}
		if hooks.OnLog != nil {
			hooks.OnLog("stdout", "ok")
		}
		if runs > 1 && hooks.OnImage != nil {
			t.Fatal("replay image hook retained")
		}
		return model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: int64(runs), CPUTimeNs: uint64(runs) * 1_000_000}
	})
	if downloads.Load() != 1 || runs != 3 || logs != 1 || response.CPUTimeMs != 2 {
		t.Fatalf("downloads=%d runs=%d logs=%d response=%+v", downloads.Load(), runs, logs, response)
	}
	for _, file := range readers {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("sample input descriptor was not closed: %v", err)
		}
		if _, err := os.Stat(file.Name()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("frozen input was not removed: %v", err)
		}
	}
	if req.StdinURL == "" || req.Stdin != "not URL input" {
		t.Fatal("mutated caller request")
	}
}

func TestShortCaseBatchRejectsOversizedChunkedInputBeforeExecution(t *testing.T) {
	shortCaseTestWorkRoot(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		io.WriteString(w, "12345")
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	response := runShortCaseBatch(context.Background(), &model.RunRequest{StdinURL: "http://payload.example/input", Limits: model.Limits{WorkspaceBytes: 4}}, Hooks{}, func(context.Context, *model.RunRequest, io.Reader, int64, Hooks) model.RunResponse {
		t.Fatal("oversized payload reached execution")
		return model.RunResponse{}
	})
	if response.Status != model.RunStatusInitFail || !strings.Contains(response.Reason, "too large") {
		t.Fatalf("response=%+v", response)
	}
}

func TestShortCaseBatchStreamsLargeURLWithoutHeapBuffering(t *testing.T) {
	shortCaseTestWorkRoot(t)
	const inputBytes = 16 << 20
	chunk := []byte(strings.Repeat("fixture\n", (32<<10)/8))
	expected := sha256.New()
	for remaining := inputBytes; remaining > 0; remaining -= len(chunk) {
		expected.Write(chunk)
	}
	wantDigest := fmt.Sprintf("%x", expected.Sum(nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for remaining := inputBytes; remaining > 0; remaining -= len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	runs := 0
	response := runShortCaseBatch(context.Background(), &model.RunRequest{
		StdinURL: "http://payload.example/input", Limits: model.Limits{WorkspaceBytes: inputBytes},
	}, Hooks{}, func(_ context.Context, req *model.RunRequest, stdin io.Reader, _ int64, _ Hooks) model.RunResponse {
		runs++
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		allocated := after.TotalAlloc - before.TotalAlloc
		if allocated >= inputBytes/2 {
			t.Fatalf("16 MiB streamed fixture allocated %d bytes on the heap", allocated)
		}
		prepared, ok := stdin.(*sandboxPreparedStdin)
		if !ok || prepared.outsideWorkspaceBytes != inputBytes || req.Stdin != "" {
			t.Fatalf("shared input must remain file-backed and counted in workspace quota: %T", stdin)
		}
		digest := sha256.New()
		n, err := io.Copy(digest, stdin)
		if err != nil || n != inputBytes || fmt.Sprintf("%x", digest.Sum(nil)) != wantDigest {
			t.Fatalf("sample %d did not receive the complete unchanged fixture: bytes=%d err=%v", runs, n, err)
		}
		t.Logf("sample %d: input=%d bytes, cumulative heap allocation=%d bytes", runs, inputBytes, allocated)
		return model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: 1, CPUTimeNs: 1_000_000}
	})
	if response.Status != model.RunStatusAccepted || runs != 3 {
		t.Fatalf("runs=%d response=%+v", runs, response)
	}
}

func TestShortCaseBatchCleansFrozenInputOnCancellation(t *testing.T) {
	for _, duringDownload := range []bool{true, false} {
		t.Run(fmt.Sprintf("during-download=%t", duringDownload), func(t *testing.T) {
			shortCaseTestWorkRoot(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "fixture")
				if duringDownload {
					w.(http.Flusher).Flush()
					cancel()
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			setStdinURLHTTPClientForTest(t, server.URL)
			runs := 0
			response := runShortCaseBatch(ctx, &model.RunRequest{StdinURL: "http://payload.example/input"}, Hooks{}, func(context.Context, *model.RunRequest, io.Reader, int64, Hooks) model.RunResponse {
				runs++
				cancel()
				return model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: 1}
			})
			wantRuns := 1
			if duringDownload {
				wantRuns = 0
			}
			if response.Status != model.RunStatusInitFail || !strings.Contains(response.Reason, "canceled") || runs != wantRuns {
				t.Fatalf("runs=%d response=%+v", runs, response)
			}
		})
	}
}

func TestShortCaseBatchCleansFrozenInputAfterFailedReplay(t *testing.T) {
	shortCaseTestWorkRoot(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "fixture")
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	runs := 0
	response := runShortCaseBatch(context.Background(), &model.RunRequest{StdinURL: "http://payload.example/input"}, Hooks{}, func(context.Context, *model.RunRequest, io.Reader, int64, Hooks) model.RunResponse {
		runs++
		if runs == 2 {
			return model.RunResponse{Status: model.RunStatusWA}
		}
		return model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: 1}
	})
	if response.Status != model.RunStatusWA || runs != 2 {
		t.Fatalf("runs=%d response=%+v", runs, response)
	}
}

func TestRunSandboxPreparedInputReservesWorkspaceQuota(t *testing.T) {
	requireSandboxSupport(t)
	ws, err := prepareWorkspaceDirs(sandboxAccessibleTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	path, err := writeTempFileFromReader(t.TempDir(), "stdin-*", strings.NewReader(strings.Repeat("x", 4096)), 4096)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result := runCommandWithSandbox(context.Background(), ws, []string{"/bin/true"},
		&model.RunRequest{Lang: "binary", Limits: model.Limits{TimeMs: 1000, MemoryMB: 256, WorkspaceBytes: 1024}},
		&sandboxPreparedStdin{file: file, outsideWorkspaceBytes: 4096}, 4096, Hooks{}, 1024, config.DefaultRuntimeTuningConfig(), "", timing.CPUNormalizer{})
	if result.Status != model.RunStatusWLE || !strings.HasPrefix(result.VerdictSource, "workspace_bytes") {
		t.Fatalf("frozen input bypassed workspace quota: %+v", result)
	}
}

func TestShortCaseBatchSkipsActualImageOutput(t *testing.T) {
	runs, images := 0, 0
	response := runShortCaseBatch(context.Background(), &model.RunRequest{}, Hooks{OnImage: func(string, string, int64) { images++ }}, func(_ context.Context, _ *model.RunRequest, _ io.Reader, _ int64, hooks Hooks) model.RunResponse {
		runs++
		hooks.OnImage("image/png", "payload", 0)
		return model.RunResponse{Status: model.RunStatusAccepted, CPUTimeNs: 1_000_000, CPUTimeMs: 1}
	})
	if runs != 1 || images != 1 || response.CPUTimeSampling != nil {
		t.Fatalf("runs=%d images=%d response=%+v", runs, images, response)
	}
}
