package payloadurl

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const fetchSecretURL = "https://storage-secret.example/internal-object?signature=private-token"

type fetchTestTransport func(*http.Request) (*http.Response, error)

func (f fetchTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fetchTestBody struct {
	read  func([]byte) (int, error)
	close func() error
}

func (b fetchTestBody) Read(p []byte) (int, error) { return b.read(p) }
func (b fetchTestBody) Close() error               { return b.close() }

type fetchTemporaryError struct{}

func (fetchTemporaryError) Error() string   { return fetchSecretURL }
func (fetchTemporaryError) Timeout() bool   { return false }
func (fetchTemporaryError) Temporary() bool { return true }

func assertFetchErrorRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected download failure")
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		for _, secret := range []string{"storage-secret", "internal-object", "private-token"} {
			if strings.Contains(cause.Error(), secret) {
				t.Fatalf("download error or its cause leaked %q: %v", secret, err)
			}
		}
	}
}

func TestFetchGETRetriesTransientStatusesAndClosesRejectedBodies(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts, closed := 0, 0
			client := &http.Client{Transport: fetchTestTransport(func(request *http.Request) (*http.Response, error) {
				attempts++
				if request.Method != http.MethodGet || request.Header.Get("User-Agent") != "fetch-test/1" {
					t.Fatalf("request method=%s agent=%q", request.Method, request.Header.Get("User-Agent"))
				}
				if attempts == 1 {
					return &http.Response{StatusCode: status, Status: fetchSecretURL, Body: fetchTestBody{
						read:  func([]byte) (int, error) { t.Error("failed response was drained"); return 0, io.EOF },
						close: func() error { closed++; return errors.New(fetchSecretURL) },
					}}, nil
				}
				if closed != 1 {
					t.Fatal("retry began before closing the failed response")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("payload"))}, nil
			})}
			response, err := FetchGET(context.Background(), client, fetchSecretURL, "fetch-test/1")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err != nil || string(data) != "payload" || attempts != 2 {
				t.Fatalf("payload=%q error=%v attempts=%d", data, err, attempts)
			}
		})
	}
}

func TestFetchGETDoesNotRetryPermanentStatusesAndRedactsReasonPhrase(t *testing.T) {
	for _, status := range []int{301, 400, 401, 403, 404, 501, 505} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts, closed := 0, 0
			client := &http.Client{Transport: fetchTestTransport(func(*http.Request) (*http.Response, error) {
				attempts++
				return &http.Response{StatusCode: status, Status: fetchSecretURL, Body: fetchTestBody{
					read: func([]byte) (int, error) { return 0, io.EOF }, close: func() error { closed++; return nil },
				}}, nil
			})}
			_, err := FetchGET(context.Background(), client, fetchSecretURL, "test")
			assertFetchErrorRedacted(t, err)
			if attempts != 1 || closed != 1 {
				t.Fatalf("attempts=%d closed=%d", attempts, closed)
			}
		})
	}
}

func TestFetchGETExhaustsThreeAttempts(t *testing.T) {
	attempts, closed := 0, 0
	client := &http.Client{Transport: fetchTestTransport(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: 503, Status: fetchSecretURL, Body: fetchTestBody{
			read: func([]byte) (int, error) { return 0, io.EOF }, close: func() error { closed++; return nil },
		}}, nil
	})}
	_, err := FetchGET(context.Background(), client, fetchSecretURL, "test")
	assertFetchErrorRedacted(t, err)
	if !strings.Contains(err.Error(), "HTTP 503") || attempts != 3 || closed != 3 {
		t.Fatalf("error=%v attempts=%d closed=%d", err, attempts, closed)
	}
}

func TestFetchGETClassifiesTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{name: "EOF", err: io.EOF, retry: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, retry: true},
		{name: "reset", err: syscall.ECONNRESET, retry: true},
		{name: "refused", err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, retry: true},
		{name: "temporary network", err: fetchTemporaryError{}, retry: true},
		{name: "temporary DNS", err: &net.DNSError{Err: fetchSecretURL, Name: fetchSecretURL, IsTemporary: true}, retry: true},
		{name: "DNS timeout", err: &net.DNSError{Err: fetchSecretURL, Name: fetchSecretURL, IsTimeout: true}, retry: true},
		{name: "missing DNS", err: &net.DNSError{Err: fetchSecretURL, Name: fetchSecretURL, IsNotFound: true}},
		{name: "certificate", err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: fetchSecretURL}},
		{name: "opaque failure", err: errors.New(fetchSecretURL)},
		{name: "policy", err: policyError("url host has no public addresses")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			client := &http.Client{Transport: fetchTestTransport(func(*http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 || !tc.retry {
					return nil, tc.err
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})}
			response, err := FetchGET(context.Background(), client, fetchSecretURL, "test")
			if tc.retry {
				if err != nil || attempts != 2 {
					t.Fatalf("attempts=%d error=%v", attempts, err)
				}
				response.Body.Close()
			} else {
				assertFetchErrorRedacted(t, err)
				if attempts != 1 {
					t.Fatalf("permanent error retried %d times", attempts)
				}
			}
		})
	}
}

func TestFetchGETRedactsInvalidURLBeforeTransport(t *testing.T) {
	for _, rawURL := range []string{"https://storage-secret.example/internal-object%zz?signature=private-token", "https://storage-secret.example:invalid/internal-object", "https://private-token@storage-secret.example/internal-object"} {
		client := &http.Client{Transport: fetchTestTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid URL reached transport")
			return nil, nil
		})}
		_, err := FetchGET(context.Background(), client, rawURL, "test")
		assertFetchErrorRedacted(t, err)
		assertFetchErrorRedacted(t, Validate(rawURL))
	}
}

func TestFetchGETBodyErrorsAreRedactedWithoutReplay(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: fetchTestTransport(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: 200, Body: fetchTestBody{
			read:  func(p []byte) (int, error) { return copy(p, "prefix"), errors.New(fetchSecretURL) },
			close: func() error { return errors.New(fetchSecretURL) },
		}}, nil
	})}
	response, err := FetchGET(context.Background(), client, fetchSecretURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	assertFetchErrorRedacted(t, err)
	assertFetchErrorRedacted(t, response.Body.Close())
	if string(data) != "prefix" || attempts != 1 {
		t.Fatalf("partial body=%q attempts=%d", data, attempts)
	}
}

func TestFetchGETSharesDeadlineAcrossRetriesAndKeepsBodyContextAlive(t *testing.T) {
	var firstDeadline time.Time
	var bodyContext context.Context
	attempts := 0
	client := &http.Client{Timeout: 5 * time.Second, Transport: fetchTestTransport(func(request *http.Request) (*http.Response, error) {
		attempts++
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Fatal("download deadline missing")
		}
		if attempts == 1 {
			firstDeadline = deadline
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if !deadline.Equal(firstDeadline) {
			t.Fatal("retry reset the download deadline")
		}
		bodyContext = request.Context()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}
	response, err := FetchGET(context.Background(), client, fetchSecretURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if bodyContext.Err() != nil {
		t.Fatal("body context canceled before consumption")
	}
	if data, err := io.ReadAll(response.Body); err != nil || string(data) != "ok" {
		t.Fatalf("body=%q error=%v", data, err)
	}
	if !errors.Is(bodyContext.Err(), context.Canceled) {
		t.Fatal("body EOF did not release download context")
	}
	response.Body.Close()
}

func TestFetchGETCancellationInterruptsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstRequest := make(chan struct{})
	var attempts atomic.Int64
	client := &http.Client{Transport: fetchTestTransport(func(*http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			close(firstRequest)
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	result := make(chan error, 1)
	go func() {
		_, err := FetchGET(ctx, client, fetchSecretURL, "test")
		result <- err
	}()
	<-firstRequest
	cancel()
	select {
	case err := <-result:
		assertFetchErrorRedacted(t, err)
		if !errors.Is(err, context.Canceled) || attempts.Load() != 1 {
			t.Fatalf("error=%v attempts=%d", err, attempts.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt retry backoff")
	}
}

func TestFetchGETRechecksDestinationPolicyOnRetry(t *testing.T) {
	var requests, lookups, dials atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &net.Dialer{}
	client := NewHTTPClientWithOptions(ClientOptions{
		Timeout: 2 * time.Second,
		Resolver: LookupIPAddrFunc(func(context.Context, string) ([]net.IPAddr, error) {
			address := "93.184.216.34"
			if lookups.Add(1) > 1 {
				address = "10.0.0.1"
			}
			return []net.IPAddr{{IP: net.ParseIP(address)}}, nil
		}),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			return dialer.DialContext(ctx, network, target.Host)
		},
	})
	defer client.CloseIdleConnections()
	_, err = FetchGET(context.Background(), client, strings.Replace(fetchSecretURL, "https:", "http:", 1), "test")
	assertFetchErrorRedacted(t, err)
	if !strings.Contains(err.Error(), "no public addresses") || requests.Load() != 1 || dials.Load() != 1 || lookups.Load() != 2 {
		t.Fatalf("error=%v requests=%d dials=%d lookups=%d", err, requests.Load(), dials.Load(), lookups.Load())
	}
}

func TestFetchGETDoesNotRetryRejectedRedirectPolicy(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", fetchSecretURL)
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return fetchTemporaryError{} }
	_, err := FetchGET(context.Background(), client, server.URL, "test")
	assertFetchErrorRedacted(t, err)
	if requests.Load() != 1 {
		t.Fatalf("rejected redirect was retried: %d requests", requests.Load())
	}
}
