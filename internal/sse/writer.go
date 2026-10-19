package sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/remoteio"
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
	wireBytes    int
	auxWireBytes int
	logWireBytes int
}

func New(w http.ResponseWriter, writeTimeout time.Duration) (*Writer, error) {
	if !supportsResponseControllerFlush(w) {
		return nil, fmt.Errorf("streaming unsupported: %w", http.ErrNotSupported)
	}
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

// Match ResponseController's flush capabilities without flushing and committing
// a response. A missing capability must leave room for an HTTP error response.
func supportsResponseControllerFlush(w http.ResponseWriter) bool {
	for {
		switch writer := w.(type) {
		case interface{ FlushError() error }, http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = writer.Unwrap()
		default:
			return false
		}
	}
}

func (s *Writer) Event(name string, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	frameOverhead := len("event: \ndata: \n\n") + len(name)
	limit := min(remoteio.DefaultSSEEventBytes, remoteio.DefaultSSELineBytes-frameOverhead, remoteio.DefaultSSEStreamBytes-s.wireBytes-frameOverhead)
	var payload []byte
	var err error
	runResult := false
	if name == "result" {
		switch response := data.(type) {
		case model.RunResponse:
			runResult = true
			payload, err = marshalRunResult(response, limit)
		case *model.RunResponse:
			if response != nil {
				runResult = true
				payload, err = marshalRunResult(*response, limit)
			} else {
				payload, err = json.Marshal(data)
			}
		default:
			payload, err = json.Marshal(data)
		}
	} else {
		payload, err = json.Marshal(data)
	}
	if err != nil {
		return err
	}
	frameBytes := frameOverhead + len(payload)
	if name != "result" {
		// Keep room for a full result even when escaped logs fill the stream.
		// Logs have a smaller share so images and control events retain space.
		auxLimit := remoteio.DefaultSSEStreamBytes - remoteio.DefaultSSELineBytes
		if s.auxWireBytes+frameBytes > auxLimit || (name == "log" && s.logWireBytes+frameBytes > auxLimit-(16<<20)) {
			return nil
		}
	}
	// Compile artifacts have an independent wire contract. Preserve that
	// existing behavior rather than silently discarding binary payloads here.
	if len(payload) > limit && (name != "result" || runResult) {
		return fmt.Errorf("SSE event %q exceeds wire budget: max %d bytes", name, limit)
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
	s.wireBytes += frameBytes
	if name != "result" {
		s.auxWireBytes += frameBytes
		if name == "log" {
			s.logWireBytes += frameBytes
		}
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
