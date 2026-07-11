package execute

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"aonohako/internal/payloadurl"
)

const stdinURLDownloadTimeout = 60 * time.Second

var stdinURLHTTPClient = payloadurl.NewHTTPClient(stdinURLDownloadTimeout)

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
	return payloadurl.Validate(rawURL)
}
