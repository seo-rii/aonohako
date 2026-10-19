package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

func TestExecuteRejectsDuplicateSidecarPathsBeforeAdmission(t *testing.T) {
	for _, transport := range []platform.ExecutionTransport{platform.ExecutionTransportEmbedded, platform.ExecutionTransportRemote} {
		for _, path := range []string{"x", "./x", "dir/../x"} {
			t.Run(string(transport)+"/"+path, func(t *testing.T) {
				cfg := configForTest(t)
				cfg.Execution.Platform.ExecutionTransport = transport
				var calls atomic.Int64
				s := NewWithServices(cfg, compileRunnerStub{}, executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
					calls.Add(1)
					return model.RunResponse{Status: model.RunStatusAccepted}
				}})
				server := httptest.NewServer(s.Handler())
				defer server.Close()
				req := model.RunRequest{
					Lang: "binary", Binaries: []model.Binary{{Name: "program", DataB64: "eA==", Mode: "exec"}},
					Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}, SidecarOutputs: []model.OutputFile{{Path: "x"}, {Path: path}},
				}
				body, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				response, err := http.Post(server.URL+"/execute", "application/json", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusBadRequest {
					t.Fatalf("status=%d response=%s", response.StatusCode, raw)
				}
				var envelope map[string]string
				if err := json.Unmarshal(raw, &envelope); err != nil {
					t.Fatalf("response should be a pre-SSE JSON error: %v", err)
				}
				if envelope["error"] != "invalid_request" || !strings.Contains(envelope["message"], "duplicate sidecar output path") {
					t.Fatalf("unexpected error: %s", raw)
				}
				if calls.Load() != 0 {
					t.Fatal("duplicate sidecar request reached execution")
				}
				if active, pending := s.queue.Snapshot(); active != 0 || pending != 0 || s.streams.Load() != 0 {
					t.Fatalf("duplicate request entered admission: active=%d pending=%d streams=%d", active, pending, s.streams.Load())
				}
			})
		}
	}
}
