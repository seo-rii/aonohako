package remoteio

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSSEReaderReadsBoundedEvents(t *testing.T) {
	reader := NewSSEReader(strings.NewReader("event: log\ndata: {\"chunk\":\"one\"}\ndata: {\"chunk\":\"two\"}\n\n"))
	event, err := reader.Next()
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if event.Name != "log" || event.Data != "{\"chunk\":\"one\"}\n{\"chunk\":\"two\"}" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after one event, got %v", err)
	}
}

func TestSSEReaderRejectsOversizedLine(t *testing.T) {
	reader := NewSSEReader(strings.NewReader("event: log\ndata: " + strings.Repeat("x", 1025) + "\n\n"))
	reader.maxLineBytes = 1024
	if _, err := reader.Next(); err == nil || !strings.Contains(err.Error(), "sse line too large") {
		t.Fatalf("expected line size error, got %v", err)
	}
}

func TestSSEReaderRejectsOversizedEvent(t *testing.T) {
	chunk := strings.Repeat("x", 1024)
	reader := NewSSEReader(strings.NewReader("event: log\n" + strings.Repeat("data: "+chunk+"\n", 5) + "\n"))
	reader.maxEventBytes = 4 * 1024
	if _, err := reader.Next(); err == nil || !strings.Contains(err.Error(), "sse event too large") {
		t.Fatalf("expected event size error, got %v", err)
	}
}

func TestSSEReaderAcceptsCompileSizedSingleLineResult(t *testing.T) {
	payload := `{"status":"OK","stdout":"` + strings.Repeat("x", 1<<20) + `"}`
	reader := NewSSEReader(strings.NewReader("event: result\ndata: " + payload + "\n\n"))
	event, err := reader.Next()
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if event.Name != "result" || event.Data != payload {
		t.Fatalf("unexpected event: name=%q data length=%d", event.Name, len(event.Data))
	}
}

func TestSSEReaderReportsHeartbeatActivity(t *testing.T) {
	reader := NewSSEReader(strings.NewReader(": heartbeat\n\n"))
	activity := 0
	reader.SetActivityCallback(func() {
		activity++
	})

	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after heartbeat-only stream, got %v", err)
	}
	if activity == 0 {
		t.Fatalf("expected activity callback for heartbeat-only stream")
	}
}
