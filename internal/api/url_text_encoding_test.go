package api

import (
	"bytes"
	"context"
	"encoding/base64"
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

func TestResolveTextURLRejectsInvalidUTF8(t *testing.T) {
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0xe9, '\n'})
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	inline, rawURL := "", "http://payload.example/answer"
	err := resolveTextURL(context.Background(), "expected_stdout", &inline, &rawURL, 1024)
	if err == nil || !strings.Contains(err.Error(), "expected_stdout_url") || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("resolveTextURL() error = %v, want field-specific UTF-8 rejection", err)
	}
	if inline != "" || rawURL != "http://payload.example/answer" {
		t.Fatalf("invalid text was installed into request: inline=%q rawURL=%q", inline, rawURL)
	}
}

func TestResolveTextURLPreservesValidUTF8(t *testing.T) {
	for _, payload := range []string{"", "답\x00\n", "replacement: \ufffd\n"} {
		t.Run(payload, func(t *testing.T) {
			assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, payload)
			}))
			defer assetServer.Close()
			setPayloadURLHTTPClientForTest(t, assetServer.URL)
			inline, rawURL := "", "http://payload.example/answer"
			if err := resolveTextURL(context.Background(), "expected_stdout", &inline, &rawURL, 1024); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(inline)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip string
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if inline != payload || roundTrip != payload || rawURL != "" {
				t.Fatalf("text changed during resolution or JSON transfer: %q, %q", inline, roundTrip)
			}
		})
	}
}

func TestResolveBase64URLPreservesInvalidUTF8Bytes(t *testing.T) {
	payload := []byte{0xe9, 0xff, 0}
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)
	inline, rawURL := "", "http://payload.example/binary"
	if err := resolveBase64URL(context.Background(), "binary", &inline, &rawURL, 1024, nil); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(inline)
	if err != nil || !bytes.Equal(decoded, payload) || rawURL != "" {
		t.Fatalf("binary URL payload changed: decoded=%x err=%v", decoded, err)
	}
}

func TestExecuteRejectsInvalidUTF8ExpectedURLBeforeAdmission(t *testing.T) {
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0xe9, '\n'})
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	for _, transport := range []platform.ExecutionTransport{platform.ExecutionTransportEmbedded, platform.ExecutionTransportRemote} {
		t.Run(string(transport), func(t *testing.T) {
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
				ExpectedStdoutURL: "http://payload.example/answer", Limits: model.Limits{TimeMs: 1000, MemoryMB: 64},
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
			if envelope["error"] != "invalid_payload_url" || !strings.Contains(envelope["message"], "valid UTF-8") {
				t.Fatalf("unexpected error: %s", raw)
			}
			if calls.Load() != 0 {
				t.Fatal("invalid UTF-8 answer reached execution")
			}
			if active, pending := s.queue.Snapshot(); active != 0 || pending != 0 || s.streams.Load() != 0 {
				t.Fatalf("rejected text entered admission: active=%d pending=%d streams=%d", active, pending, s.streams.Load())
			}
		})
	}
}
