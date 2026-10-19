package execute

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

type interactiveTimerProbe struct {
	timers     *interactiveExecutionTimers
	contestant context.Context
	interactor context.Context
	events     chan string
}

func newInteractiveTimerProbe(t *testing.T, parent context.Context, startup, contestantLimit, interactorLimit time.Duration) *interactiveTimerProbe {
	t.Helper()
	contestant, cancelContestant := context.WithCancelCause(parent)
	interactor, cancelInteractor := context.WithCancelCause(parent)
	p := &interactiveTimerProbe{
		contestant: contestant,
		interactor: interactor,
		events:     make(chan string, 16),
	}
	p.timers = newInteractiveExecutionTimers(parent, startup, contestantLimit, interactorLimit,
		func() {
			p.events <- "contestant"
			cancelContestant(context.DeadlineExceeded)
		},
		func() {
			p.events <- "interactor"
			cancelInteractor(context.DeadlineExceeded)
		},
		func() {
			p.events <- "startup"
			cancelContestant(context.DeadlineExceeded)
			cancelInteractor(context.DeadlineExceeded)
		})
	t.Cleanup(func() {
		p.timers.stop()
		cancelContestant(context.Canceled)
		cancelInteractor(context.Canceled)
	})
	return p
}

func assertInteractiveReleased(t *testing.T, timers *interactiveExecutionTimers, want bool) {
	t.Helper()
	got := false
	select {
	case <-timers.release:
		got = true
	default:
	}
	if got != want {
		t.Fatalf("target release = %v, want %v", got, want)
	}
}

func assertInteractiveEvents(t *testing.T, p *interactiveTimerProbe, want ...string) {
	t.Helper()
	var got []string
	for {
		select {
		case event := <-p.events:
			got = append(got, event)
		default:
			if len(got) != len(want) {
				t.Fatalf("expiration events = %v, want %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("expiration events = %v, want %v", got, want)
				}
			}
			return
		}
	}
}

func TestInteractiveExecutionBudgetsStartAfterBothReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newInteractiveTimerProbe(t, context.Background(), 10*time.Second, 500*time.Millisecond, 750*time.Millisecond)
		time.Sleep(2 * time.Second)
		p.timers.targetReady(0)
		assertInteractiveReleased(t, p.timers, false)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if p.contestant.Err() != nil || p.interactor.Err() != nil {
			t.Fatal("sandbox preparation consumed an execution budget")
		}
		assertInteractiveEvents(t, p)

		p.timers.targetReady(1)
		assertInteractiveReleased(t, p.timers, true)
		time.Sleep(499 * time.Millisecond)
		if p.contestant.Err() != nil || p.interactor.Err() != nil {
			t.Fatal("execution budget expired early")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(p.contestant), context.DeadlineExceeded) || p.interactor.Err() != nil {
			t.Fatalf("contestant deadline affected wrong role: contestant=%v interactor=%v", context.Cause(p.contestant), context.Cause(p.interactor))
		}
		assertInteractiveEvents(t, p, "contestant")
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(p.interactor), context.DeadlineExceeded) {
			t.Fatalf("interactor deadline cause = %v", context.Cause(p.interactor))
		}
		assertInteractiveEvents(t, p, "interactor")
		time.Sleep(10 * time.Second)
		synctest.Wait()
		assertInteractiveEvents(t, p)
	})
}

func TestInteractiveExecutionDuplicateReadyCannotRestartBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newInteractiveTimerProbe(t, context.Background(), 10*time.Second, time.Second, 2*time.Second)
		p.timers.targetReady(0)
		p.timers.targetReady(0)
		assertInteractiveReleased(t, p.timers, false)
		p.timers.targetReady(1)
		assertInteractiveReleased(t, p.timers, true)
		time.Sleep(900 * time.Millisecond)
		p.timers.targetReady(0)
		p.timers.targetReady(1)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(p.contestant), context.DeadlineExceeded) {
			t.Fatal("duplicate readiness restarted the contestant's budget")
		}
		assertInteractiveEvents(t, p, "contestant")
		time.Sleep(time.Second)
		synctest.Wait()
		assertInteractiveEvents(t, p, "interactor")
		p.timers.targetReady(0)
		p.timers.targetReady(1)
		time.Sleep(10 * time.Second)
		synctest.Wait()
		assertInteractiveEvents(t, p)
	})
}

func TestInteractiveExecutionFinishStopsOnlyItsRole(t *testing.T) {
	for _, role := range []int{0, 1} {
		t.Run(map[int]string{0: "contestant", 1: "interactor"}[role], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newInteractiveTimerProbe(t, context.Background(), 10*time.Second, time.Second, time.Second)
				p.timers.targetReady(0)
				p.timers.targetReady(1)
				p.timers.finish(role)
				p.timers.finish(role)
				time.Sleep(time.Second)
				synctest.Wait()
				contexts := [2]context.Context{p.contestant, p.interactor}
				if contexts[role].Err() != nil || !errors.Is(context.Cause(contexts[1-role]), context.DeadlineExceeded) {
					t.Fatalf("finish(%d) stopped the wrong timer: contestant=%v interactor=%v", role, context.Cause(p.contestant), context.Cause(p.interactor))
				}
				assertInteractiveEvents(t, p, map[int]string{0: "interactor", 1: "contestant"}[role])
			})
		})
	}
}

func TestInteractiveExecutionStartupExpiryForbidsLateRelease(t *testing.T) {
	for _, oneReady := range []bool{false, true} {
		t.Run(map[bool]string{false: "neither-ready", true: "one-ready"}[oneReady], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newInteractiveTimerProbe(t, context.Background(), 2*time.Second, time.Second, time.Second)
				if oneReady {
					p.timers.targetReady(0)
				}
				time.Sleep(2 * time.Second)
				synctest.Wait()
				if !errors.Is(context.Cause(p.contestant), context.DeadlineExceeded) || !errors.Is(context.Cause(p.interactor), context.DeadlineExceeded) {
					t.Fatal("startup expiration did not cancel both roles")
				}
				assertInteractiveEvents(t, p, "startup")
				p.timers.targetReady(0)
				p.timers.targetReady(1)
				assertInteractiveReleased(t, p.timers, false)
				p.timers.finish(0)
				p.timers.finish(1)
				assertInteractiveEvents(t, p)
				p.timers.targetReady(0)
				p.timers.targetReady(1)
				assertInteractiveReleased(t, p.timers, false)
				time.Sleep(10 * time.Second)
				synctest.Wait()
				assertInteractiveEvents(t, p)
			})
		})
	}
}

func TestInteractiveExecutionFinishBeforeReleaseCancelsPeer(t *testing.T) {
	for _, role := range []int{0, 1} {
		t.Run(map[int]string{0: "contestant", 1: "interactor"}[role], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newInteractiveTimerProbe(t, context.Background(), 10*time.Second, time.Second, time.Second)
				p.timers.targetReady(1 - role)
				p.timers.finish(role)
				synctest.Wait()
				if p.contestant.Err() == nil || p.interactor.Err() == nil {
					t.Fatal("a failed startup left a peer waiting at the release barrier")
				}
				assertInteractiveEvents(t, p, "startup")
				p.timers.finish(role)
				p.timers.finish(1 - role)
				p.timers.targetReady(0)
				p.timers.targetReady(1)
				assertInteractiveReleased(t, p.timers, false)
				time.Sleep(10 * time.Second)
				synctest.Wait()
				assertInteractiveEvents(t, p)
			})
		})
	}
}

func TestInteractiveExecutionStopCancelsAllTimers(t *testing.T) {
	for _, readyCount := range []int{0, 1, 2} {
		t.Run(map[int]string{0: "before-ready", 1: "one-ready", 2: "after-release"}[readyCount], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newInteractiveTimerProbe(t, context.Background(), 2*time.Second, time.Second, time.Second)
				for role := 0; role < readyCount; role++ {
					p.timers.targetReady(role)
				}
				p.timers.stop()
				p.timers.stop()
				p.timers.targetReady(0)
				p.timers.targetReady(1)
				assertInteractiveReleased(t, p.timers, readyCount == 2)
				time.Sleep(10 * time.Second)
				synctest.Wait()
				assertInteractiveEvents(t, p)
				if p.contestant.Err() != nil || p.interactor.Err() != nil {
					t.Fatal("stopped timers later canceled a role")
				}
			})
		})
	}
}

func TestInteractiveExecutionPreservesParentCancellation(t *testing.T) {
	for _, readyCount := range []int{0, 1, 2} {
		t.Run(map[int]string{0: "before-ready", 1: "one-ready", 2: "after-release"}[readyCount], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancelParent := context.WithCancelCause(context.Background())
				defer cancelParent(context.Canceled)
				p := newInteractiveTimerProbe(t, parent, 2*time.Second, time.Second, time.Second)
				for role := 0; role < readyCount; role++ {
					p.timers.targetReady(role)
				}
				cause := errors.New("request canceled")
				cancelParent(cause)
				p.timers.targetReady(0)
				p.timers.targetReady(1)
				assertInteractiveReleased(t, p.timers, readyCount == 2)
				time.Sleep(10 * time.Second)
				synctest.Wait()
				if context.Cause(p.contestant) != cause || context.Cause(p.interactor) != cause {
					t.Fatalf("parent cause overwritten: contestant=%v interactor=%v", context.Cause(p.contestant), context.Cause(p.interactor))
				}
			})
		})
	}
}

func TestInteractiveExecutionPreservesEarlierParentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancelParent()
		p := newInteractiveTimerProbe(t, parent, 10*time.Second, time.Second, time.Second)
		p.timers.targetReady(0)
		p.timers.targetReady(1)
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(p.contestant), context.DeadlineExceeded) || !errors.Is(context.Cause(p.interactor), context.DeadlineExceeded) {
			t.Fatal("execution timers lost the earlier parent deadline")
		}
		assertInteractiveEvents(t, p)
	})
}
