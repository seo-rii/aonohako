package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type shutdownLifecycleStub struct {
	began    chan struct{}
	canceled chan struct{}
	cleanup  chan struct{}
}

func (s shutdownLifecycleStub) BeginDrain() { close(s.began) }
func (s shutdownLifecycleStub) CancelWork() { close(s.canceled) }
func (s shutdownLifecycleStub) WaitForWork(ctx context.Context) error {
	select {
	case <-s.cleanup:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestShutdownAllowsRunningWorkToComplete(t *testing.T) {
	work := shutdownLifecycleStub{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	started, complete := make(chan struct{}), make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-complete
		if r.Context().Err() != nil {
			t.Error("running job canceled during grace")
		}
		close(work.cleanup)
	}))
	defer ts.Close()
	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	done := make(chan error, 1)
	go func() { done <- shutdownServer(ts.Config, work, time.Second) }()
	<-work.began
	close(complete)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed work did not shut down")
	}
	select {
	case <-work.canceled:
	default:
		t.Fatal("final cancellation phase was skipped")
	}
}

func TestShutdownWaitsAfterHTTPGraceExpires(t *testing.T) {
	work := shutdownLifecycleStub{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	started, canceled, allowCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-allowCleanup
		close(work.cleanup)
	}))
	defer ts.Close()
	resp, err := ts.Client().Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	done := make(chan error, 1)
	go func() { done <- shutdownServer(ts.Config, work, 20*time.Millisecond) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("forced HTTP close did not cancel handler")
	}
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before cleanup: %v", err)
	default:
	}
	close(allowCleanup)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown error = %v, want grace deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup completion did not release shutdown")
	}
}
