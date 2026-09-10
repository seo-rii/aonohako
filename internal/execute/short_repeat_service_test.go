package execute

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestShortCaseBatchFreezesStdinURLAndSuppressesReplayHooks(t *testing.T) {
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads++
		fmt.Fprintf(w, "input %d", downloads)
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	req := &model.RunRequest{StdinURL: "http://payload.example/input", Stdin: "not URL input", Limits: model.Limits{WorkspaceBytes: 128}}
	runs, logs := 0, 0
	response := runShortCaseBatch(context.Background(), req, Hooks{OnLog: func(string, string) { logs++ }}, func(_ context.Context, frozen *model.RunRequest, stdin io.Reader, maxBytes int64, hooks Hooks) model.RunResponse {
		runs++
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
	if downloads != 1 || runs != 3 || logs != 1 || response.CPUTimeMs != 2 {
		t.Fatalf("downloads=%d runs=%d logs=%d response=%+v", downloads, runs, logs, response)
	}
	if req.StdinURL == "" || req.Stdin != "not URL input" {
		t.Fatal("mutated caller request")
	}
}

func TestShortCaseBatchRejectsOversizedChunkedInputBeforeExecution(t *testing.T) {
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
