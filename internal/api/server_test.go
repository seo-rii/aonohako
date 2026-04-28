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
	"strings"
	"sync"
	"testing"
	"time"

	"aonohako/internal/compile"
	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/platform"
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

func platformPrincipalSignatureForTest(secret, principal string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(principal))
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
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
	for _, tc := range []struct {
		name      string
		principal string
		signature string
		want      int
	}{
		{name: "missing signature", principal: "alice", want: http.StatusUnauthorized},
		{name: "bad signature", principal: "alice", signature: "v1=bad", want: http.StatusUnauthorized},
		{name: "valid signature", principal: "alice", signature: platformPrincipalSignatureForTest("platform-secret", "alice"), want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.principal != "" {
				req.Header.Set(platformPrincipalHeader, tc.principal)
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
		name   string
		limits map[string]any
		spj    map[string]any
		want   string
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
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
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
	return config.Config{Port: "0", MaxActiveRuns: 1, MaxPendingQueue: 1, HeartbeatInterval: 100 * time.Millisecond}
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
		{name: "invalid base64 length", sources: []map[string]any{{"name": "Main.py", "data_b64": "A"}}, want: "invalid base64 length"},
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

type noFlushResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
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
