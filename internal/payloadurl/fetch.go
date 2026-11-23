package payloadurl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"
)

const fetchAttempts = 3

// FetchGET retries transient failures only until a successful response body is
// returned. The caller's client still owns redirect and destination policy.
// Its timeout covers the whole download, including retries and body reads.
func FetchGET(ctx context.Context, client *http.Client, rawURL, userAgent string) (*http.Response, error) {
	if err := Validate(rawURL); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, policyError("download client is required")
	}
	var cancel context.CancelFunc
	if client.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, client.Timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	returnedBody := false
	defer func() {
		if !returnedBody {
			cancel()
		}
	}()

	for attempt := 0; attempt < fetchAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, safeFetchError("download request failed", err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, policyError("invalid url")
		}
		request.Header.Set("User-Agent", userAgent)
		response, err := client.Do(request)
		if err == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
			response.Body = &fetchBody{body: response.Body, cancel: cancel}
			returnedBody = true
			return response, nil
		}
		if response != nil && response.Body != nil {
			// A failed response can stream indefinitely; do not drain it before
			// retrying. Closing also releases transport resources on the last try.
			_ = response.Body.Close()
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, safeFetchError("download request failed", contextErr)
		}
		if err != nil {
			// Client.Do returns a response alongside an error for rejected
			// redirects. Redirect policy errors are permanent, even when a
			// caller-provided policy happens to implement net.Error.
			if response != nil || !retryableFetchError(err) || attempt == fetchAttempts-1 {
				return nil, safeFetchError("download request failed", err)
			}
		} else if !retryableFetchStatus(response.StatusCode) || attempt == fetchAttempts-1 {
			// Status reason phrases, headers and response bodies are untrusted
			// and may contain signed URLs. Expose only the numeric status code.
			return nil, fmt.Errorf("download returned HTTP %d", response.StatusCode)
		}

		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, safeFetchError("download request failed", ctx.Err())
		case <-timer.C:
		}
	}
	panic("unreachable download retry state")
}

func retryableFetchStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableFetchError(err error) bool {
	var policy policyError
	if errors.Is(err, context.Canceled) || errors.As(err, &policy) {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return !dns.IsNotFound && (dns.IsTimeout || dns.IsTemporary)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH)
}

// safeFetchError deliberately retains only safe sentinel causes, never the
// original transport, DNS, certificate or URL parser error text.
func safeFetchError(message string, err error) error {
	var policy policyError
	if errors.As(err, &policy) {
		return policy
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		if errors.Is(err, cause) {
			return fmt.Errorf("%s: %w", message, cause)
		}
	}
	return errors.New(message)
}

type fetchBody struct {
	body   io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *fetchBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err == nil {
		return n, nil
	}
	b.once.Do(b.cancel)
	if err == io.EOF {
		return n, io.EOF
	}
	return n, safeFetchError("download response read failed", err)
}

func (b *fetchBody) Close() error {
	err := b.body.Close()
	b.once.Do(b.cancel)
	if err != nil {
		return safeFetchError("download response close failed", err)
	}
	return nil
}
