package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQueueFIFO(t *testing.T) {
	q := New(1, 10)

	p1, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire1: %v", err)
	}
	if p1.Position() != 0 {
		t.Fatalf("expected immediate position=0, got %d", p1.Position())
	}

	p2, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire2: %v", err)
	}
	p3, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire3: %v", err)
	}
	if p2.Position() != 1 || p3.Position() != 2 {
		t.Fatalf("unexpected queue positions p2=%d p3=%d", p2.Position(), p3.Position())
	}

	done2 := make(chan struct{})
	done3 := make(chan struct{})

	go func() {
		_ = p2.Wait(context.Background())
		close(done2)
	}()
	go func() {
		_ = p3.Wait(context.Background())
		close(done3)
	}()

	select {
	case <-done2:
		t.Fatalf("p2 should wait")
	case <-done3:
		t.Fatalf("p3 should wait")
	case <-time.After(50 * time.Millisecond):
	}

	p1.Release()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatalf("p2 should be released first")
	}

	select {
	case <-done3:
		t.Fatalf("p3 should still wait")
	case <-time.After(50 * time.Millisecond):
	}

	p2.Release()
	select {
	case <-done3:
	case <-time.After(time.Second):
		t.Fatalf("p3 should be released after p2")
	}
	p3.Release()
}

func TestQueueOverflow429(t *testing.T) {
	q := New(1, 1)
	p1, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire1: %v", err)
	}
	defer p1.Release()

	_, err = q.Acquire()
	if err != nil {
		t.Fatalf("acquire2: %v", err)
	}

	_, err = q.Acquire()
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestQueuedContextCancelRemovesWaiter(t *testing.T) {
	q := New(1, 10)
	p1, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire1: %v", err)
	}
	defer p1.Release()

	p2, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire2: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p2.Wait(ctx); err == nil {
		t.Fatalf("expected cancellation error")
	}

	_, pending := q.Snapshot()
	if pending != 0 {
		t.Fatalf("expected pending=0 after cancel, got %d", pending)
	}
}

func TestQueueUnlimitedPendingWhenZero(t *testing.T) {
	q := New(1, 0)
	p1, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire1: %v", err)
	}
	defer p1.Release()

	for i := 0; i < 32; i++ {
		p, err := q.Acquire()
		if err != nil {
			t.Fatalf("unexpected queue overflow at %d: %v", i, err)
		}
		if p.Position() != i+1 {
			t.Fatalf("unexpected position at %d: %d", i, p.Position())
		}
	}
}
