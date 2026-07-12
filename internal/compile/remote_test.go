package compile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/remoteio"
)

type compileRoundTripFunc func(*http.Request) (*http.Response, error)

func (f compileRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRemoteRunnerForwardsCompileRequest(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compile" {
			t.Fatalf("unexpected remote path: %s", r.URL.Path)
		}
		var req model.CompileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.EntryPoint != "src/Main.py" {
			t.Fatalf("unexpected entry_point: %+v", req)
		}
		if req.RuntimeProfile != "low-memory" {
			t.Fatalf("runtime_profile = %q, want low-memory", req.RuntimeProfile)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"OK\",\"stdout\":\"from-remote\\n\",\"artifacts\":[{\"name\":\"Main.pyc\",\"data_b64\":\"Ynl0ZWNvZGU=\"}]}\n\n"))
	}))
	defer remote.Close()

	runner, err := Build(config.Config{
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
		t.Fatalf("Build returned error: %v", err)
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang:           "PYTHON3",
		EntryPoint:     "src/Main.py",
		RuntimeProfile: "low-memory",
		Sources: []model.Source{{
			Name:    "src/Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Stdout != "from-remote\n" {
		t.Fatalf("stdout mismatch: %+v", resp)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.pyc" {
		t.Fatalf("unexpected artifacts: %+v", resp.Artifacts)
	}
}

func TestRemoteRunnerCompileIncludesRemoteErrorMessage(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte("data: {\"message\":\"remote compile failed\"}\n\n"))
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Compile Error\",\"stderr\":\"boom\\n\"}\n\n"))
	}))
	defer remote.Close()

	runner, err := Build(config.Config{
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
		t.Fatalf("Build returned error: %v", err)
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusCompileError {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !strings.Contains(resp.Reason, "remote compile failed") {
		t.Fatalf("expected remote error to survive, got %+v", resp)
	}
}

func TestRemoteRunnerCompilePrefersPreSSEErrorMessage(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request","message":"source path duplicated"}`))
	}))
	defer remote.Close()

	runner, err := Build(config.Config{
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
		t.Fatalf("Build returned error: %v", err)
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "source path duplicated") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRemoteRunnerCompileRejectsOversizedSSEEvents(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write([]byte(strings.Repeat("x", remoteio.DefaultSSELineBytes+1)))
		_, _ = w.Write([]byte("\n\n"))
	}))
	defer remote.Close()

	runner, err := Build(config.Config{
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
		t.Fatalf("Build returned error: %v", err)
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "sse line too large") {
		t.Fatalf("expected bounded SSE failure, got %+v", resp)
	}
}

func TestRemoteRunnerCompileTimesOutIdleSSEStream(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("test server does not support flushing")
		}
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer remote.Close()

	runner := &remoteRunner{
		client:      remote.Client(),
		compileURL:  remote.URL + "/compile",
		idleTimeout: 10 * time.Millisecond,
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "idle timeout") {
		t.Fatalf("expected idle timeout failure, got %+v", resp)
	}
}

func TestRemoteRunnerCompileRejectsProtocolVersionMismatch(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(remoteio.ProtocolVersionHeader, "1900-01-01")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"OK\",\"stdout\":\"compiled\\n\"}\n\n"))
	}))
	defer remote.Close()

	runner, err := Build(config.Config{
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
		t.Fatalf("Build returned error: %v", err)
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "protocol mismatch") {
		t.Fatalf("expected protocol mismatch failure, got %+v", resp)
	}
}

func TestRemoteRunnerCompileStrictProtocolRejectsMissingVersion(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"OK\",\"stdout\":\"compiled\\n\"}\n\n"))
	}))
	defer remote.Close()

	runner := &remoteRunner{
		client:      remote.Client(),
		compileURL:  remote.URL + "/compile",
		strictProto: true,
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "missing remote protocol version header") {
		t.Fatalf("expected missing protocol header failure, got %+v", resp)
	}
}

func TestRemoteRunnerCompileRejectsMalformedErrorEvents(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte("data: not-json\n\n"))
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"OK\",\"stdout\":\"compiled\\n\"}\n\n"))
	}))
	defer remote.Close()

	runner, err := Build(config.Config{
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
		t.Fatalf("Build returned error: %v", err)
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "remote error decode failed") {
		t.Fatalf("expected malformed error failure, got %+v", resp)
	}
}

func TestRemoteRunnerCompileMetadataAuthUsesProxyDisabledClient(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	t.Setenv("http_proxy", "http://proxy.invalid:3128")

	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Metadata-Flavor"); got != "Google" {
			t.Errorf("Metadata-Flavor = %q, want Google", got)
		}
		if got := r.URL.Query().Get("audience"); got != "https://runner.internal" {
			t.Errorf("audience = %q, want runner URL", got)
		}
		if got := r.URL.Query().Get("format"); got != "full" {
			t.Errorf("format = %q, want full", got)
		}
		_, _ = w.Write([]byte("metadata-token\n"))
	}))
	defer metadata.Close()

	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{
			Remote: config.RemoteExecutorConfig{
				URL:      "https://runner.internal",
				Auth:     config.RemoteAuthCloudRunIDToken,
				Audience: "https://runner.internal",
			},
		},
	}).(*remoteRunner)
	if runner.metadataClient == runner.client {
		t.Fatalf("metadata and runner requests must use separate clients")
	}
	transport, ok := runner.metadataClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("metadata transport type = %T, want *http.Transport", runner.metadataClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatalf("metadata transport must not consult HTTP_PROXY")
	}
	runner.client = nil
	runner.metadataURL = metadata.URL

	header, err := runner.authorizationHeader(context.Background())
	if err != nil {
		t.Fatalf("authorizationHeader returned error: %v", err)
	}
	if header != "Bearer metadata-token" {
		t.Fatalf("authorization header = %q, want metadata token", header)
	}
}

func TestRemoteRunnerCompileRejectsCredentialRedirect(t *testing.T) {
	var targetRequests atomic.Int64
	var targetCredentialRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		if r.Header.Get("Authorization") != "" {
			targetCredentialRequests.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"OK\"}\n\n"))
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer compile-secret" {
			t.Errorf("initial authorization header = %q, want bearer token", got)
		}
		http.Redirect(w, r, target.URL+"/compile", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{
			URL:         redirect.URL,
			Auth:        config.RemoteAuthBearer,
			BearerToken: "compile-secret",
		}},
	})
	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "python3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})

	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "redirects are not allowed") {
		t.Fatalf("redirect response = %+v, want rejected remote request", resp)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
	if got := targetCredentialRequests.Load(); got != 0 {
		t.Fatalf("redirect target credential requests = %d, want 0", got)
	}
}

func TestRemoteRunnerCompileMetadataAuthRejectsIncompleteTokens(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
		want    string
	}{
		{
			name: "partial token timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("partial-token"))
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Errorf("response writer does not support flushing")
					return
				}
				flusher.Flush()
				<-r.Context().Done()
			},
			timeout: 100 * time.Millisecond,
			want:    "deadline exceeded",
		},
		{
			name: "oversized token",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("x", remoteio.MaxMetadataIdentityTokenBytes+1)))
			},
			want: "response body too large",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metadata := httptest.NewServer(tc.handler)
			defer metadata.Close()

			runner := newRemoteRunner(config.Config{
				Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{
					URL:      "https://runner.internal",
					Auth:     config.RemoteAuthCloudRunIDToken,
					Audience: "https://runner.internal",
				}},
			}).(*remoteRunner)
			runner.metadataURL = metadata.URL
			if tc.timeout > 0 {
				runner.metadataClient.Timeout = tc.timeout
			}

			header, err := runner.authorizationHeader(context.Background())
			if header != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("authorizationHeader = (%q, %v), want empty header and %q", header, err, tc.want)
			}
		})
	}
}

func TestRemoteRunnerCompileBoundsErrorResponseBodyRead(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer does not support flushing")
			return
		}
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer remote.Close()

	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{URL: remote.URL}},
	}).(*remoteRunner)
	runner.errorBodyTimeout = 100 * time.Millisecond
	started := time.Now()
	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "python3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})

	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "response body read timed out") {
		t.Fatalf("stalled error response = %+v, want bounded body-read failure", resp)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled error response returned after %v, want under 1s", elapsed)
	}
}

func TestRemoteRunnerCompileBoundsStalledRequestUpload(t *testing.T) {
	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{URL: "https://runner.internal"}},
	}).(*remoteRunner)
	runner.uploadTimeout = 20 * time.Millisecond
	runner.client = &http.Client{Transport: compileRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}

	started := time.Now()
	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "python3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "request upload timed out") {
		t.Fatalf("stalled upload response = %+v, want bounded upload failure", resp)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled upload returned after %v, want under 1s", elapsed)
	}
}

func TestRemoteRunnerCompileKeepsUploadTimeoutAfterEarlyResponse(t *testing.T) {
	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{URL: "https://runner.internal"}},
	}).(*remoteRunner)
	runner.uploadTimeout = 20 * time.Millisecond
	runner.client = &http.Client{Transport: compileRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			<-req.Context().Done()
			_ = writer.CloseWithError(req.Context().Err())
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       reader,
		}, nil
	})}

	started := time.Now()
	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "python3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "request upload timed out") {
		t.Fatalf("early response with stalled upload = %+v, want bounded upload failure", resp)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("early response with stalled upload returned after %v, want under 1s", elapsed)
	}
}

func TestRemoteRunnerCompileRejectsDeceptiveSSEContentTypes(t *testing.T) {
	for _, contentType := range []string{`application/json; note="text/event-stream"`, "text/event-streaming"} {
		t.Run(contentType, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				_, _ = w.Write([]byte("event: result\ndata: {\"status\":\"OK\"}\n\n"))
			}))
			defer remote.Close()

			runner := newRemoteRunner(config.Config{
				Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{URL: remote.URL}},
			})
			resp := runner.Run(context.Background(), &model.CompileRequest{
				Lang: "python3",
				Sources: []model.Source{{
					Name:    "Main.py",
					DataB64: "cHJpbnQoJ29rJykK",
				}},
			})
			if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "unexpected content type") {
				t.Fatalf("deceptive content type response = %+v", resp)
			}
		})
	}
}

func TestNormalizeRemoteCompileURLAppendsCompilePath(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "https://runner.internal", want: "https://runner.internal/compile"},
		{raw: "https://runner.internal/base", want: "https://runner.internal/base/compile"},
		{raw: "https://runner.internal/compile", want: "https://runner.internal/compile"},
		{raw: "https://runner.internal/compile/", want: "https://runner.internal/compile"},
	}

	for _, tc := range tests {
		if got := normalizeRemoteCompileURL(tc.raw); got != tc.want {
			t.Fatalf("normalizeRemoteCompileURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
