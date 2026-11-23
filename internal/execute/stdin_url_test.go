package execute

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/payloadurl"
)

func setStdinURLHTTPClientForTest(t *testing.T, targetURL string) {
	t.Helper()
	parsed, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("parse test payload URL: %v", err)
	}
	dialer := &net.Dialer{}
	testClient := payloadurl.NewHTTPClientWithOptions(payloadurl.ClientOptions{
		Timeout: stdinURLDownloadTimeout,
		Resolver: payloadurl.LookupIPAddrFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, parsed.Host)
		},
	})
	previous := stdinURLHTTPClient
	stdinURLHTTPClient = testClient
	t.Cleanup(func() {
		testClient.CloseIdleConnections()
		stdinURLHTTPClient = previous
	})
}

func TestOpenStdinURLAllowsInjectedPublicDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "payload")
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)

	body, err := openStdinURL(context.Background(), "http://payload.example/input", 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Fatalf("payload = %q, want payload", string(data))
	}
}

func TestOpenStdinURLRejectsLoopbackBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "secret")
	}))
	defer server.Close()

	if _, err := openStdinURL(context.Background(), server.URL, 16, nil); err == nil || !strings.Contains(err.Error(), "no public addresses") {
		t.Fatalf("openStdinURL error = %v, want loopback rejection", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("private server received %d requests", got)
	}
}

func TestValidateStdinURLRejectsCredentials(t *testing.T) {
	if err := validateStdinURL("https://user:secret@example.com/input"); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("validateStdinURL error = %v, want credentials rejection", err)
	}
}

func TestOpenStdinURLRetriesTransientStatusBeforeExposingBody(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "aonohako-stdin-url/1" {
			t.Errorf("User-Agent = %q", got)
		}
		switch requests.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "discarded first attempt\n")
		case 2:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "discarded second attempt\n")
		default:
			_, _ = io.WriteString(w, "payload exactly once\n")
		}
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	initialBudget := 2 * time.Second
	budget := &stdinURLDownloadBudget{remaining: initialBudget}

	body, err := openStdinURL(context.Background(), "http://payload.example/private-input?token=secret", 64, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if budget.remaining != initialBudget {
		t.Fatalf("budget charged before body completion: %v", budget.remaining)
	}
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "payload exactly once\n" {
		t.Fatalf("payload = %q, error = %v", data, err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("download requests = %d, want 3", got)
	}
	remainingAfterEOF := budget.remaining
	if remainingAfterEOF >= initialBudget-250*time.Millisecond {
		t.Fatalf("retry backoff was not charged to budget: remaining %v", remainingAfterEOF)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if budget.remaining != remainingAfterEOF {
		t.Fatalf("Close charged completed download twice: %v -> %v", remainingAfterEOF, budget.remaining)
	}
}

func TestOpenStdinURLBoundsRetriesAndRedactsFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		attempts int64
	}{
		{name: "transient exhaustion", status: http.StatusServiceUnavailable, attempts: 3},
		{name: "permanent not found", status: http.StatusNotFound, attempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "upstream secret response")
			}))
			defer server.Close()
			setStdinURLHTTPClientForTest(t, server.URL)

			body, err := openStdinURL(context.Background(), "http://sensitive.example/private-input?token=secret-query", 64, nil)
			if body != nil || err == nil {
				t.Fatalf("body = %v, error = %v, want rejected download", body, err)
			}
			assertStdinURLFailureRedacted(t, err)
			if got := requests.Load(); got != tc.attempts {
				t.Fatalf("download requests = %d, want %d", got, tc.attempts)
			}
		})
	}
}

func TestOpenStdinURLHonorsCancellationDuringRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		cancel()
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)

	started := time.Now()
	_, err := openStdinURL(ctx, "http://sensitive.example/private-input?token=secret-query", 64, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	assertStdinURLFailureRedacted(t, err)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled download returned after %v", elapsed)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("canceled download requests = %d, want 1", got)
	}
}

func TestOpenStdinURLSharesDownloadBudgetAcrossRetryAndBody(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	budget := &stdinURLDownloadBudget{remaining: 250 * time.Millisecond}
	started := time.Now()
	body, err := openStdinURL(context.Background(), "http://sensitive.example/private-input?token=secret-query", 64, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if string(data) != "partial" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("payload = %q, error = %v, want partial payload and shared deadline", data, err)
	}
	assertStdinURLFailureRedacted(t, err)
	if budget.remaining != 0 {
		t.Fatalf("expired budget = %v, want zero", budget.remaining)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shared download budget returned after %v", elapsed)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("download requests = %d, want 2 without body replay", got)
	}
	if _, err := openStdinURL(context.Background(), "http://sensitive.example/private-input?token=secret-query", 64, budget); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exhausted budget error = %v, want deadline exceeded", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("exhausted budget caused %d total requests", got)
	}
}

func TestOpenStdinURLRejectsOversizedResponseAfterRetry(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Length", "32")
		_, _ = io.WriteString(w, strings.Repeat("x", 32))
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	budget := &stdinURLDownloadBudget{remaining: time.Second}

	body, err := openStdinURL(context.Background(), "http://sensitive.example/private-input?token=secret-query", 16, budget)
	if body != nil || err == nil || !strings.Contains(err.Error(), "download too large: max 16 bytes") {
		t.Fatalf("body = %v, error = %v, want content-length limit", body, err)
	}
	assertStdinURLFailureRedacted(t, err)
	if budget.remaining >= 950*time.Millisecond {
		t.Fatalf("failed download did not charge retry time: %v", budget.remaining)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("oversized download requests = %d, want 2", got)
	}
}

type stdinURLRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f stdinURLRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type stdinURLFailingBody struct {
	rawURL string
}

func (b stdinURLFailingBody) Read(p []byte) (int, error) {
	return copy(p, "partial"), errors.New("read failed: " + b.rawURL)
}

func (b stdinURLFailingBody) Close() error {
	return errors.New("close failed: " + b.rawURL)
}

func TestOpenStdinURLRedactsBodyErrorsWithoutReplay(t *testing.T) {
	const rawURL = "http://sensitive.example/private-input?token=secret-query"
	var requests atomic.Int64
	previous := stdinURLHTTPClient
	stdinURLHTTPClient = &http.Client{Timeout: time.Second, Transport: stdinURLRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: stdinURLFailingBody{rawURL: rawURL}, ContentLength: -1, Request: req}, nil
	})}
	t.Cleanup(func() { stdinURLHTTPClient = previous })
	budget := &stdinURLDownloadBudget{remaining: time.Second}

	body, err := openStdinURL(context.Background(), rawURL, 64, budget)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	if string(data) != "partial" || err == nil {
		t.Fatalf("payload = %q, error = %v, want partial payload and sanitized failure", data, err)
	}
	assertStdinURLFailureRedacted(t, err)
	remainingAfterRead := budget.remaining
	err = body.Close()
	if err == nil {
		t.Fatal("Close did not report the underlying failure")
	}
	assertStdinURLFailureRedacted(t, err)
	if budget.remaining != remainingAfterRead {
		t.Fatalf("Close charged failed read twice: %v -> %v", remainingAfterRead, budget.remaining)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("failed body triggered %d requests, want no replay", got)
	}
}

func assertStdinURLFailureRedacted(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{"sensitive.example", "private-input", "secret-query", "upstream secret response"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposes URL or upstream response %q: %v", secret, err)
		}
	}
}
