package compile

import (
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/rustpolicy"
)

func TestCompileHelperGroupsGrantOnlyInstalledRustVendor(t *testing.T) {
	stdlibRunner := sandboxCommandRunnerForRustMode(rustpolicy.CrateModeStdlib)
	if len(stdlibRunner.supplementaryGroups) != 0 {
		t.Fatalf("stdlib supplementary groups = %v, want none", stdlibRunner.supplementaryGroups)
	}
	installedRunner := sandboxCommandRunnerForRustMode(rustpolicy.CrateModeInstalled)
	if !reflect.DeepEqual(installedRunner.supplementaryGroups, []uint32{rustpolicy.ExternalCrateGID}) {
		t.Fatalf("installed supplementary groups = %v", installedRunner.supplementaryGroups)
	}
	if got := compileHelperCredentialGroups(stdlibRunner.supplementaryGroups); !reflect.DeepEqual(got, []uint32{65532}) {
		t.Fatalf("stdlib helper groups = %v, want primary sandbox group only", got)
	}
	if got := compileHelperCredentialGroups(installedRunner.supplementaryGroups); !reflect.DeepEqual(got, []uint32{65532, rustpolicy.ExternalCrateGID}) {
		t.Fatalf("installed helper groups = %v", got)
	}
	for _, forbidden := range []uint32{65530, 65528} {
		for _, gid := range compileHelperCredentialGroups(installedRunner.supplementaryGroups) {
			if gid == forbidden {
				t.Fatalf("installed Rust helper inherited unrelated dependency GID %d", gid)
			}
		}
	}
}

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
