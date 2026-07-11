package payloadurl

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateRejectsUnsafeURLSyntax(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "relative", url: "/payload", want: "scheme"},
		{name: "unsupported scheme", url: "file:///tmp/payload", want: "scheme"},
		{name: "missing host", url: "https:///payload", want: "host"},
		{name: "credentials", url: "https://user:secret@example.com/payload", want: "credentials"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.url); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate(%q) error = %v, want %q", tc.url, err, tc.want)
			}
		})
	}
}

func TestPublicAddressPolicy(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{address: "93.184.216.34", public: true},
		{address: "2606:2800:220:1:248:1893:25c8:1946", public: true},
		{address: "0.0.0.0"},
		{address: "127.0.0.1"},
		{address: "10.0.0.1"},
		{address: "100.64.0.1"},
		{address: "169.254.169.254"},
		{address: "172.16.0.1"},
		{address: "192.0.2.1"},
		{address: "192.168.0.1"},
		{address: "198.18.0.1"},
		{address: "198.51.100.1"},
		{address: "203.0.113.1"},
		{address: "224.0.0.1"},
		{address: "240.0.0.1"},
		{address: "::"},
		{address: "::1"},
		{address: "::ffff:127.0.0.1"},
		{address: "64:ff9b::a9fe:a9fe"},
		{address: "64:ff9b:1::a9fe:a9fe"},
		{address: "2001::1"},
		{address: "2001:db8::1"},
		{address: "2002:a9fe:a9fe::1"},
		{address: "3fff::1"},
		{address: "fc00::1"},
		{address: "fe80::1"},
		{address: "ff02::1"},
	}
	for _, tc := range tests {
		t.Run(tc.address, func(t *testing.T) {
			if got := isPublicAddress(netip.MustParseAddr(tc.address)); got != tc.public {
				t.Fatalf("isPublicAddress(%s) = %v, want %v", tc.address, got, tc.public)
			}
		})
	}
}

func TestClientDialsOnlyValidatedResolvedAddress(t *testing.T) {
	var dials atomic.Int64
	client := NewHTTPClientWithOptions(ClientOptions{
		Timeout: time.Second,
		Resolver: LookupIPAddrFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}),
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, context.Canceled
		},
	})
	defer client.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodGet, "http://public.example/payload", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "no public addresses") {
		t.Fatalf("client.Do error = %v, want non-public-address rejection", err)
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("underlying dialer called %d times for a denied address", got)
	}
}

func TestClientRejectsRedirectToPrivateAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://private.example/secret", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("secret"))
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	resolver := LookupIPAddrFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "private.example" {
			return []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	})
	dialer := &net.Dialer{}
	client := NewHTTPClientWithOptions(ClientOptions{
		Timeout:  time.Second,
		Resolver: resolver,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, serverURL.Host)
		},
	})
	defer client.CloseIdleConnections()

	resp, err := client.Get("http://public.example/start")
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect to a private address unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "no public addresses") {
		t.Fatalf("client.Get error = %v, want private redirect rejection", err)
	}
}

func TestClientDisablesEnvironmentProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")

	dialer := &net.Dialer{}
	var dialAddress string
	client := NewHTTPClientWithOptions(ClientOptions{
		Timeout: time.Second,
		Resolver: LookupIPAddrFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}),
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialAddress = address
			return dialer.DialContext(ctx, network, serverURL.Host)
		},
	})
	defer client.CloseIdleConnections()

	resp, err := client.Get("http://public.example/payload")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if dialAddress != "93.184.216.34:80" {
		t.Fatalf("dial address = %q, want vetted origin address", dialAddress)
	}
}
