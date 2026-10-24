package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aonohako/internal/execute"
	"aonohako/internal/model"
)

type sseInitResponseWriter struct {
	*deadlineResponseRecorder
	flushErr error
	clearErr error
}

func (w *sseInitResponseWriter) FlushError() error {
	if w.flushErr != nil {
		return w.flushErr
	}
	w.Flush()
	return nil
}

func (w *sseInitResponseWriter) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		return w.clearErr
	}
	return nil
}

type sseMiddlewareWriter struct{ http.ResponseWriter }

func (w sseMiddlewareWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Expose deadlines without exposing a flush method or an unwrap chain.
type sseNoFlushWriter struct{ http.ResponseWriter }

func (sseNoFlushWriter) SetWriteDeadline(time.Time) error { return nil }

func TestSSEInitializationResponseBoundary(t *testing.T) {
	for _, path := range []string{"/compile", "/execute"} {
		for _, mode := range []string{"wrapped", "no flush", "flush failure", "clear deadline failure"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				s := newServerForTest(t)
				called := false
				s.compile = compileRunnerStub{run: func(context.Context, *model.CompileRequest) model.CompileResponse {
					called = true
					return model.CompileResponse{Status: model.CompileStatusOK}
				}}
				s.execute = executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
					called = true
					return model.RunResponse{Status: model.RunStatusAccepted}
				}}
				body := executePayload(t)
				if path == "/compile" {
					var err error
					body, err = json.Marshal(map[string]any{
						"lang": "PYTHON3", "sources": []map[string]any{{"name": "Main.py", "data_b64": base64.StdEncoding.EncodeToString([]byte("print('ok')\n"))}},
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				w := &sseInitResponseWriter{deadlineResponseRecorder: &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}}
				if mode == "flush failure" {
					w.flushErr = errors.New("flush failed")
				}
				if mode == "clear deadline failure" {
					w.clearErr = errors.New("deadline failed")
				}
				var responseWriter http.ResponseWriter = w
				if mode == "no flush" {
					responseWriter = sseNoFlushWriter{responseWriter}
				}
				s.Handler().ServeHTTP(sseMiddlewareWriter{sseMiddlewareWriter{responseWriter}}, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
				wantStatus, wantContentType := http.StatusOK, "text/event-stream"
				if mode == "no flush" {
					wantStatus, wantContentType = http.StatusInternalServerError, "application/json"
				}
				if w.Code != wantStatus || w.Header().Get("Content-Type") != wantContentType {
					t.Fatalf("response = %d %q", w.Code, w.Header().Get("Content-Type"))
				}
				if mode == "wrapped" {
					if !called || !strings.Contains(w.Body.String(), "event: result\n") {
						t.Fatalf("wrapped response = %q, runner called = %v", w.Body.String(), called)
					}
				} else if mode == "no flush" {
					var payload map[string]string
					if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
						t.Fatal(err)
					}
					if called || payload["error"] != "stream_init_failed" || payload["message"] == "" {
						t.Fatalf("unsupported writer response = %q, runner called = %v", w.Body.String(), called)
					}
				} else if called || w.Body.Len() != 0 {
					t.Fatalf("failed initialization ran service or appended a body: called=%v, body=%q", called, w.Body.String())
				}
				active, pending := s.queue.Snapshot()
				if active != 0 || pending != 0 || s.streams.Load() != 0 || s.uploads.Load() != 0 {
					t.Fatalf("leaked admission: active=%d pending=%d streams=%d uploads=%d", active, pending, s.streams.Load(), s.uploads.Load())
				}
			})
		}
	}
}
