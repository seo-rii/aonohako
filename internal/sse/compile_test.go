package sse

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"aonohako/internal/compilewire"
	"aonohako/internal/model"
)

type compileCountingWriter struct {
	header http.Header
	bytes  int
}

func (w *compileCountingWriter) Header() http.Header            { return w.header }
func (*compileCountingWriter) WriteHeader(int)                  {}
func (*compileCountingWriter) Flush()                           {}
func (*compileCountingWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *compileCountingWriter) Write(p []byte) (int, error) {
	w.bytes += len(p)
	return len(p), nil
}

func TestCompileWriterReservesResultAndBoundsAuxiliaryEvents(t *testing.T) {
	w := &compileCountingWriter{header: make(http.Header)}
	stream, err := NewCompile(w, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("\x00", compilewire.OutputBytes)
	for _, event := range []string{"log", "log", "error"} {
		if err := stream.Event(event, map[string]string{"chunk": chunk}); err != nil {
			t.Fatalf("maximum diagnostics: %v", err)
		}
	}
	// Continued heartbeats must consume a finite auxiliary allowance without
	// taking the result reservation. No response body is retained by this test.
	heartbeat := map[string]string{"ts": strings.Repeat("x", 1<<20)}
	for {
		before := w.bytes
		if err := stream.Event("heartbeat", heartbeat); err != nil {
			if !strings.Contains(err.Error(), "auxiliary") || w.bytes != before {
				t.Fatalf("auxiliary rejection: err=%v before=%d after=%d", err, before, w.bytes)
			}
			break
		}
	}
	result := strings.Repeat("x", compilewire.SSEEventBytes-2)
	if err := stream.Event("result", result); err != nil {
		t.Fatalf("reserved maximum result: %v", err)
	}
	if w.bytes > compilewire.SSEStreamBytes {
		t.Fatalf("stream emitted %d bytes, limit %d", w.bytes, compilewire.SSEStreamBytes)
	}
	before := w.bytes
	if err := stream.Event("heartbeat", map[string]int{"ts": 1}); err != nil || w.bytes != before {
		t.Fatalf("heartbeat after final result: error=%v bytes=%d", err, w.bytes-before)
	}
}

func TestCompileWriterRejectsOversizedResultBeforeWriting(t *testing.T) {
	w := &compileCountingWriter{header: make(http.Header)}
	stream, err := NewCompile(w, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// JSON quoting adds two bytes beyond the exact event allowance.
	err = stream.Event("result", strings.Repeat("\x00", compilewire.SSEEventBytes/6))
	if err == nil || !strings.Contains(err.Error(), "exceeds wire budget") || w.bytes != 0 {
		t.Fatalf("oversize result: err=%v bytes=%d", err, w.bytes)
	}
	if err := stream.Event("result", model.CompileResponse{Status: model.CompileStatusInternal, Reason: "result too large"}); err != nil {
		t.Fatalf("bounded error result: %v", err)
	}
}

func TestCompileWriterDeadlineFailureStopsHeartbeats(t *testing.T) {
	w := newDeadlineResponseWriter()
	stream, err := NewCompile(w, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.blockWrites = true
	if err := stream.Event("log", "blocked"); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("blocked Event: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := stream.Heartbeat(ctx, time.Millisecond); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("failed writer heartbeat: %v", err)
	}
}
