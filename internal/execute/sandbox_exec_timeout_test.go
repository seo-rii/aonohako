package execute

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
)

func TestSandboxExecutionBudgetStartsAtRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, start, cancel := sandboxExecutionContext(context.Background(), 10*time.Second, 500*time.Millisecond)
		defer cancel()
		time.Sleep(2 * time.Second)
		if ctx.Err() != nil || !start() {
			t.Fatal("setup consumed the target's execution budget")
		}
		time.Sleep(499 * time.Millisecond)
		if ctx.Err() != nil {
			t.Fatal("target deadline expired early")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			t.Fatalf("deadline cause = %v", context.Cause(ctx))
		}
	})
}

func TestSandboxInitializationDeadlineCannotBeRestarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, start, cancel := sandboxExecutionContext(context.Background(), 10*time.Second, time.Second)
		defer cancel()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if start() || !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			t.Fatalf("expired setup was restarted: %v", context.Cause(ctx))
		}
	})
}

func TestSandboxExecutionPreservesParentCancellation(t *testing.T) {
	for _, released := range []bool{false, true} {
		t.Run(map[bool]string{false: "during-setup", true: "during-execution"}[released], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancelParent := context.WithCancel(context.Background())
				ctx, start, cancel := sandboxExecutionContext(parent, 10*time.Second, time.Second)
				defer cancel()
				if released && !start() {
					t.Fatal("could not start execution")
				}
				cancelParent()
				synctest.Wait()
				if !errors.Is(context.Cause(ctx), context.Canceled) {
					t.Fatalf("parent cancellation lost: %v", context.Cause(ctx))
				}
				if !released && start() {
					t.Fatal("canceled setup allowed target release")
				}
			})
		})
	}
}

func TestSandboxExecutionPreservesEarlierParentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancelParent := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancelParent()
		ctx, start, cancel := sandboxExecutionContext(parent, 10*time.Second, time.Second)
		defer cancel()
		if !start() {
			t.Fatal("could not start execution")
		}
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			t.Fatalf("parent deadline lost: %v", context.Cause(ctx))
		}
	})
}

type delayedSandboxInput struct {
	reader io.Reader
	delay  time.Duration
}

func (r *delayedSandboxInput) Read(p []byte) (int, error) {
	time.Sleep(r.delay)
	r.delay = 0
	return r.reader.Read(p)
}

func TestRunSandboxSetupNotChargedToTimeLimit(t *testing.T) {
	requireSandboxSupport(t)
	ws, err := prepareWorkspaceDirs(sandboxAccessibleTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	input := &delayedSandboxInput{reader: strings.NewReader("ready\n"), delay: 750 * time.Millisecond}
	result := runCommandWithSandbox(context.Background(), ws, []string{"/bin/cat"},
		&model.RunRequest{Lang: "binary", Limits: model.Limits{TimeMs: 500, MemoryMB: 256}},
		input, 1024, Hooks{}, 1024, config.DefaultRuntimeTuningConfig(), "")
	if result.Status != "OK" || result.ExitCode == nil || *result.ExitCode != 0 || string(result.Stdout) != "ready\n" {
		t.Fatalf("750 ms input preparation consumed 500 ms execution budget: %+v", result)
	}
	if result.WallTimeMs >= 500 {
		t.Fatalf("reported execution includes input preparation: %d ms", result.WallTimeMs)
	}
	t.Logf("750 ms preparation, target wall=%d ms CPU=%d ms", result.WallTimeMs, result.CPUTimeMs)
}

func TestRunSandboxExecutionStillEnforcesWallTimeLimit(t *testing.T) {
	requireSandboxSupport(t)
	ws, err := prepareWorkspaceDirs(sandboxAccessibleTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	result := runCommandWithSandbox(context.Background(), ws, []string{"/bin/sleep", "5"},
		&model.RunRequest{Lang: "binary", Limits: model.Limits{TimeMs: 500, MemoryMB: 256}},
		nil, 0, Hooks{}, 1024, config.DefaultRuntimeTuningConfig(), "")
	if result.Status != model.RunStatusTLE || result.VerdictSource != "wall_time" {
		t.Fatalf("execution deadline was not enforced: %+v", result)
	}
	if result.WallTimeMs < 500 || result.WallTimeMs >= 2000 {
		t.Fatalf("unexpected wall-time enforcement: %d ms", result.WallTimeMs)
	}
	t.Logf("5000 ms target killed at wall=%d ms, source=%s", result.WallTimeMs, result.VerdictSource)
}
