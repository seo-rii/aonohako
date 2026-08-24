package compile

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"aonohako/internal/model"
)

func TestPowerShellParserGetsOnlyAddressSpaceCompatibilityException(t *testing.T) {
	raw, err := os.ReadFile("sandbox_command.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, marker := range []string{
		`isPowerShell := commandName == "pwsh"`,
		`disableAddressSpaceLimit := isDotnetLike || isPowerShell`,
		`AllowThreadSignals:       isDotnetLike`,
		`AllowMemfdCreate:         isDotnetLike || isIsabelle`,
		`AllowNumaPolicy:          isDotnetLike || isIsabelle`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("PowerShell compile sandbox contract must contain %q", marker)
		}
	}
}

func TestClassifyCompileWaitStatusTreatsCompilerSignalAsInternal(t *testing.T) {
	for _, signal := range []syscall.Signal{
		syscall.SIGABRT,
		syscall.SIGBUS,
		syscall.SIGILL,
		syscall.SIGSEGV,
	} {
		status, reason := classifyCompileWaitStatus(syscall.WaitStatus(signal))
		if status != model.CompileStatusInternal {
			t.Fatalf("signal %s status = %s, want %s", signal, status, model.CompileStatusInternal)
		}
		if !strings.Contains(reason, signal.String()) {
			t.Fatalf("signal %s reason = %q", signal, reason)
		}
	}
}

func TestClassifyCompileWaitStatusKeepsCompilerExitAsCompileError(t *testing.T) {
	status, reason := classifyCompileWaitStatus(syscall.WaitStatus(2 << 8))
	if status != model.CompileStatusCompileError {
		t.Fatalf("exit status = %s, want %s", status, model.CompileStatusCompileError)
	}
	if reason != "sandbox command exited with code 2" {
		t.Fatalf("exit reason = %q", reason)
	}
}

func TestClassifyCompileWaitStatusTreatsCPUTimeSignalAsTimeout(t *testing.T) {
	status, reason := classifyCompileWaitStatus(syscall.WaitStatus(syscall.SIGXCPU))
	if status != model.CompileStatusTimeout {
		t.Fatalf("SIGXCPU status = %s, want %s", status, model.CompileStatusTimeout)
	}
	if reason != "sandbox command exceeded CPU time limit" {
		t.Fatalf("SIGXCPU reason = %q", reason)
	}
}
