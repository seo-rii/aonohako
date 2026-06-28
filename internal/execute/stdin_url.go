package execute

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const stdinURLDownloadTimeout = 60 * time.Second

var stdinURLHTTPClient = &http.Client{
	Timeout: stdinURLDownloadTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return validateStdinURL(req.URL.String())
	},
}

func openStdinURL(ctx context.Context, rawURL string, maxBytes int64) (io.ReadCloser, error) {
	if err := validateStdinURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "aonohako-stdin-url/1")
	resp, err := stdinURLHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download returned %s", resp.Status)
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download too large: max %d bytes", maxBytes)
	}
	return resp.Body, nil
}

func validateStdinURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("url scheme must be http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("url host is required")
	}
	return nil
}
