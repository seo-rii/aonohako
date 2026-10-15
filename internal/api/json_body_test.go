package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/model"
)

func jsonBodyServerForTest(t *testing.T, authMode string) *Server {
	t.Helper()
	cfg := configForTest(t)
	if authMode == "bearer" {
		cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthBearer, BearerToken: "body-test-token"}
	} else {
		cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthPlatform, PlatformPrincipalHMACSecret: "body-test-secret"}
	}
	cfg.MaxPrincipalRequestsPerMinute = 0
	return NewWithServices(cfg, compileRunnerStub{run: func(context.Context, *model.CompileRequest) model.CompileResponse {
		t.Error("invalid body reached the compile runner")
		return model.CompileResponse{}
	}}, executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
		t.Error("invalid body reached the execute runner")
		return model.RunResponse{}
	}})
}

func setJSONBodyAuthForTest(t *testing.T, req *http.Request, authMode string, body io.Reader, nonceID int) {
	t.Helper()
	if authMode == "bearer" {
		req.Header.Set("Authorization", "Bearer body-test-token")
		return
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, body); err != nil {
		t.Fatalf("hash test request body: %v", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	nonce := platformPrincipalNonceForTest(nonceID)
	mac := hmac.New(sha256.New, []byte("body-test-secret"))
	_, _ = io.WriteString(mac, req.Method+"\n"+req.URL.RequestURI()+"\nbody-test-principal\n"+timestamp+"\n"+nonce+"\n"+hex.EncodeToString(digest.Sum(nil)))
	req.Header.Set(platformPrincipalHeader, "body-test-principal")
	req.Header.Set(platformPrincipalTimestampHeader, timestamp)
	req.Header.Set(platformPrincipalNonceHeader, nonce)
	req.Header.Set(platformPrincipalSignatureHeader, "v4="+hex.EncodeToString(mac.Sum(nil)))
}

func assertJSONBodyError(t *testing.T, resp *http.Response, wantStatus int, wantCode string) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d", resp.StatusCode, wantStatus)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if payload["error"] != wantCode || payload["message"] == "" {
		t.Fatalf("error envelope = %#v, want code %q and message", payload, wantCode)
	}
}

func TestJSONBodyErrorsMatchAcrossEndpointsAndAuth(t *testing.T) {
	for _, endpoint := range []string{"/compile", "/execute"} {
		for _, authMode := range []string{"bearer", "platform-hmac"} {
			t.Run(endpoint+"/"+authMode, func(t *testing.T) {
				s := jsonBodyServerForTest(t, authMode)
				for i, tc := range []struct {
					name string
					body string
					code string
				}{
					{name: "empty", code: "invalid_json"},
					{name: "incomplete", body: "{", code: "invalid_json"},
					{name: "syntax", body: "{invalid", code: "invalid_json"},
					{name: "type", body: `{"lang":42}`, code: "invalid_json_type"},
					{name: "unknown field", body: `{"unexpected":true}`, code: "unknown_json_field"},
					{name: "second empty object", body: `{} {}`, code: "unexpected_trailing_json"},
					{name: "second unknown object", body: `{} {"unexpected":true}`, code: "unexpected_trailing_json"},
					{name: "second array", body: `{} []`, code: "unexpected_trailing_json"},
					{name: "second scalar", body: `{} true`, code: "unexpected_trailing_json"},
					{name: "second null", body: `{} null`, code: "unexpected_trailing_json"},
					{name: "malformed trailing data", body: `{} invalid`, code: "invalid_json"},
				} {
					t.Run(tc.name, func(t *testing.T) {
						req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(tc.body))
						setJSONBodyAuthForTest(t, req, authMode, strings.NewReader(tc.body), i+1)
						rr := httptest.NewRecorder()
						s.Handler().ServeHTTP(rr, req)
						assertJSONBodyError(t, rr.Result(), http.StatusBadRequest, tc.code)
						active, pending := s.queue.Snapshot()
						if active != 0 || pending != 0 || s.uploads.Load() != 0 || s.streams.Load() != 0 {
							t.Fatal("invalid body retained admission or entered the run queue")
						}
					})
				}
			})
		}
	}
}

type jsonSpaceReader struct{ remaining int64 }

func (r *jsonSpaceReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.remaining))
	for i := range p[:n] {
		p[i] = ' '
	}
	r.remaining -= int64(n)
	return n, nil
}

func sizedJSONBodyForTest(size int64, trailingWhitespace bool) io.Reader {
	prefix, suffix := `{"problem_id":"`, `"}`
	if trailingWhitespace {
		prefix, suffix = `{}`, ""
	}
	return io.MultiReader(strings.NewReader(prefix), &jsonSpaceReader{remaining: size - int64(len(prefix)+len(suffix))}, strings.NewReader(suffix))
}

func TestAuthenticatedJSONBodySizeErrorsMatch(t *testing.T) {
	for _, endpoint := range []string{"/compile", "/execute"} {
		for _, authMode := range []string{"bearer", "platform-hmac"} {
			for _, chunked := range []bool{false, true} {
				transfer := "content-length"
				if chunked {
					transfer = "chunked"
				}
				t.Run(endpoint+"/"+authMode+"/"+transfer, func(t *testing.T) {
					s := jsonBodyServerForTest(t, authMode)
					seenChunked := make(chan bool, 1)
					handler := s.Handler()
					ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						seenChunked <- r.ContentLength == -1 && len(r.TransferEncoding) == 1 && r.TransferEncoding[0] == "chunked"
						handler.ServeHTTP(w, r)
					}))
					defer ts.Close()
					const size = int64(maxJSONBodyBytes) + 1
					// Exercise overflow both inside a field and while checking trailing whitespace.
					body := sizedJSONBodyForTest(size, chunked)
					req, err := http.NewRequest(http.MethodPost, ts.URL+endpoint, body)
					if err != nil {
						t.Fatal(err)
					}
					if !chunked {
						req.ContentLength = size
					}
					setJSONBodyAuthForTest(t, req, authMode, sizedJSONBodyForTest(size, chunked), 1)
					client := ts.Client()
					client.Timeout = 30 * time.Second
					resp, err := client.Do(req)
					if err != nil {
						t.Fatalf("send oversized request: %v", err)
					}
					assertJSONBodyError(t, resp, http.StatusRequestEntityTooLarge, "request_body_too_large")
					if got := <-seenChunked; got != chunked {
						t.Fatalf("server observed chunked=%v, want %v", got, chunked)
					}
				})
			}
		}
	}
}

func TestDecodeJSONBodySizeBoundaryIncludesTrailingWhitespace(t *testing.T) {
	for _, delta := range []int64{-1, 0, 1} {
		req := httptest.NewRequest(http.MethodPost, "/execute", sizedJSONBodyForTest(int64(maxJSONBodyBytes)+delta, true))
		var decoded model.RunRequest
		err := decodeJSONBody(httptest.NewRecorder(), req, &decoded)
		if delta <= 0 {
			if err != nil {
				t.Fatalf("size limit %+d: unexpected error: %v", delta, err)
			}
			if req.Body != http.NoBody {
				t.Fatal("successful boundary decode retained the raw body")
			}
		} else {
			var maxErr *http.MaxBytesError
			if !errors.As(err, &maxErr) {
				t.Fatalf("size limit +1: error = %v, want MaxBytesError", err)
			}
		}
	}
}

func TestJSONBodyAuthenticationRejectsBeforeReading(t *testing.T) {
	for _, endpoint := range []string{"/compile", "/execute"} {
		for _, authMode := range []string{"bearer", "platform-hmac"} {
			t.Run(endpoint+"/"+authMode, func(t *testing.T) {
				s := jsonBodyServerForTest(t, authMode)
				body := &readCountingBody{}
				req := httptest.NewRequest(http.MethodPost, endpoint, body)
				req.ContentLength = maxJSONBodyBytes + 1
				req.Header.Set("Authorization", "Bearer wrong-token")
				req.Header.Set(platformPrincipalHeader, "body-test-principal")
				req.Header.Set(platformPrincipalSignatureHeader, "v4=invalid")
				rr := httptest.NewRecorder()
				s.Handler().ServeHTTP(rr, req)
				assertJSONBodyError(t, rr.Result(), http.StatusUnauthorized, "unauthorized")
				if body.reads != 0 {
					t.Fatalf("rejected credentials caused %d body reads", body.reads)
				}
			})
		}
	}
}

func TestJSONBodyReadFailuresUseCommonEnvelope(t *testing.T) {
	for _, endpoint := range []string{"/compile", "/execute"} {
		for _, authMode := range []string{"bearer", "platform-hmac"} {
			t.Run(endpoint+"/"+authMode, func(t *testing.T) {
				s := jsonBodyServerForTest(t, authMode)
				body := &readCountingBody{}
				req := httptest.NewRequest(http.MethodPost, endpoint, body)
				setJSONBodyAuthForTest(t, req, authMode, strings.NewReader(`{}`), 1)
				rr := httptest.NewRecorder()
				s.Handler().ServeHTTP(rr, req)
				assertJSONBodyError(t, rr.Result(), http.StatusBadRequest, "invalid_request_body")
				if body.reads == 0 {
					t.Fatal("request body error was returned before attempting to read it")
				}
			})
		}
	}
}

func TestJSONBodyOversizedContentLengthRejectedBeforeReading(t *testing.T) {
	for _, endpoint := range []string{"/compile", "/execute"} {
		for _, authMode := range []string{"bearer", "platform-hmac"} {
			t.Run(endpoint+"/"+authMode, func(t *testing.T) {
				s := jsonBodyServerForTest(t, authMode)
				body := &readCountingBody{}
				req := httptest.NewRequest(http.MethodPost, endpoint, body)
				req.ContentLength = maxJSONBodyBytes + 1
				setJSONBodyAuthForTest(t, req, authMode, strings.NewReader(`{}`), 1)
				rr := httptest.NewRecorder()
				s.Handler().ServeHTTP(rr, req)
				assertJSONBodyError(t, rr.Result(), http.StatusRequestEntityTooLarge, "request_body_too_large")
				if body.reads != 0 {
					t.Fatalf("over-limit Content-Length caused %d body reads", body.reads)
				}
			})
		}
	}
}
