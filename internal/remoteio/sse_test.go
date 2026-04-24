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
	reader := NewSSEReader(strings.NewReader("event: log\ndata: " + strings.Repeat("x", DefaultSSELineBytes+1) + "\n\n"))
	if _, err := reader.Next(); err == nil || !strings.Contains(err.Error(), "sse line too large") {
		t.Fatalf("expected line size error, got %v", err)
	}
}

func TestSSEReaderRejectsOversizedEvent(t *testing.T) {
	chunk := strings.Repeat("x", 1024)
	reader := NewSSEReader(strings.NewReader("event: log\n" + strings.Repeat("data: "+chunk+"\n", DefaultSSEEventBytes/1024+2) + "\n"))
	if _, err := reader.Next(); err == nil || !strings.Contains(err.Error(), "sse event too large") {
		t.Fatalf("expected event size error, got %v", err)
	}
}
