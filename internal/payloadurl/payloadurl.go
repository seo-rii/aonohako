package payloadurl

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxRedirects = 10

var deniedPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // current network
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space (CGNAT)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // deprecated 6to4 relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("64:ff9b::/96"),    // IPv4/IPv6 translation
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use IPv4/IPv6 translation
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4 translation
	netip.MustParsePrefix("3fff::/20"),       // documentation
}

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type LookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)

func (f LookupIPAddrFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

type ClientOptions struct {
	Timeout     time.Duration
	Resolver    Resolver
	DialContext DialContextFunc
}

func NewHTTPClient(timeout time.Duration) *http.Client {
	return NewHTTPClientWithOptions(ClientOptions{Timeout: timeout})
}

func NewHTTPClientWithOptions(options ClientOptions) *http.Client {
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialContext := options.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dialContext = dialer.DialContext
	}

	transport := &http.Transport{
		// A proxy would replace the destination passed to DialContext with the
		// proxy address and bypass destination filtering. Payload downloads are
		// therefore always direct.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid url address: %w", err)
			}
			resolved, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve url host: %w", err)
			}
			if len(resolved) == 0 {
				return nil, fmt.Errorf("url host resolved to no addresses")
			}

			var lastErr error
			sawPublic := false
			for _, candidate := range resolved {
				addr, ok := netip.AddrFromSlice(candidate.IP)
				if !ok {
					continue
				}
				addr = addr.Unmap().WithZone("")
				if !isPublicAddress(addr) {
					lastErr = fmt.Errorf("url host resolved to a non-public address")
					continue
				}
				sawPublic = true
				conn, dialErr := dialContext(ctx, network, net.JoinHostPort(addr.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			if !sawPublic {
				return nil, fmt.Errorf("url host has no public addresses: %w", lastErr)
			}
			return nil, fmt.Errorf("dial url host: %w", lastErr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   options.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects")
			}
			return Validate(req.URL.String())
		},
	}
}

func Validate(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("url scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("url host is required")
	}
	if parsed.User != nil {
		return fmt.Errorf("url credentials are not allowed")
	}
	return nil
}

func isPublicAddress(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return false
	}
	for _, prefix := range deniedPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
