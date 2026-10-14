package sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const DefaultWriteTimeout = 10 * time.Second

type responseStartedError struct{ error }

func (e *responseStartedError) Unwrap() error { return e.error }

// ResponseStarted reports whether initialization failed after committing the
// SSE response. Callers must not append an HTTP error response in this case.
func ResponseStarted(err error) bool {
	var started *responseStartedError
	return errors.As(err, &started)
}

type Writer struct {
	w            http.ResponseWriter
	controller   *http.ResponseController
	writeTimeout time.Duration
	mu           sync.Mutex
	failed       error
}

func New(w http.ResponseWriter, writeTimeout time.Duration) (*Writer, error) {
	if writeTimeout <= 0 {
		writeTimeout = DefaultWriteTimeout
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return nil, fmt.Errorf("set initial sse write deadline: %w", err)
	}
	headers := w.Header()
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		return nil, &responseStartedError{fmt.Errorf("flush initial sse response: %w", err)}
	}
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		return nil, &responseStartedError{fmt.Errorf("clear initial sse write deadline: %w", err)}
	}
	return &Writer{w: w, controller: controller, writeTimeout: writeTimeout}, nil
}

func (s *Writer) Event(name string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	if err := s.controller.SetWriteDeadline(time.Now().Add(s.writeTimeout)); err != nil {
		s.failed = fmt.Errorf("set sse write deadline: %w", err)
		return s.failed
	}
	_, err = fmt.Fprintf(s.w, "event: %s\n", name)
	if err == nil {
		_, err = fmt.Fprintf(s.w, "data: %s\n\n", payload)
	}
	if err == nil {
		err = s.controller.Flush()
	}
	if err == nil {
		err = s.controller.SetWriteDeadline(time.Time{})
	}
	if err != nil {
		s.failed = fmt.Errorf("write sse event %q: %w", name, err)
		return s.failed
	}
	return nil
}

func (s *Writer) Heartbeat(ctx context.Context, interval time.Duration) error {
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
