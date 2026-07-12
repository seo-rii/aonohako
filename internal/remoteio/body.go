package remoteio

import (
	"context"
	"fmt"
	"io"
	"time"
)

const (
	DefaultErrorResponseBodyTimeout = 10 * time.Second
	MaxMetadataIdentityTokenBytes   = 1 << 20
	MaxRemoteErrorResponseBytes     = 1 << 20
)

func ReadBoundedBody(body io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response body too large: max %d bytes", maxBytes)
	}
	return data, nil
}

func ReadBoundedBodyWithTimeout(body io.Reader, cancel context.CancelFunc, maxBytes int64, timeout time.Duration) ([]byte, error) {
	timerFired := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		close(timerFired)
		cancel()
	})
	data, err := ReadBoundedBody(body, maxBytes)
	if !timer.Stop() {
		<-timerFired
		return nil, fmt.Errorf("response body read timed out after %s: %w", timeout, context.DeadlineExceeded)
	}
	return data, err
}
