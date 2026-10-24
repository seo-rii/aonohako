package api

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type workContextKey struct{}

type workRequest struct {
	cancel  context.CancelFunc
	running bool
}

// workLifecycle owns requests until their handlers (including runner cleanup)
// return. Admission and the transition to running share the drain lock.
type workLifecycle struct {
	mu       sync.Mutex
	draining bool
	requests map[*workRequest]struct{}
	done     chan struct{}
	finished bool
}

func (l *workLifecycle) begin(parent context.Context) (context.Context, func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.draining {
		return nil, nil, false
	}
	if l.requests == nil {
		l.requests = make(map[*workRequest]struct{})
	}
	ctx, cancel := context.WithCancel(parent)
	request := &workRequest{cancel: cancel}
	l.requests[request] = struct{}{}
	ctx = context.WithValue(ctx, workContextKey{}, request)
	return ctx, func() {
		cancel()
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.requests, request)
		l.signalFinished()
	}, true
}

func (l *workLifecycle) start(ctx context.Context) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.draining || ctx.Err() != nil {
		return false
	}
	if request, ok := ctx.Value(workContextKey{}).(*workRequest); ok {
		if _, exists := l.requests[request]; !exists {
			return false
		}
		request.running = true
	}
	return true
}

func (l *workLifecycle) isDraining() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.draining
}

func (l *workLifecycle) signalFinished() {
	if l.draining && len(l.requests) == 0 && !l.finished {
		if l.done == nil {
			l.done = make(chan struct{})
		}
		close(l.done)
		l.finished = true
	}
}

// BeginDrain rejects new work and cancels requests still preparing or queued.
// Running jobs retain their contexts for the HTTP shutdown grace period.
func (s *Server) BeginDrain() {
	s.work.mu.Lock()
	defer s.work.mu.Unlock()
	s.work.draining = true
	for request := range s.work.requests {
		if !request.running {
			request.cancel()
		}
	}
	s.work.signalFinished()
}

// CancelWork cancels every outstanding request after the completion grace.
func (s *Server) CancelWork() {
	s.work.mu.Lock()
	defer s.work.mu.Unlock()
	for request := range s.work.requests {
		request.cancel()
	}
}

// WaitForWork waits for every admitted handler and its synchronous cleanup.
// Call BeginDrain first to prevent new requests racing with completion.
func (s *Server) WaitForWork(ctx context.Context) error {
	s.work.mu.Lock()
	if s.work.done == nil {
		s.work.done = make(chan struct{})
	}
	done := s.work.done
	s.work.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) withWorkLifecycle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, finish, ok := s.work.begin(r.Context())
		if !ok {
			writeJSONError(w, http.StatusServiceUnavailable, "server_draining")
			return
		}
		defer finish()
		// Close the original body on cancellation to unblock preparatory reads.
		// Capture it before decoding replaces r.Body with http.NoBody.
		body := r.Body
		bodyClosed := make(chan struct{})
		stopClose := context.AfterFunc(ctx, func() {
			defer close(bodyClosed)
			// net/http's body.Close takes the same lock as a blocked Read.
			// Interrupt the socket read first, including through middleware.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
			if body != nil {
				_ = body.Close()
			}
		})
		defer func() {
			if !stopClose() {
				<-bodyClosed
			}
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
