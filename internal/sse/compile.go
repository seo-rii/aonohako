package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"aonohako/internal/compilewire"
)

// CompileWriter has a larger result allowance for Base64 artifacts. Execute
// streams retain their independent default limits and presentation policy.
type CompileWriter struct {
	transport *Writer
	wireBytes int
	auxBytes  int
	finished  bool
}

func NewCompile(w http.ResponseWriter, writeTimeout time.Duration) (*CompileWriter, error) {
	transport, err := New(w, writeTimeout)
	if err != nil {
		return nil, err
	}
	return &CompileWriter{transport: transport}, nil
}

func (s *CompileWriter) Event(name string, data any) error {
	t := s.transport
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failed != nil {
		return t.failed
	}
	if s.finished {
		return nil
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	frameBytes := len("event: \ndata: \n\n") + len(name) + len(payload)
	if len(payload) > compilewire.SSEEventBytes || len("data: \n")+len(payload) > compilewire.SSELineBytes {
		return fmt.Errorf("compile SSE event %q exceeds wire budget: max %d bytes", name, compilewire.SSEEventBytes)
	}
	if s.wireBytes+frameBytes > compilewire.SSEStreamBytes {
		return fmt.Errorf("compile SSE stream exceeds wire budget: max %d bytes", compilewire.SSEStreamBytes)
	}
	if name != "result" {
		resultFrame := compilewire.SSEEventBytes + len("event: result\ndata: \n\n")
		if s.auxBytes+frameBytes > compilewire.SSEStreamBytes-resultFrame {
			return fmt.Errorf("compile SSE auxiliary events exceed wire budget")
		}
	}
	if err := t.controller.SetWriteDeadline(time.Now().Add(t.writeTimeout)); err != nil {
		t.failed = fmt.Errorf("set sse write deadline: %w", err)
		return t.failed
	}
	_, err = fmt.Fprintf(t.w, "event: %s\n", name)
	if err == nil {
		_, err = fmt.Fprintf(t.w, "data: %s\n\n", payload)
	}
	if err == nil {
		err = t.controller.Flush()
	}
	if err == nil {
		err = t.controller.SetWriteDeadline(time.Time{})
	}
	if err != nil {
		t.failed = fmt.Errorf("write compile sse event %q: %w", name, err)
		return t.failed
	}
	s.wireBytes += frameBytes
	if name == "result" {
		s.finished = true
	} else {
		s.auxBytes += frameBytes
	}
	return nil
}

func (s *CompileWriter) Heartbeat(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Event("heartbeat", map[string]any{"ts": time.Now().UnixMilli()}); err != nil {
				return err
			}
		}
	}
}
