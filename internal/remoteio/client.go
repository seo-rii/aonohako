package remoteio

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"
)

const metadataRequestTimeout = 10 * time.Second

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
