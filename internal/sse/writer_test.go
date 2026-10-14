package sse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

type deadlineResponseWriter struct {
	header http.Header

	mu          sync.Mutex
	body        bytes.Buffer
	deadline    time.Time
	blockWrites bool
	writes      int
}

type unwrapResponseWriter struct{ http.ResponseWriter }

func (w unwrapResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// This writer deliberately exposes FlushError without implementing Flusher.
type flushErrorResponseWriter struct {
	w        *deadlineResponseWriter
	flushErr error
	clearErr error
}

func (w *flushErrorResponseWriter) Header() http.Header         { return w.w.Header() }
func (w *flushErrorResponseWriter) WriteHeader(status int)      { w.w.WriteHeader(status) }
func (w *flushErrorResponseWriter) Write(p []byte) (int, error) { return w.w.Write(p) }
func (w *flushErrorResponseWriter) FlushError() error           { return w.flushErr }
func (w *flushErrorResponseWriter) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() && w.clearErr != nil {
		return w.clearErr
	}
	return w.w.SetWriteDeadline(deadline)
}

type noFlushDeadlineWriter struct{ http.ResponseWriter }

func (noFlushDeadlineWriter) SetWriteDeadline(time.Time) error { return nil }

func TestNewSupportsResponseControllerWriters(t *testing.T) {
	for _, kind := range []string{"direct", "unwrap", "multiple unwrap", "FlushError", "wrapped FlushError"} {
		t.Run(kind, func(t *testing.T) {
			base := newDeadlineResponseWriter()
			var w http.ResponseWriter = base
			switch kind {
			case "unwrap":
				w = unwrapResponseWriter{w}
			case "multiple unwrap":
				w = unwrapResponseWriter{unwrapResponseWriter{w}}
			case "FlushError":
				w = &flushErrorResponseWriter{w: base}
			case "wrapped FlushError":
				w = unwrapResponseWriter{&flushErrorResponseWriter{w: base}}
			}
			stream, err := New(w, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Event("result", map[string]string{"status": "OK"}); err != nil {
				t.Fatal(err)
			}
			if got := base.body.String(); got != "event: result\ndata: {\"status\":\"OK\"}\n\n" {
				t.Fatalf("body = %q", got)
			}
			if !base.deadline.IsZero() {
				t.Fatal("successful event retained a write deadline")
			}
		})
	}
}

func TestNewMarksOnlyCommittedInitializationFailures(t *testing.T) {
	failure := errors.New("transport failed")
	for _, tc := range []struct {
		name    string
		writer  http.ResponseWriter
		want    error
		started bool
	}{
		{"no deadlines", httptest.NewRecorder(), http.ErrNotSupported, false},
		{"no flush", noFlushDeadlineWriter{httptest.NewRecorder()}, http.ErrNotSupported, true},
		{"flush error", &flushErrorResponseWriter{w: newDeadlineResponseWriter(), flushErr: failure}, failure, true},
		{"clear deadline error", &flushErrorResponseWriter{w: newDeadlineResponseWriter(), clearErr: failure}, failure, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := New(tc.writer, time.Second)
			if stream != nil || !errors.Is(err, tc.want) {
				t.Fatalf("New = %v, %v; want error %v", stream, err, tc.want)
			}
			if ResponseStarted(fmt.Errorf("wrapped: %w", err)) != tc.started {
				t.Fatalf("ResponseStarted(%v) = %v, want %v", err, ResponseStarted(err), tc.started)
			}
		})
	}
	if ResponseStarted(nil) {
		t.Fatal("nil error reports a started response")
	}
}

func newDeadlineResponseWriter() *deadlineResponseWriter {
	return &deadlineResponseWriter{header: make(http.Header)}
}

func (w *deadlineResponseWriter) Header() http.Header {
	return w.header
}

func (w *deadlineResponseWriter) WriteHeader(int) {}

func (w *deadlineResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	if !w.blockWrites {
		n, err := w.body.Write(p)
		w.mu.Unlock()
		return n, err
	}
	deadline := w.deadline
	w.mu.Unlock()
	if deadline.IsZero() {
		return 0, errors.New("blocking write has no deadline")
	}
	timer := time.NewTimer(max(0, time.Until(deadline)))
	defer timer.Stop()
	<-timer.C
	return 0, os.ErrDeadlineExceeded
}

func (w *deadlineResponseWriter) Flush() {}

func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	return nil
}

func TestEventWritesAndClearsPerEventDeadline(t *testing.T) {
	w := newDeadlineResponseWriter()
	stream, err := New(w, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Event("result", map[string]string{"status": "OK"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * stream.writeTimeout)
	if err := stream.Event("progress", map[string]string{"stage": "still-open"}); err != nil {
		t.Fatalf("event after an idle interval longer than the write timeout: %v", err)
	}

	w.mu.Lock()
	body, deadline := w.body.String(), w.deadline
	w.mu.Unlock()
	if body != "event: result\ndata: {\"status\":\"OK\"}\n\nevent: progress\ndata: {\"stage\":\"still-open\"}\n\n" {
		t.Fatalf("event body = %q", body)
	}
	if !deadline.IsZero() {
		t.Fatalf("successful event retained write deadline %v", deadline)
	}
}

func TestEventDeadlineFailsWriterAndStopsHeartbeat(t *testing.T) {
	w := newDeadlineResponseWriter()
	stream, err := New(w, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.blockWrites = true
	w.mu.Unlock()

	started := time.Now()
	err = stream.Event("log", map[string]string{"chunk": "blocked"})
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Event error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked Event returned after %v", elapsed)
	}

	w.mu.Lock()
	writesBeforeRetry := w.writes
	w.mu.Unlock()
	if err := stream.Event("result", map[string]string{"status": "OK"}); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("second Event error = %v, want retained deadline failure", err)
	}
	w.mu.Lock()
	writesAfterRetry := w.writes
	w.mu.Unlock()
	if writesAfterRetry != writesBeforeRetry {
		t.Fatalf("failed writer retried response writes: before=%d after=%d", writesBeforeRetry, writesAfterRetry)
	}

	heartbeatDone := make(chan error, 1)
	go func() {
		heartbeatDone <- stream.Heartbeat(context.Background(), time.Millisecond)
	}()
	select {
	case err := <-heartbeatDone:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("Heartbeat error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Heartbeat did not stop after the writer failed")
	}
}
