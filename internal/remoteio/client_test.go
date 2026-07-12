package remoteio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMetadataHTTPClientDisablesEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	t.Setenv("http_proxy", "http://proxy.invalid:3128")

	metadataClient := NewMetadataHTTPClient()
	metadataTransport, ok := metadataClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("metadata transport type = %T, want *http.Transport", metadataClient.Transport)
	}
	if metadataTransport.Proxy != nil {
		t.Fatalf("metadata transport must not consult HTTP_PROXY")
	}

	runnerClient := NewHTTPClient()
	runnerTransport, ok := runnerClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("runner transport type = %T, want *http.Transport", runnerClient.Transport)
	}
	if runnerTransport.Proxy == nil {
		t.Fatalf("runner transport should retain environment proxy support")
	}
	if metadataTransport == runnerTransport {
		t.Fatalf("metadata and runner clients must not share a transport")
	}
	if metadataClient.Timeout != metadataRequestTimeout {
		t.Fatalf("metadata timeout = %v, want %v", metadataClient.Timeout, metadataRequestTimeout)
	}
	if runnerClient.Timeout != 0 {
		t.Fatalf("runner timeout = %v, want no total timeout for SSE", runnerClient.Timeout)
	}
}

func TestMetadataHTTPClientTimesOutStalledResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer does not support flushing")
			return
		}
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewMetadataHTTPClient()
	defer client.CloseIdleConnections()
	client.Timeout = 100 * time.Millisecond
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("metadata request returned before response body: %v", err)
	}
	defer resp.Body.Close()

	started := time.Now()
	_, err = io.ReadAll(resp.Body)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled metadata body error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled metadata body timed out after %v, want under 1s", elapsed)
	}
}

func TestHTTPClientsRejectRedirectsBeforeForwardingCredentials(t *testing.T) {
	var targetRequests atomic.Int64
	var targetCredentialRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Metadata-Flavor") != "" {
			targetCredentialRequests.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	tests := []struct {
		name      string
		newClient func() *http.Client
	}{
		{name: "runner", newClient: NewHTTPClient},
		{name: "metadata", newClient: NewMetadataHTTPClient},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.newClient()
			defer client.CloseIdleConnections()
			req, err := http.NewRequest(http.MethodGet, redirect.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("Metadata-Flavor", "Google")

			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if err == nil || !strings.Contains(err.Error(), errRedirectNotAllowed.Error()) {
				t.Fatalf("redirect request error = %v, want %q", err, errRedirectNotAllowed)
			}
		})
	}

	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
	if got := targetCredentialRequests.Load(); got != 0 {
		t.Fatalf("redirect target credential requests = %d, want 0", got)
	}
}

func TestRequestUploadTimeoutCancelsStalledWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracedCtx, finish := WithRequestUploadTimeout(ctx, cancel, 20*time.Millisecond)

	select {
	case <-tracedCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("request upload timeout did not cancel the request context")
	}
	if !finish() {
		t.Fatal("request upload timeout did not report that its timer fired")
	}
}

func TestRequestUploadTimeoutStopsAfterRequestWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tracedCtx, finish := WithRequestUploadTimeout(ctx, cancel, 20*time.Millisecond)
	trace := httptrace.ContextClientTrace(tracedCtx)
	if trace == nil || trace.WroteRequest == nil {
		t.Fatal("request upload context is missing WroteRequest trace")
	}
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	time.Sleep(3 * 20 * time.Millisecond)

	if err := tracedCtx.Err(); err != nil {
		t.Fatalf("completed request write was canceled: %v", err)
	}
	if finish() {
		t.Fatal("completed request write was reported as timed out")
	}
}
