package execute

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

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
