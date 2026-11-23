package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/execute"
	"aonohako/internal/model"
)

type payloadRetryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f payloadRetryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type payloadReadError struct{ err error }

func (r payloadReadError) Read([]byte) (int, error) { return 0, r.err }
func (payloadReadError) Close() error               { return nil }

func TestDownloadPayloadURLRetriesBeforeReturningContent(t *testing.T) {
	var requests atomic.Int64
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 3 {
			http.Error(w, "temporary storage failure", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "complete payload")
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)
	payloadURLHTTPClient.Timeout = 5 * time.Second

	data, err := downloadPayloadURL(context.Background(), "http://storage.example/private-object", 32)
	if err != nil || string(data) != "complete payload" || requests.Load() != 3 {
		t.Fatalf("download: payload=%q error=%v requests=%d", data, err, requests.Load())
	}
}

func TestPayloadURLErrorsDoNotExposeStorageDetails(t *testing.T) {
	const rawURL = "http://storage.example/judge-data/1137/private-object?signature=secret-token"
	for _, endpoint := range []string{"/compile", "/execute"} {
		for _, failure := range []string{"status", "body", "malformed"} {
			t.Run(endpoint+"/"+failure, func(t *testing.T) {
				var requests, runnerCalls atomic.Int64
				previous := payloadURLHTTPClient
				payloadURLHTTPClient = &http.Client{
					Timeout: 5 * time.Second,
					Transport: payloadRetryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
						requests.Add(1)
						response := &http.Response{
							StatusCode:    http.StatusServiceUnavailable,
							Status:        "503 " + rawURL,
							Header:        make(http.Header),
							Body:          io.NopCloser(strings.NewReader("private upstream response: " + rawURL)),
							ContentLength: -1, Request: req,
						}
						if failure == "body" {
							response.StatusCode = http.StatusOK
							response.Status = "200 OK"
							response.Body = payloadReadError{err: errors.New("upstream read failed: " + rawURL)}
						}
						return response, nil
					}),
				}
				t.Cleanup(func() { payloadURLHTTPClient = previous })
				server := NewWithServices(configForTest(t), compileRunnerStub{run: func(context.Context, *model.CompileRequest) model.CompileResponse {
					runnerCalls.Add(1)
					return model.CompileResponse{Status: model.CompileStatusOK}
				}}, executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
					runnerCalls.Add(1)
					return model.RunResponse{Status: model.RunStatusAccepted}
				}})
				assetURL := rawURL
				if failure == "malformed" {
					assetURL = strings.Replace(rawURL, "private-object", "%zz", 1)
				}
				var payload any = model.CompileRequest{Lang: "CPP17", Sources: []model.Source{{Name: "Main.cpp", DataURL: assetURL}}}
				if endpoint == "/execute" {
					payload = model.RunRequest{
						Lang: "binary", Binaries: []model.Binary{{Name: "Main", DataB64: "eA==", Mode: "exec"}},
						ExpectedStdoutURL: assetURL, Limits: model.Limits{TimeMs: 1000, MemoryMB: 64},
					}
				}
				body, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				recorder := httptest.NewRecorder()
				server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body)))
				if recorder.Code != http.StatusBadRequest || runnerCalls.Load() != 0 {
					t.Fatalf("failed payload reached runner: status=%d runnerCalls=%d", recorder.Code, runnerCalls.Load())
				}
				var envelope map[string]string
				if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope["error"] != "invalid_payload_url" {
					t.Fatalf("unexpected error response: %s (%v)", recorder.Body.String(), err)
				}
				for _, secret := range []string{"storage.example", "judge-data", "1137", "private-object", "secret-token", "signature", "private upstream response"} {
					if strings.Contains(recorder.Body.String(), secret) {
						t.Fatalf("public error leaked %q: %s", secret, recorder.Body.String())
					}
				}
				wantRequests := int64(3)
				if failure == "body" {
					wantRequests = 1
				} else if failure == "malformed" {
					wantRequests = 0
				}
				if requests.Load() != wantRequests {
					t.Fatalf("download requests=%d, want %d", requests.Load(), wantRequests)
				}
			})
		}
	}
}
