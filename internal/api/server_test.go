package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aonohako/internal/compile"
	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/remoteio"
)

type executeRunnerStub struct {
	run func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse
}

func (s executeRunnerStub) Run(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
	return s.run(ctx, req, hooks)
}

type compileRunnerStub struct {
	run func(context.Context, *model.CompileRequest) model.CompileResponse
}

func (s compileRunnerStub) Run(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
	return s.run(ctx, req)
}

type blockingBody struct {
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

type readCountingBody struct {
	reads int
}

func (b *readCountingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must not be read")
}

func (*readCountingBody) Close() error { return nil }

func newBlockingBody() *blockingBody {
	return &blockingBody{started: make(chan struct{}), unblock: make(chan struct{})}
}

func (b *blockingBody) Read(_ []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.unblock
	return 0, io.EOF
}

func (b *blockingBody) Close() error {
	select {
	case <-b.unblock:
	default:
		close(b.unblock)
	}
	return nil
}

func platformPrincipalNonceForTest(id int) string {
	return fmt.Sprintf("%032x", id)
}

func platformPrincipalSignatureForTest(secret, method, requestURI, principal, timestamp, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(method + "\n" + requestURI + "\n" + principal + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])))
	return "v4=" + hex.EncodeToString(mac.Sum(nil))
}

func TestConstantTimeEqualRequiresDigestAndLengthMatch(t *testing.T) {
	if !constantTimeEqual("same-token", "same-token") {
		t.Fatalf("constantTimeEqual should accept identical values")
	}
	if constantTimeEqual("same-token", "same-token-extra") {
		t.Fatalf("constantTimeEqual should reject different lengths")
	}
	if constantTimeEqual("same-token", "xxxx-token") {
		t.Fatalf("constantTimeEqual should reject different values")
	}
}

func TestDecodeJSONBodyReleasesRawBodyReference(t *testing.T) {
	body := executePayload(t)
	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()

	var decoded model.RunRequest
	if err := decodeJSONBody(rr, req, &decoded); err != nil {
		t.Fatalf("decodeJSONBody returned error: %v", err)
	}
	if decoded.Lang != "binary" {
		t.Fatalf("decoded lang = %q, want binary", decoded.Lang)
	}
	if req.Body != http.NoBody {
		t.Fatalf("decodeJSONBody should replace request body with http.NoBody")
	}
	if req.ContentLength != 0 {
		t.Fatalf("content length = %d, want 0", req.ContentLength)
	}
}

func TestDecodeExecuteCaptureLimitsPreservesExplicitZero(t *testing.T) {
	body := []byte(`{"capture_limits":{"stdout_bytes":0,"stderr_bytes":1024}}`)
	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	var decoded model.RunRequest
	if err := decodeJSONBody(rr, req, &decoded); err != nil {
		t.Fatalf("decodeJSONBody returned error: %v", err)
	}
	if decoded.CaptureLimits == nil ||
		decoded.CaptureLimits.StdoutBytes == nil ||
		*decoded.CaptureLimits.StdoutBytes != 0 ||
		decoded.CaptureLimits.StderrBytes == nil ||
		*decoded.CaptureLimits.StderrBytes != 1024 {
		t.Fatalf("decoded capture_limits = %+v, want explicit stdout 0 and stderr 1024", decoded.CaptureLimits)
	}
}

func TestPreSSEErrorsUseJSONEnvelope(t *testing.T) {
	t.Run("method mismatch", func(t *testing.T) {
		s := newServerForTest(t)
		req := httptest.NewRequest(http.MethodGet, "/execute", nil)
		resp := httptest.NewRecorder()

		s.Handler().ServeHTTP(resp, req)

		if resp.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusMethodNotAllowed)
		}
		if got := resp.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("content type = %q, want application/json", got)
		}
		var payload map[string]string
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("response should be JSON: %v; body=%q", err, resp.Body.String())
		}
		if payload["error"] != "method_not_allowed" || payload["message"] != "POST only" {
			t.Fatalf("unexpected error payload: %#v", payload)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		s := newServerForTest(t)
		req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader("{"))
		resp := httptest.NewRecorder()

		s.Handler().ServeHTTP(resp, req)

		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
		}
		var payload map[string]string
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("response should be JSON: %v; body=%q", err, resp.Body.String())
		}
		if payload["error"] != "invalid_json" || !strings.Contains(payload["message"], "invalid json") {
			t.Fatalf("unexpected error payload: %#v", payload)
		}
	})

	t.Run("auth rejection", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthBearer, BearerToken: "secret"}
		s := NewWithServices(cfg, compile.New(), execute.New())
		req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(executePayload(t)))
		resp := httptest.NewRecorder()

		s.Handler().ServeHTTP(resp, req)

		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusUnauthorized)
		}
		var payload map[string]string
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("response should be JSON: %v; body=%q", err, resp.Body.String())
		}
		if payload["error"] != "unauthorized" {
			t.Fatalf("unexpected error payload: %#v", payload)
		}
	})
}

func TestExecuteQueueOverflowReturns429(t *testing.T) {
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		time.Sleep(2 * time.Second)
		return model.RunResponse{Status: model.RunStatusAccepted}
	}}
	h := s.Handler()
	ts := httptest.NewServer(h)
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("import time\ntime.sleep(2)\n"))
	payload := map[string]any{
		"lang":     "python",
		"binaries": []map[string]any{{"name": "main.py", "data_b64": script}},
		"limits":   map[string]any{"time_ms": 5000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)

	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("req1 failed: %v", err)
	}
	defer resp1.Body.Close()

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("req2 failed: %v", err)
	}
	defer resp2.Body.Close()

	time.Sleep(100 * time.Millisecond)

	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("req3 failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp3.StatusCode)
	}
}

func TestExecuteResolvesPayloadURLsBeforeRunner(t *testing.T) {
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stdin":
			_, _ = w.Write([]byte("hello\n"))
		case "/expected":
			_, _ = w.Write([]byte("world\n"))
		case "/prefix":
			_, _ = w.Write([]byte("DECODE\n"))
		case "/runner":
			_, _ = w.Write([]byte("#!/bin/sh\ncat\n"))
		case "/checker":
			_, _ = w.Write([]byte("#!/bin/sh\nexit 0\n"))
		case "/interactor":
			_, _ = w.Write([]byte("#!/bin/sh\nexit 0\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	tests := []struct {
		name    string
		payload map[string]any
		check   func(*testing.T, *model.RunRequest)
	}{
		{
			name: "legacy run and spj",
			payload: map[string]any{
				"lang": "binary",
				"binaries": []map[string]any{{
					"name":     "run.sh",
					"data_url": assetServer.URL + "/runner",
					"mode":     "exec",
				}},
				"stdin_url":           assetServer.URL + "/stdin",
				"expected_stdout_url": assetServer.URL + "/expected",
				"limits":              map[string]any{"time_ms": 1000, "memory_mb": 64},
				"spj": map[string]any{
					"binary": map[string]any{
						"name":     "checker",
						"data_url": assetServer.URL + "/checker",
						"mode":     "exec",
					},
					"lang": "binary",
				},
			},
			check: func(t *testing.T, req *model.RunRequest) {
				t.Helper()
				if req.Stdin != "" || req.StdinURL != assetServer.URL+"/stdin" || req.ExpectedStdout != "world\n" {
					t.Fatalf("unexpected text fields: stdin=%q stdin_url=%q expected=%q", req.Stdin, req.StdinURL, req.ExpectedStdout)
				}
				if got, _ := base64.StdEncoding.DecodeString(req.Binaries[0].DataB64); string(got) != "#!/bin/sh\ncat\n" {
					t.Fatalf("binary url was not resolved: %q", string(got))
				}
				if req.Binaries[0].DataURL != "" {
					t.Fatalf("resolved binary data_url was retained: %q", req.Binaries[0].DataURL)
				}
				if req.SPJ == nil || req.SPJ.Binary == nil {
					t.Fatalf("spj was not preserved: %+v", req.SPJ)
				}
				if got, _ := base64.StdEncoding.DecodeString(req.SPJ.Binary.DataB64); string(got) != "#!/bin/sh\nexit 0\n" {
					t.Fatalf("spj binary url was not resolved: %q", string(got))
				}
				if req.SPJ.Binary.DataURL != "" {
					t.Fatalf("resolved spj data_url was retained: %q", req.SPJ.Binary.DataURL)
				}
			},
		},
		{
			name: "interactor binary",
			payload: map[string]any{
				"lang": "binary",
				"binaries": []map[string]any{{
					"name":     "run.sh",
					"data_url": assetServer.URL + "/runner",
					"mode":     "exec",
				}},
				"stdin_url":           assetServer.URL + "/stdin",
				"expected_stdout_url": assetServer.URL + "/expected",
				"limits":              map[string]any{"time_ms": 1000, "memory_mb": 64},
				"interactor": map[string]any{
					"lang": "binary",
					"binaries": []map[string]any{{
						"name":     "interactor",
						"data_url": assetServer.URL + "/interactor",
						"mode":     "exec",
					}},
				},
			},
			check: func(t *testing.T, req *model.RunRequest) {
				t.Helper()
				if req.Stdin != "" || req.StdinURL != assetServer.URL+"/stdin" || req.ExpectedStdout != "world\n" {
					t.Fatalf("unexpected text fields: stdin=%q stdin_url=%q expected=%q", req.Stdin, req.StdinURL, req.ExpectedStdout)
				}
				if req.Interactor == nil || len(req.Interactor.Binaries) != 1 {
					t.Fatalf("interactor was not preserved: %+v", req.Interactor)
				}
				if got, _ := base64.StdEncoding.DecodeString(req.Interactor.Binaries[0].DataB64); string(got) != "#!/bin/sh\nexit 0\n" {
					t.Fatalf("interactor binary url was not resolved: %q", string(got))
				}
				if req.Interactor.Binaries[0].DataURL != "" {
					t.Fatalf("resolved interactor data_url was retained: %q", req.Interactor.Binaries[0].DataURL)
				}
			},
		},
		{
			name: "step pipeline",
			payload: map[string]any{
				"programs": []map[string]any{
					{
						"id":   "encode",
						"lang": "binary",
						"binaries": []map[string]any{{
							"name":     "encode.sh",
							"data_url": assetServer.URL + "/runner",
							"mode":     "exec",
						}},
					},
					{
						"id":   "decode",
						"lang": "binary",
						"binaries": []map[string]any{{
							"name":     "decode.sh",
							"data_url": assetServer.URL + "/runner",
							"mode":     "exec",
						}},
					},
				},
				"steps": []map[string]any{
					{
						"id":         "encode",
						"program_id": "encode",
						"stdin_url":  assetServer.URL + "/stdin",
						"limits":     map[string]any{"time_ms": 1000, "memory_mb": 64},
						"handoff":    map[string]any{"id": "encoded"},
					},
					{
						"id":         "decode",
						"program_id": "decode",
						"stdin_parts": []map[string]any{
							{"type": "text", "data_url": assetServer.URL + "/prefix"},
							{"type": "handoff", "id": "encoded"},
						},
						"limits": map[string]any{"time_ms": 1000, "memory_mb": 64},
					},
				},
				"expected_stdout_url": assetServer.URL + "/expected",
			},
			check: func(t *testing.T, req *model.RunRequest) {
				t.Helper()
				if req.Steps[0].Stdin != "" || req.Steps[0].StdinURL != assetServer.URL+"/stdin" || req.ExpectedStdout != "world\n" {
					t.Fatalf("unexpected step text fields: step=%q step_url=%q expected=%q", req.Steps[0].Stdin, req.Steps[0].StdinURL, req.ExpectedStdout)
				}
				if len(req.Steps[1].StdinParts) != 2 || req.Steps[1].StdinParts[0].Data != "" || req.Steps[1].StdinParts[0].DataURL != assetServer.URL+"/prefix" {
					t.Fatalf("unexpected stdin_parts url state: %+v", req.Steps[1].StdinParts)
				}
				if got, _ := base64.StdEncoding.DecodeString(req.Programs[1].Binaries[0].DataB64); string(got) != "#!/bin/sh\ncat\n" {
					t.Fatalf("program binary url was not resolved: %q", string(got))
				}
				if req.Programs[0].Binaries[0].DataURL != "" || req.Programs[1].Binaries[0].DataURL != "" {
					t.Fatalf("resolved program data_url was retained: %+v", req.Programs)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			s := NewWithServices(configForTest(t), compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
				called = true
				tc.check(t, req)
				return model.RunResponse{Status: model.RunStatusAccepted}
			}})
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()

			body, _ := json.Marshal(tc.payload)
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, body=%s", resp.StatusCode, string(raw))
			}
			events := readSSEEvents(resp.Body, t)
			if len(events) == 0 || events[len(events)-1].Name != "result" {
				t.Fatalf("missing result event: %+v", events)
			}
			if !called {
				t.Fatalf("runner was not called")
			}
		})
	}
}

func TestExecuteRejectsConflictingPayloadURL(t *testing.T) {
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("execute runner should not be called for conflicting payload urls")
		return model.RunResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":      "binary",
		"binaries":  []map[string]any{{"name": "run.sh", "data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n")), "mode": "exec"}},
		"stdin":     "inline",
		"stdin_url": ts.URL + "/stdin",
		"limits":    map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for conflicting stdin url, got %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "cannot combine inline content with url") {
		t.Fatalf("unexpected response: %s", string(raw))
	}
}

func TestExecuteActiveStreamOverflowReturns429(t *testing.T) {
	cfg := configForTest(t)
	cfg.MaxPendingQueue = 8
	cfg.MaxActiveStreams = 1
	unblock := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(unblock)
		}
	}()
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		<-unblock
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := executePayload(t)
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp1.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", resp2.StatusCode)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp2.Body).Decode(&payload); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if payload["error"] != "stream_limit_exceeded" {
		t.Fatalf("error = %q, want stream_limit_exceeded", payload["error"])
	}
	close(unblock)
	released = true
	_, _ = io.Copy(io.Discard, resp1.Body)
}

func TestExecuteDoesNotAcquireStreamBeforeBodyDecode(t *testing.T) {
	cfg := configForTest(t)
	cfg.MaxActiveStreams = 1
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("runner should not be called for an undecoded body")
		return model.RunResponse{}
	}})

	body := newBlockingBody()
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}()

	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatalf("handler did not start reading request body")
	}
	time.Sleep(50 * time.Millisecond)
	if active := s.streams.Load(); active != 0 {
		t.Fatalf("active streams = %d while body decode is blocked, want 0", active)
	}
	_ = body.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("handler did not finish after body unblock")
	}
}

func TestHTTPServerReadTimeoutBoundsSlowUploads(t *testing.T) {
	cfg := configForTest(t)
	cfg.BodyReadTimeout = 150 * time.Millisecond
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("runner should not be called for a slow incomplete body")
		return model.RunResponse{}
	}})
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       cfg.BodyReadTimeout,
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("server.Serve returned %v", err)
			}
		case <-time.After(time.Second):
			t.Fatalf("server did not stop")
		}
	}()

	body := executePayload(t)
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "POST /execute HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", listener.Addr().String(), len(body)); err != nil {
		t.Fatalf("write headers: %v", err)
	}
	if _, err := conn.Write(body[:1]); err != nil {
		t.Fatalf("write first byte: %v", err)
	}
	time.Sleep(2 * cfg.BodyReadTimeout)
	_, _ = conn.Write(body[1:])
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		if s.streams.Load() != 0 {
			t.Fatalf("active streams = %d after slow upload timeout", s.streams.Load())
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("slow upload status = %d, want error response", resp.StatusCode)
	}
	if s.streams.Load() != 0 {
		t.Fatalf("active streams = %d after slow upload timeout", s.streams.Load())
	}
}

func TestExecuteRejectsRequestControlledNetworkWhenPolicyDisabled(t *testing.T) {
	cfg := configForTest(t)
	cfg.AllowRequestNetwork = false
	called := false
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		called = true
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	var payload map[string]any
	if err := json.Unmarshal(executePayload(t), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	payload["enable_network"] = true
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if called {
		t.Fatalf("runner should not be called when enable_network is rejected by policy")
	}
}

func TestExecuteAcceptsTwoStepPipelineShape(t *testing.T) {
	cfg := configForTest(t)
	called := false
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		called = true
		if len(req.Programs) != 2 || len(req.Steps) != 2 {
			t.Fatalf("unexpected two-step payload: %+v", req)
		}
		return model.RunResponse{
			Status: model.RunStatusAccepted,
			Steps: []model.StepResult{
				{ID: "encode", Status: model.RunStatusAccepted, HandoffBytes: 8},
				{ID: "decode", Status: model.RunStatusAccepted},
			},
		}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body, _ := json.Marshal(twoStepPayloadForTest(false))
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, string(raw))
	}
	events := readSSEEvents(resp.Body, t)
	if len(events) == 0 || events[len(events)-1].Name != "result" {
		t.Fatalf("missing result event: %+v", events)
	}
	if status, _ := events[len(events)-1].JSON["status"].(string); status != model.RunStatusAccepted {
		t.Fatalf("status = %q, want Accepted", status)
	}
	if !called {
		t.Fatalf("runner was not called")
	}
}

func TestExecuteRejectsTwoStepProgramNetworkWhenPolicyDisabled(t *testing.T) {
	cfg := configForTest(t)
	cfg.AllowRequestNetwork = false
	called := false
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		called = true
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body, _ := json.Marshal(twoStepPayloadForTest(true))
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, string(raw))
	}
	if called {
		t.Fatalf("runner should not be called when a step program requests network")
	}
}

func twoStepPayloadForTest(enableNetwork bool) map[string]any {
	return map[string]any{
		"programs": []map[string]any{
			{
				"id":             "encoder",
				"lang":           "binary",
				"enable_network": enableNetwork,
				"binaries":       []map[string]any{{"name": "encode.sh", "data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\ncat\n")), "mode": "exec"}},
			},
			{
				"id":       "decoder",
				"lang":     "binary",
				"binaries": []map[string]any{{"name": "decode.sh", "data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\ncat\n")), "mode": "exec"}},
			},
		},
		"steps": []map[string]any{
			{
				"id":         "encode",
				"program_id": "encoder",
				"stdin":      "payload\n",
				"limits":     map[string]any{"time_ms": 1000, "memory_mb": 64},
				"handoff":    map[string]any{"id": "encoded", "from": "stdout", "max_bytes": 1024},
			},
			{
				"id":         "decode",
				"program_id": "decoder",
				"stdin_from": "encoded",
				"limits":     map[string]any{"time_ms": 1000, "memory_mb": 64},
			},
		},
		"expected_stdout": "payload\n",
	}
}

func TestExecutePrincipalStreamOverflowReturns429(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform}
	cfg.MaxActiveRuns = 4
	cfg.MaxPendingQueue = 8
	cfg.MaxActiveStreams = 8
	cfg.MaxPrincipalStreams = 1
	unblock := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(unblock)
		}
	}()
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		<-unblock
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := executePayload(t)
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Aonohako-Principal", "alice")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp1.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Aonohako-Principal", "bob")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second request status = %d, want 200 for a different principal", resp2.StatusCode)
	}

	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-Aonohako-Principal", "alice")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("third request failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third request status = %d, want 429", resp3.StatusCode)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp3.Body).Decode(&payload); err != nil {
		t.Fatalf("decode third response: %v", err)
	}
	if payload["error"] != "principal_stream_limit_exceeded" {
		t.Fatalf("error = %q, want principal_stream_limit_exceeded", payload["error"])
	}
	close(unblock)
	released = true
	_, _ = io.Copy(io.Discard, resp1.Body)
	_, _ = io.Copy(io.Discard, resp2.Body)
}

func TestExecutePrincipalRequestRateOverflowReturns429(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform}
	cfg.MaxActiveRuns = 4
	cfg.MaxPendingQueue = 8
	cfg.MaxActiveStreams = 8
	cfg.MaxPrincipalStreams = 8
	cfg.MaxPrincipalRequestsPerMinute = 1
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := executePayload(t)
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Aonohako-Principal", "alice")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp1.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Aonohako-Principal", "alice")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", resp2.StatusCode)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp2.Body).Decode(&payload); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if payload["error"] != "principal_rate_limited" {
		t.Fatalf("error = %q, want principal_rate_limited", payload["error"])
	}

	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-Aonohako-Principal", "bob")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("third request failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("third request status = %d, want 200 for a different principal", resp3.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp3.Body)
}

func TestPrincipalRequestRateCleanupDropsStaleWindows(t *testing.T) {
	cfg := configForTest(t)
	cfg.MaxPrincipalRequestsPerMinute = 10
	s := NewWithServices(cfg, compile.New(), execute.New())
	now := time.Unix(200, 0)
	s.principalRates = map[string]principalRateWindow{
		"old":   {start: now.Add(-2 * time.Minute), count: 10},
		"fresh": {start: now.Add(-30 * time.Second), count: 1},
	}
	s.rateLastCleanup = now.Add(-2 * time.Minute)

	if !s.allowPrincipalRequest("new", now) {
		t.Fatalf("new principal should be allowed")
	}
	if _, ok := s.principalRates["old"]; ok {
		t.Fatalf("stale principal rate window was not cleaned up")
	}
	if _, ok := s.principalRates["fresh"]; !ok {
		t.Fatalf("fresh principal rate window should remain")
	}
	if _, ok := s.principalRates["new"]; !ok {
		t.Fatalf("new principal rate window should be recorded")
	}
}

func TestPlatformAuthIgnoresForwardedPrincipalHeaders(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform}
	cfg.MaxActiveRuns = 4
	cfg.MaxPendingQueue = 8
	cfg.MaxActiveStreams = 8
	cfg.MaxPrincipalStreams = 8
	cfg.MaxPrincipalRequestsPerMinute = 1
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := executePayload(t)
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Forwarded-Email", "alice@example.test")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp1.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp1.Body)

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Forwarded-Email", "bob@example.test")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429 because forwarded identity headers are ignored", resp2.StatusCode)
	}
}

func TestPlatformAuthRequiresValidPrincipalSignatureWhenConfigured(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform, PlatformPrincipalHMACSecret: "platform-secret"}
	cfg.MaxActiveRuns = 4
	cfg.MaxPendingQueue = 8
	cfg.MaxActiveStreams = 8
	cfg.MaxPrincipalStreams = 8
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := executePayload(t)
	now := time.Now().UTC()
	validTimestamp := now.Format(time.RFC3339)
	staleTimestamp := now.Add(-10 * time.Minute).Format(time.RFC3339)
	for _, tc := range []struct {
		name      string
		principal string
		timestamp string
		nonce     string
		url       string
		signature string
		want      int
	}{
		{name: "missing signature", principal: "alice", want: http.StatusUnauthorized},
		{name: "missing timestamp", principal: "alice", signature: "v2=bad", want: http.StatusUnauthorized},
		{name: "missing nonce", principal: "alice", timestamp: validTimestamp, signature: platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", "alice", validTimestamp, platformPrincipalNonceForTest(1), body), want: http.StatusUnauthorized},
		{name: "malformed nonce", principal: "alice", timestamp: validTimestamp, nonce: "bad", signature: "v4=bad", want: http.StatusUnauthorized},
		{name: "stale timestamp", principal: "alice", timestamp: staleTimestamp, nonce: platformPrincipalNonceForTest(2), signature: platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", "alice", staleTimestamp, platformPrincipalNonceForTest(2), body), want: http.StatusUnauthorized},
		{name: "legacy principal only signature", principal: "alice", timestamp: validTimestamp, signature: "v1=bad", want: http.StatusUnauthorized},
		{name: "legacy bodyless signature", principal: "alice", timestamp: validTimestamp, signature: "v2=bad", want: http.StatusUnauthorized},
		{name: "legacy replayable signature", principal: "alice", timestamp: validTimestamp, nonce: platformPrincipalNonceForTest(3), signature: "v3=" + strings.Repeat("0", sha256.Size*2), want: http.StatusUnauthorized},
		{name: "bad signature", principal: "alice", timestamp: validTimestamp, nonce: platformPrincipalNonceForTest(4), signature: "v4=bad", want: http.StatusUnauthorized},
		{name: "wrong path signature", principal: "alice", timestamp: validTimestamp, nonce: platformPrincipalNonceForTest(5), signature: platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/compile", "alice", validTimestamp, platformPrincipalNonceForTest(5), body), want: http.StatusUnauthorized},
		{name: "wrong query signature", principal: "alice", timestamp: validTimestamp, nonce: platformPrincipalNonceForTest(6), url: "/execute?trace=1", signature: platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", "alice", validTimestamp, platformPrincipalNonceForTest(6), body), want: http.StatusUnauthorized},
		{name: "valid query signature", principal: "alice", timestamp: validTimestamp, nonce: platformPrincipalNonceForTest(7), url: "/execute?trace=1", signature: platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute?trace=1", "alice", validTimestamp, platformPrincipalNonceForTest(7), body), want: http.StatusOK},
		{name: "valid signature", principal: "alice", timestamp: validTimestamp, nonce: platformPrincipalNonceForTest(8), signature: platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", "alice", validTimestamp, platformPrincipalNonceForTest(8), body), want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.url
			if path == "" {
				path = "/execute"
			}
			req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.principal != "" {
				req.Header.Set(platformPrincipalHeader, tc.principal)
			}
			if tc.timestamp != "" {
				req.Header.Set(platformPrincipalTimestampHeader, tc.timestamp)
			}
			if tc.nonce != "" {
				req.Header.Set(platformPrincipalNonceHeader, tc.nonce)
			}
			if tc.signature != "" {
				req.Header.Set(platformPrincipalSignatureHeader, tc.signature)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
		})
	}
}

func TestPlatformAuthEnforcesTrustedProxyCIDRsForSignedHeaders(t *testing.T) {
	body := executePayload(t)
	for _, tc := range []struct {
		name  string
		cidrs []string
		want  int
	}{
		{name: "trusted source", cidrs: []string{"127.0.0.1/32", "::1/128"}, want: http.StatusOK},
		{name: "untrusted source", cidrs: []string{"192.0.2.0/24"}, want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configForTest(t)
			cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform, PlatformPrincipalHMACSecret: "platform-secret"}
			cfg.TrustedPlatformHeaders = true
			cfg.TrustedPlatformHeaderCIDRs = tc.cidrs
			cfg.MaxActiveRuns = 4
			cfg.MaxPendingQueue = 8
			cfg.MaxActiveStreams = 8
			cfg.MaxPrincipalStreams = 8
			s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
				return model.RunResponse{Status: model.RunStatusAccepted}
			}})
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()

			timestamp := time.Now().UTC().Format(time.RFC3339)
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(platformPrincipalHeader, "alice")
			req.Header.Set(platformPrincipalTimestampHeader, timestamp)
			nonce := platformPrincipalNonceForTest(1)
			req.Header.Set(platformPrincipalNonceHeader, nonce)
			req.Header.Set(platformPrincipalSignatureHeader, platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", "alice", timestamp, nonce, body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
		})
	}
}

func TestPlatformAuthRejectsCheapMetadataFailuresBeforeBodyHashing(t *testing.T) {
	validTimestamp := time.Now().UTC().Format(time.RFC3339)
	staleTimestamp := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	for _, tc := range []struct {
		name       string
		remoteAddr string
		timestamp  string
		cidrs      []string
	}{
		{name: "untrusted source", remoteAddr: "198.51.100.1:1234", timestamp: validTimestamp, cidrs: []string{"192.0.2.0/24"}},
		{name: "stale timestamp", remoteAddr: "127.0.0.1:1234", timestamp: staleTimestamp, cidrs: []string{"127.0.0.1/32"}},
		{name: "malformed timestamp", remoteAddr: "127.0.0.1:1234", timestamp: "not-a-time", cidrs: []string{"127.0.0.1/32"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configForTest(t)
			cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform, PlatformPrincipalHMACSecret: "platform-secret"}
			cfg.TrustedPlatformHeaders = true
			cfg.TrustedPlatformHeaderCIDRs = tc.cidrs
			s := NewWithServices(cfg, compile.New(), execute.New())
			body := &readCountingBody{}
			req := httptest.NewRequest(http.MethodPost, "/execute", body)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set(platformPrincipalHeader, "alice")
			req.Header.Set(platformPrincipalTimestampHeader, tc.timestamp)
			req.Header.Set(platformPrincipalNonceHeader, platformPrincipalNonceForTest(1))
			req.Header.Set(platformPrincipalSignatureHeader, "v4="+strings.Repeat("0", sha256.Size*2))
			rr := httptest.NewRecorder()

			s.Handler().ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
			}
			if body.reads != 0 {
				t.Fatalf("request body was read %d times before cheap authentication rejection", body.reads)
			}
		})
	}
}

func TestPlatformAuthRejectsBodySubstitutionReplay(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform, PlatformPrincipalHMACSecret: "platform-secret"}
	cfg.MaxActiveRuns = 4
	cfg.MaxPendingQueue = 8
	cfg.MaxActiveStreams = 8
	cfg.MaxPrincipalStreams = 8
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	signedBody := executePayload(t)
	mutatedBody := append([]byte{}, signedBody...)
	mutatedBody = append(mutatedBody, ' ')
	timestamp := time.Now().UTC().Format(time.RFC3339)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(mutatedBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(platformPrincipalHeader, "alice")
	req.Header.Set(platformPrincipalTimestampHeader, timestamp)
	nonce := platformPrincipalNonceForTest(1)
	req.Header.Set(platformPrincipalNonceHeader, nonce)
	req.Header.Set(platformPrincipalSignatureHeader, platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", "alice", timestamp, nonce, signedBody))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}

func TestPlatformAuthRejectsReusedNonce(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform, PlatformPrincipalHMACSecret: "platform-secret"}
	cfg.MaxActiveRuns = 4
	cfg.MaxPendingQueue = 8
	cfg.MaxActiveStreams = 8
	cfg.MaxPrincipalStreams = 8
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := executePayload(t)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	nonce := platformPrincipalNonceForTest(1)
	signature := platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", "alice", timestamp, nonce, body)
	send := func() int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(platformPrincipalHeader, "alice")
		req.Header.Set(platformPrincipalTimestampHeader, timestamp)
		req.Header.Set(platformPrincipalNonceHeader, nonce)
		req.Header.Set(platformPrincipalSignatureHeader, signature)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	if got := send(); got != http.StatusOK {
		t.Fatalf("first request status = %d, want %d", got, http.StatusOK)
	}
	if got := send(); got != http.StatusUnauthorized {
		t.Fatalf("replayed request status = %d, want %d", got, http.StatusUnauthorized)
	}
}

func TestPlatformAuthLimitsConcurrentBodyHashing(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform, PlatformPrincipalHMACSecret: "platform-secret"}
	cfg.MaxActiveRuns = 4
	cfg.MaxPendingQueue = 8
	cfg.MaxActiveStreams = 8
	cfg.PlatformBodyHashConcurrency = 1
	cfg.MaxPrincipalStreams = 8
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	releaseHashSlot, ok := s.acquirePlatformBodyHashSlot()
	if !ok {
		t.Fatalf("failed to occupy platform body hash slot")
	}
	defer releaseHashSlot()

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body := executePayload(t)
	timestamp := time.Now().UTC().Format(time.RFC3339)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(platformPrincipalHeader, "alice")
	req.Header.Set(platformPrincipalTimestampHeader, timestamp)
	nonce := platformPrincipalNonceForTest(1)
	req.Header.Set(platformPrincipalNonceHeader, nonce)
	req.Header.Set(platformPrincipalSignatureHeader, platformPrincipalSignatureForTest("platform-secret", http.MethodPost, "/execute", "alice", timestamp, nonce, body))
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp2.StatusCode, http.StatusTooManyRequests)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp2.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["error"] != "platform_body_hash_limit_exceeded" {
		t.Fatalf("error = %q, want platform_body_hash_limit_exceeded", payload["error"])
	}
}

func TestUploadAdmissionPrecedesPlatformBodyHashing(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform, PlatformPrincipalHMACSecret: "platform-secret"}
	cfg.MaxActiveUploads = 1
	s := NewWithServices(cfg, compile.New(), execute.New())
	release, ok, code := s.acquireUpload("occupied")
	if !ok {
		t.Fatalf("failed to occupy upload slot: %s", code)
	}
	defer release()

	body := &readCountingBody{}
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	req.Header.Set(platformPrincipalHeader, "alice")
	req.Header.Set(platformPrincipalTimestampHeader, time.Now().UTC().Format(time.RFC3339))
	req.Header.Set(platformPrincipalNonceHeader, platformPrincipalNonceForTest(1))
	req.Header.Set(platformPrincipalSignatureHeader, "v4="+strings.Repeat("0", sha256.Size*2))
	rr := httptest.NewRecorder()

	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}
	if body.reads != 0 {
		t.Fatalf("request body was read %d times without upload admission", body.reads)
	}
}

func TestUploadAdmissionRejectsBeforeReadingJSONBody(t *testing.T) {
	for _, endpoint := range []string{"/compile", "/execute"} {
		for _, tc := range []struct {
			name         string
			activeLimit  int
			principalCap int
			wantCode     string
		}{
			{name: "global", activeLimit: 1, wantCode: "upload_limit_exceeded"},
			{name: "principal", activeLimit: 2, principalCap: 1, wantCode: "principal_upload_limit_exceeded"},
		} {
			t.Run(strings.TrimPrefix(endpoint, "/")+"/"+tc.name, func(t *testing.T) {
				cfg := configForTest(t)
				cfg.MaxActiveUploads = tc.activeLimit
				cfg.MaxPrincipalUploads = tc.principalCap
				s := NewWithServices(cfg, compile.New(), execute.New())

				blocked := newBlockingBody()
				firstDone := make(chan struct{})
				go func() {
					defer close(firstDone)
					req := httptest.NewRequest(http.MethodPost, endpoint, blocked)
					req.RemoteAddr = "192.0.2.10:1234"
					s.Handler().ServeHTTP(httptest.NewRecorder(), req)
				}()
				select {
				case <-blocked.started:
				case <-time.After(2 * time.Second):
					t.Fatal("first request did not start reading its body")
				}

				rejectedBody := &readCountingBody{}
				req := httptest.NewRequest(http.MethodPost, endpoint, rejectedBody)
				req.RemoteAddr = "192.0.2.10:5678"
				rr := httptest.NewRecorder()
				s.Handler().ServeHTTP(rr, req)
				if rr.Code != http.StatusTooManyRequests {
					t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
				}
				var payload map[string]string
				if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
					t.Fatalf("decode rejection: %v", err)
				}
				if payload["error"] != tc.wantCode {
					t.Fatalf("error = %q, want %q", payload["error"], tc.wantCode)
				}
				if rejectedBody.reads != 0 {
					t.Fatalf("rejected body was read %d times", rejectedBody.reads)
				}

				close(blocked.unblock)
				select {
				case <-firstDone:
				case <-time.After(2 * time.Second):
					t.Fatal("first request did not release upload admission")
				}
				if got := s.uploads.Load(); got != 0 {
					t.Fatalf("active uploads = %d, want 0", got)
				}
			})
		}
	}
}

func TestPlatformAuthEnforcesTrustedProxyCIDRsForUnsignedHeaders(t *testing.T) {
	body := executePayload(t)
	for _, tc := range []struct {
		name      string
		cidrs     []string
		principal string
		want      int
	}{
		{name: "trusted source with principal", cidrs: []string{"127.0.0.1/32", "::1/128"}, principal: "alice", want: http.StatusOK},
		{name: "trusted source missing principal", cidrs: []string{"127.0.0.1/32", "::1/128"}, want: http.StatusUnauthorized},
		{name: "untrusted source", cidrs: []string{"192.0.2.0/24"}, principal: "alice", want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configForTest(t)
			cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform}
			cfg.Execution.Platform.DeploymentTarget = platform.DeploymentTargetDev
			cfg.TrustedPlatformHeaders = true
			cfg.TrustedPlatformHeaderCIDRs = tc.cidrs
			cfg.MaxActiveRuns = 4
			cfg.MaxPendingQueue = 8
			cfg.MaxActiveStreams = 8
			cfg.MaxPrincipalStreams = 8
			s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
				return model.RunResponse{Status: model.RunStatusAccepted}
			}})
			ts := httptest.NewServer(s.Handler())
			defer ts.Close()

			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.principal != "" {
				req.Header.Set(platformPrincipalHeader, tc.principal)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
		})
	}
}

func TestPlatformAuthRejectsUnsignedCIDRHeadersOutsideDev(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform}
	cfg.Execution.Platform.DeploymentTarget = platform.DeploymentTargetCloudRun
	cfg.TrustedPlatformHeaderCIDRs = []string{"127.0.0.1/32", "::1/128"}
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("runner should not be called for unsigned platform principal outside dev")
		return model.RunResponse{}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(executePayload(t)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(platformPrincipalHeader, "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHealthz(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("unexpected healthz response: %q", string(body))
	}
}

func TestCapabilitiesAdvertiseCommunicationOnSupportedRunners(t *testing.T) {
	tests := []struct {
		name string
		edit func(*config.Config)
		want bool
	}{
		{
			name: "self-hosted cgroup helper",
			edit: func(cfg *config.Config) {
				cfg.Execution.Platform = platform.RuntimeOptions{
					DeploymentTarget:   platform.DeploymentTargetSelfHosted,
					ExecutionTransport: platform.ExecutionTransportEmbedded,
					SandboxBackend:     platform.SandboxBackendHelper,
				}
				cfg.Execution.Cgroup.ParentDir = "/sys/fs/cgroup/aonohako"
			},
			want: true,
		},
		{
			name: "Cloud Run embedded helper",
			edit: func(cfg *config.Config) {
				cfg.CommunicationEnabled = true
				cfg.Execution.Platform = platform.RuntimeOptions{
					DeploymentTarget:   platform.DeploymentTargetCloudRun,
					ExecutionTransport: platform.ExecutionTransportEmbedded,
					SandboxBackend:     platform.SandboxBackendHelper,
				}
				cfg.Execution.Cgroup.ParentDir = ""
			},
			want: true,
		},
		{
			name: "Cloud Run embedded helper without dedicated opt in",
			edit: func(cfg *config.Config) {
				cfg.Execution.Platform = platform.RuntimeOptions{
					DeploymentTarget:   platform.DeploymentTargetCloudRun,
					ExecutionTransport: platform.ExecutionTransportEmbedded,
					SandboxBackend:     platform.SandboxBackendHelper,
				}
			},
		},
		{
			name: "self-hosted helper without cgroup",
			edit: func(cfg *config.Config) {
				cfg.Execution.Platform = platform.RuntimeOptions{
					DeploymentTarget:   platform.DeploymentTargetSelfHosted,
					ExecutionTransport: platform.ExecutionTransportEmbedded,
					SandboxBackend:     platform.SandboxBackendHelper,
				}
				cfg.Execution.Cgroup.ParentDir = ""
			},
		},
		{
			name: "Cloud Run remote control plane",
			edit: func(cfg *config.Config) {
				cfg.Execution.Platform = platform.RuntimeOptions{
					DeploymentTarget:   platform.DeploymentTargetCloudRun,
					ExecutionTransport: platform.ExecutionTransportRemote,
					SandboxBackend:     platform.SandboxBackendNone,
				}
				cfg.Execution.Cgroup.ParentDir = ""
			},
		},
		{
			name: "development helper",
			edit: func(cfg *config.Config) {
				cfg.Execution.Platform = platform.RuntimeOptions{
					DeploymentTarget:   platform.DeploymentTargetDev,
					ExecutionTransport: platform.ExecutionTransportEmbedded,
					SandboxBackend:     platform.SandboxBackendHelper,
				}
				cfg.Execution.Cgroup.ParentDir = ""
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configForTest(t)
			tc.edit(&cfg)
			s := NewWithServices(cfg, compile.New(), execute.New())
			s.readinessCheck = nil
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
			s.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d", recorder.Code)
			}
			hasCapability := strings.Contains(recorder.Body.String(), "communication-v1")
			if hasCapability != tc.want {
				t.Fatalf("capabilities = %s, want communication-v1=%v", recorder.Body.String(), tc.want)
			}
		})
	}
}

func TestCapabilitiesAdvertiseInstalledPythonAndPyPyLibrariesOnlyForAttestedImages(t *testing.T) {
	tests := []struct {
		name              string
		edit              func(*testing.T, *config.Config)
		want              bool
		wantCommunication bool
	}{
		{name: "attested image with request opt in", want: true},
		{
			name: "attested image with installed default",
			edit: func(_ *testing.T, cfg *config.Config) {
				cfg.AllowRequestPythonInstalledLibraries = false
				cfg.DefaultPythonLibraryMode = pythonpolicy.LibraryModeInstalled
			},
			want: true,
		},
		{
			name: "coexists with communication capability",
			edit: func(_ *testing.T, cfg *config.Config) {
				cfg.CommunicationEnabled = true
				cfg.Execution.Platform.DeploymentTarget = platform.DeploymentTargetCloudRun
			},
			want:              true,
			wantCommunication: true,
		},
		{
			name: "server policy blocks installed mode",
			edit: func(_ *testing.T, cfg *config.Config) {
				cfg.AllowRequestPythonInstalledLibraries = false
			},
		},
		{
			name: "remote control plane cannot attest workers",
			edit: func(_ *testing.T, cfg *config.Config) {
				cfg.Execution.Platform.ExecutionTransport = platform.ExecutionTransportRemote
			},
		},
		{
			name: "embedded execution without helper cannot attest isolation",
			edit: func(_ *testing.T, cfg *config.Config) {
				cfg.Execution.Platform.SandboxBackend = platform.SandboxBackendNone
			},
		},
		{
			name: "image is missing Python",
			edit: func(t *testing.T, _ *config.Config) {
				t.Setenv("AONOHAKO_LANGUAGES", "plain,pypy")
			},
		},
		{
			name: "image is missing PyPy",
			edit: func(t *testing.T, _ *config.Config) {
				t.Setenv("AONOHAKO_LANGUAGES", "plain,python")
			},
		},
		{
			name: "Python isolation is disabled",
			edit: func(t *testing.T, _ *config.Config) {
				t.Setenv("AONOHAKO_PYTHON_LIBRARY_ISOLATION", "false")
			},
		},
		{
			name: "PyPy isolation is disabled",
			edit: func(t *testing.T, _ *config.Config) {
				t.Setenv("AONOHAKO_PYPY_LIBRARY_ISOLATION", "false")
			},
		},
		{
			name: "external library group does not match",
			edit: func(t *testing.T, _ *config.Config) {
				t.Setenv("AONOHAKO_PYTHON_EXTERNAL_LIBRARY_GID", "65529")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AONOHAKO_LANGUAGES", "plain,pypy,python")
			t.Setenv("AONOHAKO_PYTHON_LIBRARY_ISOLATION", "true")
			t.Setenv("AONOHAKO_PYPY_LIBRARY_ISOLATION", "true")
			t.Setenv("AONOHAKO_PYTHON_EXTERNAL_LIBRARY_GID", strconv.FormatUint(uint64(pythonpolicy.ExternalLibraryGID), 10))

			cfg := configForTest(t)
			cfg.DefaultPythonLibraryMode = pythonpolicy.LibraryModeStdlib
			cfg.AllowRequestPythonInstalledLibraries = true
			cfg.Execution.Platform.ExecutionTransport = platform.ExecutionTransportEmbedded
			cfg.Execution.Platform.SandboxBackend = platform.SandboxBackendHelper
			if tc.edit != nil {
				tc.edit(t, &cfg)
			}

			s := NewWithServices(cfg, compile.New(), execute.New())
			s.readinessCheck = nil
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
			s.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d", recorder.Code)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			var body struct {
				Capabilities []string `json:"capabilities"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode capabilities: %v", err)
			}
			hasCapability := slices.Contains(body.Capabilities, pythonpolicy.InstalledCapability)
			if hasCapability != tc.want {
				t.Fatalf("capabilities = %q, want installed capability=%v", body.Capabilities, tc.want)
			}
			hasCommunication := slices.Contains(body.Capabilities, "communication-v1")
			if hasCommunication != tc.wantCommunication {
				t.Fatalf("capabilities = %q, want communication capability=%v", body.Capabilities, tc.wantCommunication)
			}
		})
	}
}

func TestLivenessRemainsHealthyWhenReadinessFails(t *testing.T) {
	s := newServerForTest(t)
	s.readinessCheck = func() error { return errors.New("sandbox dependency unavailable") }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/livez", want: http.StatusOK},
		{path: "/readyz", want: http.StatusServiceUnavailable},
		{path: "/healthz", want: http.StatusServiceUnavailable},
	} {
		resp, err := http.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatalf("%s request failed: %v", tc.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.path, resp.StatusCode, tc.want)
		}
	}
}

func TestRuntimeReadinessDetectsWorkRootContractLoss(t *testing.T) {
	workRoot := t.TempDir()
	if err := os.Chmod(workRoot, 0o755); err != nil {
		t.Fatalf("Chmod(work root): %v", err)
	}
	t.Setenv("AONOHAKO_WORK_ROOT", workRoot)
	cfg := configForTest(t)
	cfg.Execution.Platform = platform.RuntimeOptions{
		DeploymentTarget:   platform.DeploymentTargetCloudRun,
		ExecutionTransport: platform.ExecutionTransportRemote,
		SandboxBackend:     platform.SandboxBackendNone,
	}
	check := newRuntimeReadinessCheck(cfg)
	if err := check(); err != nil {
		t.Fatalf("initial readiness check: %v", err)
	}
	if err := os.Chmod(workRoot, 0o777); err != nil {
		t.Fatalf("Chmod(work root writable): %v", err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "group/world writable") {
		t.Fatalf("readiness after work-root permission loss = %v", err)
	}
}

func TestRuntimeReadinessDetectsCgroupControllerLoss(t *testing.T) {
	workRoot := t.TempDir()
	if err := os.Chmod(workRoot, 0o755); err != nil {
		t.Fatalf("Chmod(work root): %v", err)
	}
	t.Setenv("AONOHAKO_WORK_ROOT", workRoot)
	cgroupParent := t.TempDir()
	if err := os.WriteFile(filepath.Join(cgroupParent, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o644); err != nil {
		t.Fatalf("write cgroup.controllers: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupParent, "cgroup.subtree_control"), nil, 0o644); err != nil {
		t.Fatalf("write cgroup.subtree_control: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupParent, "cgroup.procs"), nil, 0o644); err != nil {
		t.Fatalf("write cgroup.procs: %v", err)
	}
	cfg := configForTest(t)
	cfg.Execution.Platform = platform.RuntimeOptions{
		DeploymentTarget:   platform.DeploymentTargetSelfHosted,
		ExecutionTransport: platform.ExecutionTransportEmbedded,
		SandboxBackend:     platform.SandboxBackendHelper,
	}
	cfg.Execution.Cgroup.ParentDir = cgroupParent
	check := newRuntimeReadinessCheck(cfg)
	if err := check(); err != nil {
		t.Fatalf("initial readiness check: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupParent, "cgroup.controllers"), []byte("cpu memory\n"), 0o644); err != nil {
		t.Fatalf("remove pids controller: %v", err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "lost pids controller") {
		t.Fatalf("readiness after cgroup controller loss = %v", err)
	}
}

func TestExecuteRequiresBearerAuthWhenConfigured(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthBearer, BearerToken: "secret-token"}
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":     "binary",
		"binaries": []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)

	for _, auth := range []string{"", "Bearer wrong-token", "Basic secret-token"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unauthorized request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 for Authorization %q, got %d", auth, resp.StatusCode)
		}
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authorized request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for authorized request, got %d", resp.StatusCode)
	}
}

func TestHealthzDoesNotRequireBearerAuth(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthBearer, BearerToken: "secret-token"}
	s := NewWithServices(cfg, compile.New(), execute.New())
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for unauthenticated healthz, got %d", resp.StatusCode)
	}
}

func TestExecuteRejectsOversizedTextFieldsBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("execute runner should not be called for oversized text fields")
		return model.RunResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	basePayload := map[string]any{
		"lang":     "binary",
		"binaries": []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
	}

	for _, field := range []string{"stdin", "expected_stdout"} {
		t.Run(field, func(t *testing.T) {
			payload := map[string]any{}
			for k, v := range basePayload {
				payload[k] = v
			}
			payload[field] = strings.Repeat("x", maxRunTextFieldBytes+1)
			body, _ := json.Marshal(payload)

			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for oversized %s, got %d", field, resp.StatusCode)
			}
		})
	}
}

func TestExecuteRejectsInvalidLimitsBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("execute runner should not be called for invalid limits")
		return model.RunResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	tests := []struct {
		name          string
		limits        map[string]any
		captureLimits map[string]any
		spj           map[string]any
		want          string
	}{
		{name: "time zero", limits: map[string]any{"time_ms": 0, "memory_mb": 64}, want: "limits.time_ms"},
		{name: "time too high", limits: map[string]any{"time_ms": maxRunTimeMs + 1, "memory_mb": 64}, want: "limits.time_ms"},
		{name: "memory zero", limits: map[string]any{"time_ms": 1000, "memory_mb": 0}, want: "limits.memory_mb"},
		{name: "memory too high", limits: map[string]any{"time_ms": 1000, "memory_mb": maxRunMemoryMB + 1}, want: "limits.memory_mb"},
		{name: "output negative", limits: map[string]any{"time_ms": 1000, "memory_mb": 64, "output_bytes": -1}, want: "limits.output_bytes"},
		{name: "output too high", limits: map[string]any{"time_ms": 1000, "memory_mb": 64, "output_bytes": maxRunOutputBytes + 1}, want: "limits.output_bytes"},
		{name: "workspace negative", limits: map[string]any{"time_ms": 1000, "memory_mb": 64, "workspace_bytes": -1}, want: "limits.workspace_bytes"},
		{name: "workspace too high", limits: map[string]any{"time_ms": 1000, "memory_mb": 64, "workspace_bytes": int64(maxRunWorkspaceBytes) + 1}, want: "limits.workspace_bytes"},
		{
			name:          "capture stdout negative",
			limits:        map[string]any{"time_ms": 1000, "memory_mb": 64},
			captureLimits: map[string]any{"stdout_bytes": -1},
			want:          "capture_limits.stdout_bytes",
		},
		{
			name:          "capture stderr too high",
			limits:        map[string]any{"time_ms": 1000, "memory_mb": 64},
			captureLimits: map[string]any{"stderr_bytes": maxRunCaptureBytes + 1},
			want:          "capture_limits.stderr_bytes",
		},
		{
			name:   "spj too high",
			limits: map[string]any{"time_ms": 1000, "memory_mb": 64},
			spj: map[string]any{
				"binary": map[string]any{"name": "checker", "data_b64": script, "mode": "exec"},
				"limits": map[string]any{"time_ms": maxRunTimeMs + 1},
			},
			want: "spj.limits.time_ms",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"lang":     "binary",
				"binaries": []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
				"limits":   tc.limits,
			}
			if tc.spj != nil {
				payload["spj"] = tc.spj
			}
			if tc.captureLimits != nil {
				payload["capture_limits"] = tc.captureLimits
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
			bodyBytes, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(bodyBytes), tc.want) {
				t.Fatalf("response %q should mention %q", string(bodyBytes), tc.want)
			}
		})
	}
}

func TestExecuteRejectsInvalidRuntimeProfileBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("execute runner should not be called for invalid runtime_profile")
		return model.RunResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"runtime_profile": "bad profile",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid runtime_profile, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "runtime_profile") {
		t.Fatalf("response %q should mention runtime_profile", string(bodyBytes))
	}
	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("invalid runtime_profile request entered queue: active=%d pending=%d", active, pending)
	}
}

func TestExecuteAllowsConfiguredRuntimeProfile(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = true
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{"low-memory": config.DefaultRuntimeTuningConfig()}
	seenProfile := ""
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		seenProfile = req.RuntimeProfile
		return model.RunResponse{Status: model.RunStatusAccepted}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"runtime_profile": "low-memory",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for configured runtime_profile, got %d", resp.StatusCode)
	}
	events := readSSEEvents(resp.Body, t)
	if len(events) == 0 || events[len(events)-1].Name != "result" {
		t.Fatalf("expected result event for configured runtime_profile, got %#v", events)
	}
	if seenProfile != "low-memory" {
		t.Fatalf("execute runner saw runtime_profile %q", seenProfile)
	}
}

func TestExecuteRejectsRuntimeProfileWhenPolicyDisabledBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = false
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{"low-memory": config.DefaultRuntimeTuningConfig()}
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("execute runner should not be called when runtime_profile policy is disabled")
		return model.RunResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"runtime_profile": "low-memory",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for policy-disabled runtime_profile, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "server policy") {
		t.Fatalf("response %q should mention server policy", string(bodyBytes))
	}
	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("policy-disabled runtime_profile request entered queue: active=%d pending=%d", active, pending)
	}
}

func TestExecuteAppliesProblemRuntimeProfileBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = false
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{"low-memory": config.DefaultRuntimeTuningConfig()}
	s.cfg.Execution.ProblemRuntimeProfiles = map[string]string{"contest-1/a": "low-memory"}
	seenProfile := ""
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		seenProfile = req.RuntimeProfile
		return model.RunResponse{Status: model.RunStatusAccepted}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":       "binary",
		"problem_id": "contest-1/a",
		"binaries":   []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"limits":     map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for problem runtime profile, got %d", resp.StatusCode)
	}
	events := readSSEEvents(resp.Body, t)
	if len(events) == 0 || events[len(events)-1].Name != "result" {
		t.Fatalf("expected result event for problem runtime profile, got %#v", events)
	}
	if seenProfile != "low-memory" {
		t.Fatalf("execute runner saw runtime_profile %q", seenProfile)
	}
}

func TestExecuteAllowsForwardedProblemRuntimeProfileWhenRequestProfilesDisabled(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = false
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{"low-memory": config.DefaultRuntimeTuningConfig()}
	s.cfg.Execution.ProblemRuntimeProfiles = map[string]string{"contest-1/a": "low-memory"}
	seenProfile := ""
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		seenProfile = req.RuntimeProfile
		return model.RunResponse{Status: model.RunStatusAccepted}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"problem_id":      "contest-1/a",
		"runtime_profile": "low-memory",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for forwarded problem runtime profile, got %d", resp.StatusCode)
	}
	events := readSSEEvents(resp.Body, t)
	if len(events) == 0 || events[len(events)-1].Name != "result" {
		t.Fatalf("expected result event for forwarded problem runtime profile, got %#v", events)
	}
	if seenProfile != "low-memory" {
		t.Fatalf("execute runner saw runtime_profile %q", seenProfile)
	}
}

func TestExecuteRejectsRuntimeProfileConflictWithProblemPolicyBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = true
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{
		"low-memory": config.DefaultRuntimeTuningConfig(),
		"jvm-heavy":  config.DefaultRuntimeTuningConfig(),
	}
	s.cfg.Execution.ProblemRuntimeProfiles = map[string]string{"contest-1/a": "low-memory"}
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("execute runner should not be called for conflicting problem runtime profile")
		return model.RunResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"problem_id":      "contest-1/a",
		"runtime_profile": "jvm-heavy",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for conflicting problem runtime profile, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "problem policy") {
		t.Fatalf("response %q should mention problem policy", string(bodyBytes))
	}
	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("conflicting problem runtime profile request entered queue: active=%d pending=%d", active, pending)
	}
}

func TestExecuteSSESequence(t *testing.T) {
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		hooks.OnLog("stdout", "ok\n")
		return model.RunResponse{Status: model.RunStatusAccepted, TimeMs: 5, WallTimeMs: 5, CPUTimeMs: 3, Stdout: "ok\n"}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"expected_stdout": "",
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get(remoteio.ProtocolVersionHeader); got != remoteio.ProtocolVersion {
		t.Fatalf("protocol version header = %q, want %q", got, remoteio.ProtocolVersion)
	}

	events := readSSEEvents(resp.Body, t)
	if len(events) < 4 {
		t.Fatalf("expected accepted/start/log/result events, got %d", len(events))
	}
	if events[0].Name != "progress" {
		t.Fatalf("first event should be progress, got %s", events[0].Name)
	}
	if events[0].JSON["stage"] != "accepted" {
		t.Fatalf("first progress stage should be accepted: %#v", events[0].JSON)
	}
	if events[1].Name != "progress" || events[1].JSON["stage"] != "start" {
		t.Fatalf("second event should be start progress: %#v", events[1])
	}
	if events[2].Name != "log" || events[2].JSON["stream"] != "stdout" || events[2].JSON["chunk"] != "ok\n" {
		t.Fatalf("omitted emit_logs should preserve stdout log events: %#v", events[2])
	}
	last := events[len(events)-1]
	if last.Name != "result" {
		t.Fatalf("last event should be result, got %s", last.Name)
	}
	if last.JSON["status"] != "Accepted" {
		t.Fatalf("unexpected run status in result: %#v", last.JSON)
	}
	if _, ok := last.JSON["wall_time_ms"]; !ok {
		t.Fatalf("result missing wall_time_ms: %#v", last.JSON)
	}
	if _, ok := last.JSON["cpu_time_ms"]; !ok {
		t.Fatalf("result missing cpu_time_ms: %#v", last.JSON)
	}
	if last.JSON["time_ms"] != last.JSON["wall_time_ms"] {
		t.Fatalf("time_ms should mirror wall_time_ms for compatibility: %#v", last.JSON)
	}
}

func TestExecuteCanDisableLogEventsWithoutChangingResult(t *testing.T) {
	type observation struct {
		emitLogs *bool
		onLogNil bool
	}
	observed := make(chan observation, 1)
	exitCode := 120
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		observed <- observation{emitLogs: req.EmitLogs, onLogNil: hooks.OnLog == nil}
		return model.RunResponse{
			Status:        model.RunStatusInitFail,
			TimeMs:        9,
			WallTimeMs:    9,
			CPUTimeMs:     4,
			MemoryKB:      1234,
			ExitCode:      &exitCode,
			Stdout:        "contestant stdout\n",
			Stderr:        "contestant stderr\n",
			Reason:        "sandbox initialization failed",
			VerdictSource: "sandbox_init",
		}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 7\n"))
	payload := map[string]any{
		"lang":      "binary",
		"binaries":  []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"emit_logs": false,
		"limits":    map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	gotObservation := <-observed
	if gotObservation.emitLogs == nil || *gotObservation.emitLogs {
		t.Fatalf("execute runner received emit_logs = %v, want explicit false", gotObservation.emitLogs)
	}
	if !gotObservation.onLogNil {
		t.Fatal("emit_logs=false should omit the OnLog hook and its output copies")
	}

	events := readSSEEvents(resp.Body, t)
	errorSeen := false
	for _, event := range events {
		if event.Name == "log" {
			t.Fatalf("emit_logs=false emitted a log event: %#v", event)
		}
		if event.Name == "error" {
			errorSeen = true
			if event.JSON["message"] != "sandbox initialization failed" {
				t.Fatalf("emit_logs=false leaked output through error event: %#v", event)
			}
		}
	}
	if !errorSeen {
		t.Fatal("container initialization failure should emit a structural error event")
	}
	last := events[len(events)-1]
	if last.Name != "result" {
		t.Fatalf("last event should be result, got %#v", last)
	}
	if last.JSON["status"] != model.RunStatusInitFail ||
		last.JSON["stdout"] != "contestant stdout\n" ||
		last.JSON["stderr"] != "contestant stderr\n" ||
		last.JSON["reason"] != "sandbox initialization failed" ||
		last.JSON["verdict_source"] != "sandbox_init" {
		t.Fatalf("emit_logs=false changed the result payload: %#v", last.JSON)
	}
}

func TestExecuteSSESequenceViaRemoteRunner(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			t.Fatalf("unexpected remote path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: log\n"))
		_, _ = w.Write([]byte("data: {\"stream\":\"stdout\",\"chunk\":\"from-remote\\n\"}\n\n"))
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Accepted\",\"time_ms\":7,\"wall_time_ms\":7,\"cpu_time_ms\":4,\"stdout\":\"from-remote\\n\"}\n\n"))
	}))
	defer remote.Close()

	s, err := New(config.Config{
		Port:              "0",
		MaxActiveRuns:     1,
		MaxPendingQueue:   1,
		HeartbeatInterval: 100 * time.Millisecond,
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetDev,
				ExecutionTransport: platform.ExecutionTransportRemote,
				SandboxBackend:     platform.SandboxBackendNone,
			},
			Remote: config.RemoteExecutorConfig{
				URL: remote.URL,
			},
		},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"expected_stdout": "",
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get(remoteio.ProtocolVersionHeader); got != remoteio.ProtocolVersion {
		t.Fatalf("protocol version header = %q, want %q", got, remoteio.ProtocolVersion)
	}

	events := readSSEEvents(resp.Body, t)
	if len(events) < 4 {
		t.Fatalf("expected accepted/start/log/result events, got %d", len(events))
	}
	if events[2].Name != "log" || events[2].JSON["chunk"] != "from-remote\n" {
		t.Fatalf("unexpected forwarded log event: %#v", events[2])
	}
	last := events[len(events)-1]
	if last.Name != "result" || last.JSON["status"] != "Accepted" {
		t.Fatalf("unexpected result event: %#v", last)
	}
}

func TestExecuteCanDisableLogEventsViaRemoteRunner(t *testing.T) {
	remoteRequest := make(chan model.RunRequest, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			t.Fatalf("unexpected remote path: %s", r.URL.Path)
		}
		var req model.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode remote request: %v", err)
		}
		remoteRequest <- req

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: log\n"))
		_, _ = w.Write([]byte("data: {\"stream\":\"stdout\",\"chunk\":\"must-not-leak\\n\"}\n\n"))
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Accepted\",\"time_ms\":7,\"wall_time_ms\":7,\"cpu_time_ms\":4,\"stdout\":\"kept-in-result\\n\",\"stderr\":\"kept-error\\n\"}\n\n"))
	}))
	defer remote.Close()

	s, err := New(config.Config{
		Port:              "0",
		MaxActiveRuns:     1,
		MaxPendingQueue:   1,
		HeartbeatInterval: 100 * time.Millisecond,
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetDev,
				ExecutionTransport: platform.ExecutionTransportRemote,
				SandboxBackend:     platform.SandboxBackendNone,
			},
			Remote: config.RemoteExecutorConfig{
				URL: remote.URL,
			},
		},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"expected_stdout": "",
		"emit_logs":       false,
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	forwarded := <-remoteRequest
	if forwarded.EmitLogs == nil || *forwarded.EmitLogs {
		t.Fatalf("remote runner received emit_logs = %v, want explicit false", forwarded.EmitLogs)
	}

	events := readSSEEvents(resp.Body, t)
	for _, event := range events {
		if event.Name == "log" {
			t.Fatalf("emit_logs=false forwarded a remote log event: %#v", event)
		}
	}
	last := events[len(events)-1]
	if last.Name != "result" ||
		last.JSON["status"] != model.RunStatusAccepted ||
		last.JSON["stdout"] != "kept-in-result\n" ||
		last.JSON["stderr"] != "kept-error\n" {
		t.Fatalf("emit_logs=false changed the remote result event: %#v", last)
	}
}

func TestCompileSSESequenceViaRemoteRunner(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compile" {
			t.Fatalf("unexpected remote path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"OK\",\"stdout\":\"compiled\\n\",\"artifacts\":[{\"name\":\"Main.pyc\",\"data_b64\":\"Ynl0ZWNvZGU=\"}]}\n\n"))
	}))
	defer remote.Close()

	s, err := New(config.Config{
		Port:              "0",
		MaxActiveRuns:     1,
		MaxPendingQueue:   1,
		HeartbeatInterval: 100 * time.Millisecond,
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetDev,
				ExecutionTransport: platform.ExecutionTransportRemote,
				SandboxBackend:     platform.SandboxBackendNone,
			},
			Remote: config.RemoteExecutorConfig{
				URL: remote.URL,
			},
		},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":        "python3",
		"entry_point": "src/Main.py",
		"sources": []map[string]any{{
			"name":     "src/Main.py",
			"data_b64": base64.StdEncoding.EncodeToString([]byte("print('ok')\n")),
		}},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get(remoteio.ProtocolVersionHeader); got != remoteio.ProtocolVersion {
		t.Fatalf("protocol version header = %q, want %q", got, remoteio.ProtocolVersion)
	}

	events := readSSEEvents(resp.Body, t)
	if len(events) < 4 {
		t.Fatalf("expected accepted/start/log/result events, got %d", len(events))
	}
	if events[2].Name != "log" || events[2].JSON["chunk"] != "compiled\n" {
		t.Fatalf("unexpected forwarded log event: %#v", events[2])
	}
	last := events[len(events)-1]
	if last.Name != "result" || last.JSON["status"] != "OK" {
		t.Fatalf("unexpected result event: %#v", last)
	}
}

type sseEvent struct {
	Name string
	JSON map[string]any
}

func readSSEEvents(r io.Reader, t *testing.T) []sseEvent {
	t.Helper()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), 2*1024*1024)
	events := make([]sseEvent, 0, 8)
	name := ""
	data := ""
	dispatch := func() {
		if name == "" || data == "" {
			name = ""
			data = ""
			return
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			t.Fatalf("invalid json payload for %s: %v", name, err)
		}
		events = append(events, sseEvent{Name: name, JSON: parsed})
		name = ""
		data = ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			dispatch()
			if len(events) > 0 && events[len(events)-1].Name == "result" {
				return events
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("sse scan failed: %v", err)
	}
	return events
}

func configForTest(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "embedded")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "helper")
	return config.Config{
		Port:              "0",
		MaxActiveRuns:     1,
		MaxPendingQueue:   1,
		HeartbeatInterval: 100 * time.Millisecond,
		Execution: config.ExecutionConfig{Platform: platform.RuntimeOptions{
			DeploymentTarget:   platform.DeploymentTargetDev,
			ExecutionTransport: platform.ExecutionTransportEmbedded,
			SandboxBackend:     platform.SandboxBackendHelper,
		}},
	}
}

func executePayload(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"lang":     "binary",
		"binaries": []map[string]any{{"name": "run.sh", "data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n")), "mode": "exec"}},
		"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
	})
	if err != nil {
		t.Fatalf("marshal execute payload: %v", err)
	}
	return body
}

func newServerForTest(t *testing.T) *Server {
	t.Helper()
	return NewWithServices(configForTest(t), compile.New(), execute.New())
}

// --------------- #3: /compile shares queue with /execute ---------------

func TestCompileQueueOverflowReturns429(t *testing.T) {
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		time.Sleep(2 * time.Second)
		return model.RunResponse{Status: model.RunStatusAccepted}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("import time\ntime.sleep(2)\n"))
	compilePayload := map[string]any{
		"lang":    "CPP17",
		"sources": []map[string]any{{"name": "Main.cpp", "data_b64": base64.StdEncoding.EncodeToString([]byte("int main(){}"))}},
	}
	execPayload := map[string]any{
		"lang":     "python",
		"binaries": []map[string]any{{"name": "main.py", "data_b64": script}},
		"limits":   map[string]any{"time_ms": 5000, "memory_mb": 64},
	}

	// Fill the queue with execute request
	execBody, _ := json.Marshal(execPayload)
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(execBody))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("execute req failed: %v", err)
	}
	defer resp1.Body.Close()

	// Fill the pending slot with another execute
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(execBody))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("execute req2 failed: %v", err)
	}
	defer resp2.Body.Close()

	time.Sleep(100 * time.Millisecond)

	// Now compile should also get 429 since it shares the same queue
	compileBody, _ := json.Marshal(compilePayload)
	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(compileBody))
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("compile req failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected compile to get 429 (shared queue), got %d", resp3.StatusCode)
	}
}

func TestCompileSSEHasProgressEvents(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":    "CPP17",
		"sources": []map[string]any{{"name": "Main.cpp", "data_b64": base64.StdEncoding.EncodeToString([]byte("int main(){}"))}},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	events := readSSEEvents(resp.Body, t)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (progress+result), got %d", len(events))
	}
	if events[0].Name != "progress" {
		t.Fatalf("first event should be progress, got %s", events[0].Name)
	}
	if events[0].JSON["stage"] != "accepted" {
		t.Fatalf("first progress stage should be accepted: %#v", events[0].JSON)
	}
}

func TestCompileCanDisableLogEventsWithoutChangingResult(t *testing.T) {
	observed := make(chan *bool, 1)
	s := newServerForTest(t)
	s.compile = compileRunnerStub{run: func(_ context.Context, req *model.CompileRequest) model.CompileResponse {
		observed <- req.EmitLogs
		return model.CompileResponse{
			Status: model.CompileStatusCompileError,
			Stdout: "compiler stdout\n",
			Stderr: "compiler stderr\n",
		}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":      "CPP17",
		"emit_logs": false,
		"sources": []map[string]any{{
			"name":     "Main.cpp",
			"data_b64": base64.StdEncoding.EncodeToString([]byte("int main(){}")),
		}},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	emitLogs := <-observed
	if emitLogs == nil || *emitLogs {
		t.Fatalf("compile runner received emit_logs = %v, want explicit false", emitLogs)
	}
	events := readSSEEvents(resp.Body, t)
	errorSeen := false
	for _, event := range events {
		if event.Name == "log" {
			t.Fatalf("emit_logs=false emitted a compile log event: %#v", event)
		}
		if event.Name == "error" {
			errorSeen = true
			if event.JSON["message"] != "compile failed" {
				t.Fatalf("emit_logs=false leaked compiler output through error event: %#v", event)
			}
		}
	}
	if !errorSeen {
		t.Fatal("compile failure should emit a structural error event")
	}
	last := events[len(events)-1]
	if last.Name != "result" ||
		last.JSON["status"] != model.CompileStatusCompileError ||
		last.JSON["stdout"] != "compiler stdout\n" ||
		last.JSON["stderr"] != "compiler stderr\n" {
		t.Fatalf("emit_logs=false changed compile result: %#v", last)
	}
}

func TestCompileMethodNotAllowed(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/compile")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /compile, got %d", resp.StatusCode)
	}
}

func TestExecuteMethodNotAllowed(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/execute")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /execute, got %d", resp.StatusCode)
	}
}

func TestCompileInvalidJSON(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

func TestCompileRejectsUnknownJSONFields(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", strings.NewReader(`{"lang":"UHMLANG","sources":[{"name":"Main.uhm","data_b64":"dGV4dA=="}],"unexpected":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown compile field, got %d", resp.StatusCode)
	}
}

func TestCompileRejectsTrailingJSONPayload(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", strings.NewReader(`{"lang":"UHMLANG","sources":[{"name":"Main.uhm","data_b64":"dGV4dA=="}]}{"extra":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for trailing compile JSON, got %d", resp.StatusCode)
	}
}

func TestCompileRejectsInvalidSourcesBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.compile = compileRunnerStub{run: func(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
		t.Fatalf("compile runner should not be called for invalid sources")
		return model.CompileResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	tooManySources := make([]map[string]any, 0, maxCompileSourceFiles+1)
	for i := 0; i < maxCompileSourceFiles+1; i++ {
		tooManySources = append(tooManySources, map[string]any{"name": fmt.Sprintf("src/%03d.py", i), "data_b64": "cHJpbnQoJ29rJykK"})
	}
	oversizedSource := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxCompileDecodedSourceBytes+1))

	tests := []struct {
		name    string
		sources any
		want    string
	}{
		{name: "missing", sources: []map[string]any{}, want: "no sources"},
		{name: "too many", sources: tooManySources, want: "too many sources"},
		{name: "invalid base64", sources: []map[string]any{{"name": "Main.py", "data_b64": "!!!!"}}, want: "invalid base64"},
		{name: "duplicate path", sources: []map[string]any{{"name": "Main.py", "data_b64": "cHJpbnQoJ29rJykK"}, {"name": "Main.py", "data_b64": "cHJpbnQoJ29rJykK"}}, want: "duplicate source path"},
		{name: "source too large", sources: []map[string]any{{"name": "Main.py", "data_b64": oversizedSource}}, want: "source too large"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"lang": "python3", "sources": tc.sources}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
			bodyBytes, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(bodyBytes), tc.want) {
				t.Fatalf("response %q should mention %q", string(bodyBytes), tc.want)
			}
			active, pending := s.queue.Snapshot()
			if active != 0 || pending != 0 {
				t.Fatalf("invalid compile source request entered queue: active=%d pending=%d", active, pending)
			}
		})
	}
}

func TestCompileResolvesSourcePayloadURLsBeforeRunner(t *testing.T) {
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Main.py" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("print('ok')\n"))
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	called := false
	s := NewWithServices(configForTest(t), compileRunnerStub{run: func(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
		called = true
		if len(req.Sources) != 1 {
			t.Fatalf("sources = %d, want 1", len(req.Sources))
		}
		data, err := base64.StdEncoding.DecodeString(req.Sources[0].DataB64)
		if err != nil {
			t.Fatalf("decoded source: %v", err)
		}
		if string(data) != "print('ok')\n" {
			t.Fatalf("source url was not resolved: %q", string(data))
		}
		if req.Sources[0].DataURL != "" {
			t.Fatalf("resolved source data_url was retained: %q", req.Sources[0].DataURL)
		}
		return model.CompileResponse{Status: model.CompileStatusOK}
	}}, execute.New())
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"lang":    "python3",
		"sources": []map[string]any{{"name": "Main.py", "data_url": assetServer.URL + "/Main.py"}},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, string(raw))
	}
	events := readSSEEvents(resp.Body, t)
	if len(events) == 0 || events[len(events)-1].Name != "result" {
		t.Fatalf("missing result event: %+v", events)
	}
	if !called {
		t.Fatalf("compile runner was not called")
	}
}

func TestCompileRejectsInvalidRuntimeProfileBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.compile = compileRunnerStub{run: func(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
		t.Fatalf("compile runner should not be called for invalid runtime_profile")
		return model.CompileResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":            "python3",
		"runtime_profile": "bad profile",
		"sources":         []map[string]any{{"name": "Main.py", "data_b64": "cHJpbnQoJ29rJykK"}},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid runtime_profile, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "runtime_profile") {
		t.Fatalf("response %q should mention runtime_profile", string(bodyBytes))
	}
	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("invalid compile runtime_profile request entered queue: active=%d pending=%d", active, pending)
	}
}

func TestCompileAllowsConfiguredRuntimeProfile(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = true
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{"low-memory": config.DefaultRuntimeTuningConfig()}
	seenProfile := ""
	s.compile = compileRunnerStub{run: func(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
		seenProfile = req.RuntimeProfile
		return model.CompileResponse{Status: model.CompileStatusOK}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":            "python3",
		"runtime_profile": "low-memory",
		"sources":         []map[string]any{{"name": "Main.py", "data_b64": "cHJpbnQoJ29rJykK"}},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for configured runtime_profile, got %d", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if seenProfile != "low-memory" {
		t.Fatalf("compile runner saw runtime_profile %q", seenProfile)
	}
}

func TestCompileRejectsRuntimeProfileWhenPolicyDisabledBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = false
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{"low-memory": config.DefaultRuntimeTuningConfig()}
	s.compile = compileRunnerStub{run: func(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
		t.Fatalf("compile runner should not be called when runtime_profile policy is disabled")
		return model.CompileResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":            "python3",
		"runtime_profile": "low-memory",
		"sources":         []map[string]any{{"name": "Main.py", "data_b64": "cHJpbnQoJ29rJykK"}},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for policy-disabled runtime_profile, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "server policy") {
		t.Fatalf("response %q should mention server policy", string(bodyBytes))
	}
	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("policy-disabled compile runtime_profile request entered queue: active=%d pending=%d", active, pending)
	}
}

func TestCompileAppliesProblemRuntimeProfileBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = false
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{"low-memory": config.DefaultRuntimeTuningConfig()}
	s.cfg.Execution.ProblemRuntimeProfiles = map[string]string{"contest-1/a": "low-memory"}
	seenProfile := ""
	s.compile = compileRunnerStub{run: func(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
		seenProfile = req.RuntimeProfile
		return model.CompileResponse{Status: model.CompileStatusOK}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":       "python3",
		"problem_id": "contest-1/a",
		"sources":    []map[string]any{{"name": "Main.py", "data_b64": "cHJpbnQoJ29rJykK"}},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for problem runtime profile, got %d", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if seenProfile != "low-memory" {
		t.Fatalf("compile runner saw runtime_profile %q", seenProfile)
	}
}

func TestCompileAllowsForwardedProblemRuntimeProfileWhenRequestProfilesDisabled(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = false
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{"low-memory": config.DefaultRuntimeTuningConfig()}
	s.cfg.Execution.ProblemRuntimeProfiles = map[string]string{"contest-1/a": "low-memory"}
	seenProfile := ""
	s.compile = compileRunnerStub{run: func(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
		seenProfile = req.RuntimeProfile
		return model.CompileResponse{Status: model.CompileStatusOK}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":            "python3",
		"problem_id":      "contest-1/a",
		"runtime_profile": "low-memory",
		"sources":         []map[string]any{{"name": "Main.py", "data_b64": "cHJpbnQoJ29rJykK"}},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for forwarded problem runtime profile, got %d", resp.StatusCode)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if seenProfile != "low-memory" {
		t.Fatalf("compile runner saw runtime_profile %q", seenProfile)
	}
}

func TestCompileRejectsRuntimeProfileConflictWithProblemPolicyBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.cfg.AllowRequestRuntimeProfile = true
	s.cfg.Execution.RuntimeTuningProfiles = map[string]config.RuntimeTuningConfig{
		"low-memory": config.DefaultRuntimeTuningConfig(),
		"jvm-heavy":  config.DefaultRuntimeTuningConfig(),
	}
	s.cfg.Execution.ProblemRuntimeProfiles = map[string]string{"contest-1/a": "low-memory"}
	s.compile = compileRunnerStub{run: func(ctx context.Context, req *model.CompileRequest) model.CompileResponse {
		t.Fatalf("compile runner should not be called for conflicting problem runtime profile")
		return model.CompileResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":            "python3",
		"problem_id":      "contest-1/a",
		"runtime_profile": "jvm-heavy",
		"sources":         []map[string]any{{"name": "Main.py", "data_b64": "cHJpbnQoJ29rJykK"}},
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for conflicting problem runtime profile, got %d", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "problem policy") {
		t.Fatalf("response %q should mention problem policy", string(bodyBytes))
	}
	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("conflicting problem runtime profile request entered queue: active=%d pending=%d", active, pending)
	}
}

func TestExecuteInvalidJSON(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

func TestExecuteRejectsUnknownJSONFields(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", strings.NewReader(`{"lang":"text","binaries":[{"name":"Main.txt","data_b64":"dGV4dA=="}],"limits":{"time_ms":1000,"memory_mb":64},"unexpected":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown execute field, got %d", resp.StatusCode)
	}
}

func TestExecuteRejectsTrailingJSONPayload(t *testing.T) {
	s := newServerForTest(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", strings.NewReader(`{"lang":"text","binaries":[{"name":"Main.txt","data_b64":"dGV4dA=="}],"limits":{"time_ms":1000,"memory_mb":64}}{"extra":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for trailing execute JSON, got %d", resp.StatusCode)
	}
}

func TestExecuteRejectsInvalidBinariesBeforeQueueing(t *testing.T) {
	s := newServerForTest(t)
	s.execute = executeRunnerStub{run: func(ctx context.Context, req *model.RunRequest, hooks execute.Hooks) model.RunResponse {
		t.Fatalf("execute runner should not be called for invalid binaries")
		return model.RunResponse{}
	}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	tests := []struct {
		name     string
		binaries []map[string]any
		want     string
	}{
		{
			name:     "invalid base64",
			binaries: []map[string]any{{"name": "run.sh", "data_b64": "!!!!", "mode": "exec"}},
			want:     "invalid base64",
		},
		{
			name: "duplicate path",
			binaries: []map[string]any{
				{"name": "run.sh", "data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n")), "mode": "exec"},
				{"name": "run.sh", "data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 1\n")), "mode": "exec"},
			},
			want: "duplicate binary path",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"lang":     "binary",
				"binaries": tc.binaries,
				"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d", tc.name, resp.StatusCode)
			}
			bodyBytes, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(bodyBytes), tc.want) {
				t.Fatalf("response %q should mention %q", string(bodyBytes), tc.want)
			}
			active, pending := s.queue.Snapshot()
			if active != 0 || pending != 0 {
				t.Fatalf("invalid execute binary request entered queue: active=%d pending=%d", active, pending)
			}
		})
	}
}

type noFlushResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

type deadlineResponseRecorder struct {
	*httptest.ResponseRecorder
}

func (w *deadlineResponseRecorder) SetWriteDeadline(time.Time) error {
	return nil
}

func (w *noFlushResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *noFlushResponseWriter) WriteHeader(code int) {
	w.status = code
}

func (w *noFlushResponseWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}

func TestCompileSSEInitFailureReleasesPermit(t *testing.T) {
	s := newServerForTest(t)
	payload := map[string]any{
		"lang":    "UHMLANG",
		"sources": []map[string]any{{"name": "Main.uhm", "data_b64": base64.StdEncoding.EncodeToString([]byte("text"))}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/compile", bytes.NewReader(body))

	w := &noFlushResponseWriter{}
	s.compileHandler(w, req)

	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("queue leaked after compile SSE init failure: active=%d pending=%d", active, pending)
	}
}

func TestExecuteSSEInitFailureReleasesPermit(t *testing.T) {
	s := newServerForTest(t)
	payload := map[string]any{
		"lang":     "binary",
		"binaries": []map[string]any{{"name": "run.sh", "data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho ok\n")), "mode": "exec"}},
		"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))

	w := &noFlushResponseWriter{}
	s.executeHandler(w, req)

	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("queue leaked after execute SSE init failure: active=%d pending=%d", active, pending)
	}
}

func TestCanceledRequestDoesNotStartRunnerWithImmediatePermit(t *testing.T) {
	t.Run("compile", func(t *testing.T) {
		called := false
		s := newServerForTest(t)
		s.compile = compileRunnerStub{run: func(context.Context, *model.CompileRequest) model.CompileResponse {
			called = true
			return model.CompileResponse{Status: model.CompileStatusOK}
		}}
		body, err := json.Marshal(map[string]any{
			"lang":    "PYTHON3",
			"sources": []map[string]any{{"name": "Main.py", "data_b64": base64.StdEncoding.EncodeToString([]byte("print('ok')\n"))}},
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodPost, "/compile", bytes.NewReader(body)).WithContext(ctx)
		w := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}

		s.compileHandler(w, req)
		if called {
			t.Fatal("compile runner started after request cancellation")
		}
		active, pending := s.queue.Snapshot()
		if active != 0 || pending != 0 {
			t.Fatalf("queue leaked after canceled compile request: active=%d pending=%d", active, pending)
		}
	})

	t.Run("execute", func(t *testing.T) {
		called := false
		s := newServerForTest(t)
		s.execute = executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
			called = true
			return model.RunResponse{Status: model.RunStatusAccepted}
		}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(executePayload(t))).WithContext(ctx)
		w := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}

		s.executeHandler(w, req)
		if called {
			t.Fatal("execute runner started after request cancellation")
		}
		active, pending := s.queue.Snapshot()
		if active != 0 || pending != 0 {
			t.Fatalf("queue leaked after canceled execute request: active=%d pending=%d", active, pending)
		}
	})
}

func TestCompileSlowSSEReaderWriteDeadlineReleasesPermit(t *testing.T) {
	runnerStarted := make(chan struct{})
	releaseRunner := make(chan struct{})
	largeOutput := strings.Repeat("x", 8<<20)
	s := newServerForTest(t)
	s.cfg.HeartbeatInterval = time.Hour
	s.sseWriteTimeout = 100 * time.Millisecond
	s.compile = compileRunnerStub{run: func(context.Context, *model.CompileRequest) model.CompileResponse {
		close(runnerStarted)
		<-releaseRunner
		return model.CompileResponse{Status: model.CompileStatusOK, Stdout: largeOutput}
	}}

	server := httptest.NewUnstartedServer(s.Handler())
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				_ = tcpConn.SetWriteBuffer(1024)
			}
		}
	}
	if server.Config.WriteTimeout != 0 {
		t.Fatalf("test server unexpectedly has global WriteTimeout %v", server.Config.WriteTimeout)
	}
	server.Start()
	defer server.Close()
	runnerReleased := false
	defer func() {
		if !runnerReleased {
			close(releaseRunner)
		}
	}()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetReadBuffer(1024)
	}
	payload, err := json.Marshal(map[string]any{
		"lang":    "python3",
		"sources": []map[string]any{{"name": "Main.py", "data_b64": base64.StdEncoding.EncodeToString([]byte("print('ok')\n"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHeader := fmt.Sprintf(
		"POST /compile HTTP/1.1\r\nHost: slow-reader\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: keep-alive\r\n\r\n",
		len(payload),
	)
	if _, err := io.WriteString(conn, requestHeader); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReaderSize(conn, 16)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read response headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-runnerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("compile runner did not start")
	}
	active, _ := s.queue.Snapshot()
	if active != 1 {
		t.Fatalf("active runs = %d, want 1 before blocked response write", active)
	}
	close(releaseRunner)
	runnerReleased = true

	deadline := time.Now().Add(3 * time.Second)
	for {
		active, pending := s.queue.Snapshot()
		if active == 0 && pending == 0 && s.streams.Load() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slow SSE reader retained capacity: active=%d pending=%d streams=%d", active, pending, s.streams.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
