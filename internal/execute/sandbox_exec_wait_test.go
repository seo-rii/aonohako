package execute

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"aonohako/internal/model"
)

func TestWaitErrorIsProcessExit(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_EXIT_1") == "1" {
		os.Exit(1)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestWaitErrorIsProcessExit")
	cmd.Env = append(os.Environ(), "AONOHAKO_TEST_EXIT_1=1")
	err := cmd.Run()
	if !waitErrorIsProcessExit(err) {
		t.Fatalf("waitErrorIsProcessExit(%T) = false, want true", err)
	}
	if waitErrorIsProcessExit(errors.New("relay failed")) {
		t.Fatal("ordinary wait error was misclassified as process exit")
	}
	if waitErrorIsProcessExit(nil) {
		t.Fatal("nil wait error was misclassified as process exit")
	}
}

func TestInteractorNonZeroExitUsesInteractorVerdict(t *testing.T) {
	exitCode := 1
	req := &model.RunRequest{Limits: model.Limits{TimeMs: 1000}}
	status, reason, source := classifyInteractorStatus(req, execResult{
		Status:   "OK",
		ExitCode: &exitCode,
		Stderr:   []byte("wrong answer Wrong Answer\n"),
	})
	if status != model.RunStatusWA {
		t.Fatalf("status = %q, want %q", status, model.RunStatusWA)
	}
	if reason != "wrong answer Wrong Answer\n" {
		t.Fatalf("reason = %q", reason)
	}
	if source != "exit_code" {
		t.Fatalf("source = %q, want exit_code", source)
	}
}

func TestContestantNonZeroExitRemainsRuntimeError(t *testing.T) {
	exitCode := 1
	req := &model.RunRequest{Limits: model.Limits{TimeMs: 1000}}
	status, _, source := classifyRunStatusWithoutOutput(req, execResult{
		Status:   "OK",
		ExitCode: &exitCode,
	})
	if status != model.RunStatusRE {
		t.Fatalf("status = %q, want %q", status, model.RunStatusRE)
	}
	if source != "exit_code" {
		t.Fatalf("source = %q, want exit_code", source)
	}
}
