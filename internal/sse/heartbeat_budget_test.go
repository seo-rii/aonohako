package sse

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/remoteio"
)

type countingResponseWriter struct {
	header http.Header
	bytes  int
}

func (w *countingResponseWriter) Header() http.Header              { return w.header }
func (w *countingResponseWriter) WriteHeader(int)                  {}
func (w *countingResponseWriter) Flush()                           {}
func (w *countingResponseWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *countingResponseWriter) Write(p []byte) (int, error) {
	w.bytes += len(p)
	return len(p), nil
}

func TestHeartbeatSurvivesEscapedDisplayBudget(t *testing.T) {
	w := &countingResponseWriter{header: make(http.Header)}
	stream, err := New(w, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// A first legacy step can leave almost 48 MiB of escaped logs before the
	// second step starts. Image MIME strings are request-owned JSON text: '<'
	// occupies one source byte but six bytes when re-encoded for SSE.
	if err := stream.Event("log", map[string]any{"stream": "stdout", "chunk": strings.Repeat("\x00", (8<<20)-128)}); err != nil {
		t.Fatal(err)
	}
	if w.bytes == 0 {
		t.Fatal("initial log was unexpectedly discarded")
	}
	for range 2 {
		if err := stream.Event("image", map[string]any{"mime": strings.Repeat("<", (1<<20)-128), "b64": "AA==", "ts": 1}); err != nil {
			t.Fatal(err)
		}
	}
	// This final image would consume exactly all 64 MiB of auxiliary space.
	// Each image source line is below 1 MiB and the three source lines use
	// less than the ordinary 8 MiB image-file capture limit.
	auxLimit := remoteio.DefaultSSEStreamBytes - remoteio.DefaultSSELineBytes
	emptyImage, _ := json.Marshal(map[string]any{"mime": "", "b64": "AA==", "ts": 1})
	remaining := auxLimit - w.bytes - len(emptyImage) - len("event: image\ndata: \n\n")
	mime := strings.Repeat("<", remaining/6) + strings.Repeat("a", remaining%6)
	if err := stream.Event("image", map[string]any{"mime": mime, "b64": "AA==", "ts": 1}); err != nil {
		t.Fatal(err)
	}
	beforeError := w.bytes
	if err := stream.Event("error", map[string]string{"message": strings.Repeat("\x00", 512<<10)}); err != nil {
		t.Fatal(err)
	}
	if w.bytes != beforeError {
		t.Fatal("large error diagnostic consumed the reserved heartbeat allowance")
	}
	// Cover the longest computed communication deadline even at the minimum
	// production heartbeat interval of one second (44,970 seconds).
	heartbeat := map[string]any{"ts": time.Now().UnixMilli()}
	for i := range 50_000 {
		before := w.bytes
		if err := stream.Event("heartbeat", heartbeat); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
		if w.bytes == before {
			t.Fatalf("heartbeat %d was omitted after escaped display frames filled the budget", i)
		}
	}
	beforeResult := w.bytes
	if err := stream.Event("result", model.RunResponse{Status: model.RunStatusAccepted}); err != nil {
		t.Fatal(err)
	}
	if w.bytes == beforeResult || w.bytes > remoteio.DefaultSSEStreamBytes {
		t.Fatalf("result missing or stream exceeds wire limit: %d bytes", w.bytes)
	}
}

func TestHeartbeatBudgetExhaustionReturnsError(t *testing.T) {
	w := &countingResponseWriter{header: make(http.Header)}
	stream, err := New(w, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Arbitrarily frequent or nonstandard heartbeats still need a hard bound.
	// Exhaustion must stop the stream explicitly instead of returning success
	// while making it impossible to reset the downstream idle timer.
	heartbeat := map[string]string{"ts": strings.Repeat("a", 1<<20)}
	for range 65 {
		before := w.bytes
		if err := stream.Event("heartbeat", heartbeat); err != nil {
			if w.bytes != before || w.bytes > remoteio.DefaultSSEStreamBytes {
				t.Fatal("exhausted heartbeat wrote beyond its wire budget")
			}
			return
		}
		if w.bytes == before {
			t.Fatal("heartbeat budget exhaustion was silently ignored")
		}
	}
	t.Fatal("heartbeat stream was not bounded")
}
