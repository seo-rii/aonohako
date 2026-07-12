package queue

import (
	"context"
	"errors"
	"sync"
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

func TestImmediatePermitWaitHonorsCanceledContext(t *testing.T) {
	q := New(1, 1)
	permit, err := q.Acquire()
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := permit.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
	active, pending := q.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("queue snapshot = (%d, %d), want (0, 0)", active, pending)
	}

	next, err := q.Acquire()
	if err != nil {
		t.Fatalf("Acquire() after cancellation error = %v", err)
	}
	if next.Position() != 0 {
		t.Fatalf("position after cancellation = %d, want 0", next.Position())
	}
	next.Release()
}

func TestGrantedPermitWaitHonorsCanceledContext(t *testing.T) {
	q := New(1, 1)
	first, err := q.Acquire()
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	second, err := q.Acquire()
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	first.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := second.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
	active, pending := q.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("queue snapshot = (%d, %d), want (0, 0)", active, pending)
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

func TestCancelGrantedQueuedPermitReleasesCapacity(t *testing.T) {
	q := New(1, 1)
	p1, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire1: %v", err)
	}
	p2, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire2: %v", err)
	}

	p1.Release()
	p2.Cancel()

	if err := p2.Wait(context.Background()); !errors.Is(err, ErrPermitCanceled) {
		t.Fatalf("Wait() error = %v, want ErrPermitCanceled", err)
	}
	active, pending := q.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("queue snapshot = (%d, %d), want (0, 0)", active, pending)
	}

	p3, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire3: %v", err)
	}
	if p3.Position() != 0 {
		t.Fatalf("position after cancellation = %d, want 0", p3.Position())
	}
	p3.Release()
}

func TestImmediatePermitWaitAfterTerminalActionReturnsError(t *testing.T) {
	for _, action := range []struct {
		name string
		do   func(*Permit)
	}{
		{name: "cancel", do: func(permit *Permit) { permit.Cancel() }},
		{name: "release", do: func(permit *Permit) { permit.Release() }},
	} {
		t.Run(action.name, func(t *testing.T) {
			q := New(1, 1)
			permit, err := q.Acquire()
			if err != nil {
				t.Fatalf("Acquire() error = %v", err)
			}
			action.do(permit)
			if err := permit.Wait(context.Background()); !errors.Is(err, ErrPermitCanceled) {
				t.Fatalf("Wait() error = %v, want ErrPermitCanceled", err)
			}
			active, pending := q.Snapshot()
			if active != 0 || pending != 0 {
				t.Fatalf("queue snapshot = (%d, %d), want (0, 0)", active, pending)
			}
		})
	}
}

func TestConcurrentWaitAndCancelDoesNotHang(t *testing.T) {
	q := New(1, 1)
	p1, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire1: %v", err)
	}
	p2, err := q.Acquire()
	if err != nil {
		t.Fatalf("acquire2: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- p2.Wait(context.Background())
	}()
	cancelDone := make(chan struct{})
	go func() {
		p2.Cancel()
		close(cancelDone)
	}()

	select {
	case <-cancelDone:
	case <-time.After(time.Second):
		t.Fatal("Cancel() hung")
	}
	select {
	case err := <-waitDone:
		if !errors.Is(err, ErrPermitCanceled) {
			t.Fatalf("Wait() error = %v, want ErrPermitCanceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() did not wake after Cancel()")
	}

	p1.Release()
	active, pending := q.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("queue snapshot = (%d, %d), want (0, 0)", active, pending)
	}
}

func TestGrantCancelWaitRaceReleasesExactlyOnce(t *testing.T) {
	for i := 0; i < 1000; i++ {
		q := New(1, 1)
		p1, err := q.Acquire()
		if err != nil {
			t.Fatalf("iteration %d acquire1: %v", i, err)
		}
		p2, err := q.Acquire()
		if err != nil {
			t.Fatalf("iteration %d acquire2: %v", i, err)
		}

		waitDone := make(chan error, 1)
		go func() {
			waitDone <- p2.Wait(context.Background())
		}()
		var actions sync.WaitGroup
		actions.Add(2)
		go func() {
			defer actions.Done()
			p1.Release()
		}()
		go func() {
			defer actions.Done()
			p2.Cancel()
		}()
		actions.Wait()

		select {
		case err := <-waitDone:
			if err != nil && !errors.Is(err, ErrPermitCanceled) {
				t.Fatalf("iteration %d Wait() error = %v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d Wait() hung", i)
		}
		p2.Release()
		active, pending := q.Snapshot()
		if active != 0 || pending != 0 {
			t.Fatalf("iteration %d queue snapshot = (%d, %d), want (0, 0)", i, active, pending)
		}
	}
}
