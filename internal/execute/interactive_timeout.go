package execute

import (
	"context"
	"sync"
	"time"
)

// Both targets must finish sandbox initialization before either can execute.
// Keep initialization bounded, but do not charge it to either execution limit.
type interactiveExecutionTimers struct {
	mu             sync.Mutex
	ctx            context.Context
	release        chan struct{}
	startup        *time.Timer
	execution      [2]*time.Timer
	limits         [2]time.Duration
	expired        [2]func()
	startupExpired func()
	ready          [2]bool
	finished       [2]bool
	started        bool
	stopped        bool
}

func newInteractiveExecutionTimers(ctx context.Context, startup, contestantLimit, interactorLimit time.Duration, contestantExpired, interactorExpired, startupExpired func()) *interactiveExecutionTimers {
	t := &interactiveExecutionTimers{
		ctx: ctx, release: make(chan struct{}),
		limits:         [2]time.Duration{contestantLimit, interactorLimit},
		expired:        [2]func(){contestantExpired, interactorExpired},
		startupExpired: sync.OnceFunc(startupExpired),
	}
	t.startup = time.AfterFunc(startup, t.startupExpired)
	return t
}

func (t *interactiveExecutionTimers) targetReady(role int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.started || t.ctx.Err() != nil {
		return
	}
	t.ready[role] = true
	if !t.ready[0] || !t.ready[1] {
		return
	}
	// Stop failing means the startup deadline has fired or its callback is
	// pending. Never revive initialization by releasing late-ready targets.
	if !t.startup.Stop() {
		t.stopped = true
		return
	}
	if t.ctx.Err() != nil {
		t.stopped = true
		return
	}
	for i := range t.execution {
		t.execution[i] = time.AfterFunc(t.limits[i], func() {
			t.mu.Lock()
			active := !t.stopped && !t.finished[i]
			t.mu.Unlock()
			if active {
				t.expired[i]()
			}
		})
	}
	t.started = true
	close(t.release)
}

func (t *interactiveExecutionTimers) finish(role int) {
	t.mu.Lock()
	t.finished[role] = true
	if t.started {
		t.execution[role].Stop()
		t.mu.Unlock()
		return
	}
	// A target that failed before readiness cannot join the barrier. Cancel
	// its peer immediately instead of leaving it waiting for a second ready.
	abort := !t.stopped
	t.stopped = true
	t.startup.Stop()
	t.mu.Unlock()
	if abort {
		t.startupExpired()
	}
}

func (t *interactiveExecutionTimers) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	t.startup.Stop()
	for _, timer := range t.execution {
		if timer != nil {
			timer.Stop()
		}
	}
}
