package payloadurl

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzValidate checks the SSRF URL sanitizer never panics and that every URL it
// accepts is http(s), carries a host, and has no embedded credentials.
func FuzzValidate(f *testing.F) {
	seeds := []string{
		"", "http://example.com", "https://example.com/a?b=1",
		"ftp://example.com", "http://user:pass@example.com",
		"http://", "https://[::1]/", "HTTP://EXAMPLE.COM",
		"javascript:alert(1)", "file:///etc/passwd", "http://a b",
		"\x00", "http://\x00", "gopher://x", "//example.com",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if err := Validate(raw); err != nil {
			return
		}
		parsed, perr := url.Parse(raw)
		if perr != nil {
			t.Fatalf("Validate accepted %q that url.Parse rejects: %v", raw, perr)
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
		default:
			t.Fatalf("Validate accepted non-http(s) scheme %q in %q", parsed.Scheme, raw)
		}
		if parsed.Hostname() == "" {
			t.Fatalf("Validate accepted %q with empty host", raw)
		}
		if parsed.User != nil {
			t.Fatalf("Validate accepted %q carrying userinfo", raw)
		}
	})
}
