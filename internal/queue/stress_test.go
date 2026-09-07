package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestQueueActiveNeverExceedsLimitUnderLoad drives many concurrent acquirers
// that each Wait, briefly hold the permit, then Release, and asserts the number
// of simultaneously-active permits never exceeds maxActive and that no permit or
// waiter leaks at the end. Most valuable under `go test -race`.
func TestQueueActiveNeverExceedsLimitUnderLoad(t *testing.T) {
	const (
		maxActive  = 4
		maxPending = 256
		workers    = 64
		iterations = 25
	)
	m := New(maxActive, maxPending)
	var live int64
	var acquired int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				permit, err := m.Acquire()
				if err != nil {
					continue // queue full is a legal outcome
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := permit.Wait(ctx); err != nil {
					cancel()
					permit.Cancel()
					continue
				}
				atomic.AddInt64(&acquired, 1)
				cur := atomic.AddInt64(&live, 1)
				if cur > maxActive {
					t.Errorf("simultaneously active permits=%d exceeds maxActive=%d", cur, maxActive)
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt64(&live, -1)
				permit.Release()
				cancel()
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&live); got != 0 {
		t.Fatalf("live permits leaked: %d", got)
	}
	if atomic.LoadInt64(&acquired) == 0 {
		t.Fatalf("no permit was ever acquired")
	}
	active, pending := m.Snapshot()
	if active != 0 {
		t.Fatalf("active count leaked: %d", active)
	}
	if pending != 0 {
		t.Fatalf("pending waiters leaked: %d", pending)
	}
}
