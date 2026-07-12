package remoteio

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync/atomic"
	"time"
)

const (
	metadataRequestTimeout      = 10 * time.Second
	DefaultRequestUploadTimeout = 30 * time.Second
)

var errRedirectNotAllowed = errors.New("remote redirects are not allowed")

func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport:     newHTTPTransport(http.ProxyFromEnvironment),
		CheckRedirect: rejectRedirect,
	}
}

func NewMetadataHTTPClient() *http.Client {
	return &http.Client{
		Transport:     newHTTPTransport(nil),
		CheckRedirect: rejectRedirect,
		Timeout:       metadataRequestTimeout,
	}
}

func WithRequestUploadTimeout(ctx context.Context, cancel context.CancelFunc, timeout time.Duration) (context.Context, func() bool) {
	if timeout <= 0 {
		timeout = DefaultRequestUploadTimeout
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		cancel()
	})
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
		timer.Stop()
	}}
	return httptrace.WithClientTrace(ctx, trace), func() bool {
		timer.Stop()
		return timedOut.Load()
	}
}

func rejectRedirect(*http.Request, []*http.Request) error {
	return errRedirectNotAllowed
}

func newHTTPTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
	}
}
