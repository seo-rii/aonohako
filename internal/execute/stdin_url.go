package execute

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"aonohako/internal/payloadurl"
)

const stdinURLDownloadTimeout = 60 * time.Second

var stdinURLHTTPClient = payloadurl.NewHTTPClient(stdinURLDownloadTimeout)

type stdinURLDownloadBudget struct {
	remaining time.Duration
}

type stdinURLDownloadBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

func (b *stdinURLDownloadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.finish)
	}
	return n, err
}

func (b *stdinURLDownloadBody) Close() error {
	b.once.Do(b.finish)
	return b.ReadCloser.Close()
}

func openStdinURL(ctx context.Context, rawURL string, maxBytes int64, budget *stdinURLDownloadBudget) (io.ReadCloser, error) {
	if err := validateStdinURL(rawURL); err != nil {
		return nil, err
	}
	downloadCtx := ctx
	finishDownload := func() {}
	if budget != nil {
		if budget.remaining <= 0 {
			return nil, context.DeadlineExceeded
		}
		started := time.Now()
		var cancel context.CancelFunc
		downloadCtx, cancel = context.WithTimeout(ctx, budget.remaining)
		finishDownload = func() {
			elapsed := time.Since(started)
			if elapsed >= budget.remaining {
				budget.remaining = 0
			} else {
				budget.remaining -= elapsed
			}
			cancel()
		}
	}
	resp, err := payloadurl.FetchGET(downloadCtx, stdinURLHTTPClient, rawURL, "aonohako-stdin-url/1")
	if err != nil {
		finishDownload()
		return nil, err
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		_ = resp.Body.Close()
		finishDownload()
		return nil, fmt.Errorf("download too large: max %d bytes", maxBytes)
	}
	if budget != nil {
		return &stdinURLDownloadBody{ReadCloser: resp.Body, finish: finishDownload}, nil
	}
	return resp.Body, nil
}

func validateStdinURL(rawURL string) error {
	return payloadurl.Validate(rawURL)
}
