package compile

import (
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"aonohako/internal/gomodulepolicy"
	"aonohako/internal/model"
	"aonohako/internal/rustpolicy"
)

func TestCompileHelperGroupsGrantOnlyInstalledRustVendor(t *testing.T) {
	stdlibRunner := sandboxCommandRunnerForRustMode(rustpolicy.CrateModeStdlib)
	if len(stdlibRunner.supplementaryGroups) != 0 {
		t.Fatalf("stdlib supplementary groups = %v, want none", stdlibRunner.supplementaryGroups)
	}
	if len(stdlibRunner.workspaceDirs) != 0 {
		t.Fatalf("stdlib workspace dirs = %v, want none", stdlibRunner.workspaceDirs)
	}
	installedRunner := sandboxCommandRunnerForRustMode(rustpolicy.CrateModeInstalled)
	if !reflect.DeepEqual(installedRunner.supplementaryGroups, []uint32{rustpolicy.ExternalCrateGID}) {
		t.Fatalf("installed supplementary groups = %v", installedRunner.supplementaryGroups)
	}
	if !reflect.DeepEqual(installedRunner.workspaceDirs, []string{".cargo-home", ".cargo-target"}) {
		t.Fatalf("installed workspace dirs = %v", installedRunner.workspaceDirs)
	}
	if got := compileHelperCredentialGroups(stdlibRunner.supplementaryGroups); !reflect.DeepEqual(got, []uint32{65532}) {
		t.Fatalf("stdlib helper groups = %v, want primary sandbox group only", got)
	}
	if got := compileHelperCredentialGroups(installedRunner.supplementaryGroups); !reflect.DeepEqual(got, []uint32{65532, rustpolicy.ExternalCrateGID}) {
		t.Fatalf("installed helper groups = %v", got)
	}
	for _, forbidden := range []uint32{65530, gomodulepolicy.ExternalModuleGID} {
		for _, gid := range compileHelperCredentialGroups(installedRunner.supplementaryGroups) {
			if gid == forbidden {
				t.Fatalf("installed Rust helper inherited unrelated dependency GID %d", gid)
			}
		}
	}
}

func TestCompileHelperGroupsGrantOnlyInstalledGoCache(t *testing.T) {
	stdlibRunner := sandboxCommandRunnerForGoMode(gomodulepolicy.ModeStdlib)
	if len(stdlibRunner.supplementaryGroups) != 0 {
		t.Fatalf("stdlib supplementary groups = %v, want none", stdlibRunner.supplementaryGroups)
	}
	installedRunner := sandboxCommandRunnerForGoMode(gomodulepolicy.ModeInstalled)
	if !reflect.DeepEqual(installedRunner.supplementaryGroups, []uint32{gomodulepolicy.ExternalModuleGID}) {
		t.Fatalf("installed supplementary groups = %v", installedRunner.supplementaryGroups)
	}
	if got := compileHelperCredentialGroups(stdlibRunner.supplementaryGroups); !reflect.DeepEqual(got, []uint32{65532}) {
		t.Fatalf("stdlib helper groups = %v, want primary sandbox group only", got)
	}
	if got := compileHelperCredentialGroups(installedRunner.supplementaryGroups); !reflect.DeepEqual(got, []uint32{65532, gomodulepolicy.ExternalModuleGID}) {
		t.Fatalf("installed helper groups = %v", got)
	}
	for _, forbidden := range []uint32{65530, rustpolicy.ExternalCrateGID} {
		for _, gid := range compileHelperCredentialGroups(installedRunner.supplementaryGroups) {
			if gid == forbidden {
				t.Fatalf("installed Go helper inherited unrelated dependency GID %d", gid)
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
		`allowThreadSignals := isDotnetLike || commandName == "factor"`,
		`AllowThreadSignals:       allowThreadSignals`,
		`AllowMemfdCreate:         isDotnetLike || isIsabelle`,
		`AllowNumaPolicy:          isDotnetLike || isIsabelle`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("PowerShell compile sandbox contract must contain %q", marker)
		}
	}
}

func TestAssemblyScriptCompilerGetsOnlyAddressSpaceCompatibilityException(t *testing.T) {
	raw, err := os.ReadFile("sandbox_command.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, marker := range []string{
		`commandName == "aonohako-assemblyscript-compile"`,
		`allowProcesses := commandName != "aonohako-assemblyscript-compile"`,
		`allowUnixSockets := commandName != "aonohako-assemblyscript-compile"`,
		`AllowProcesses:           allowProcesses`,
		`AllowUnixSockets:         allowUnixSockets`,
		`allowThreadSignals := isDotnetLike || commandName == "factor"`,
		`AllowThreadSignals:       allowThreadSignals`,
		`AllowMemfdCreate:         isDotnetLike || isIsabelle`,
		`AllowNumaPolicy:          isDotnetLike || isIsabelle`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("AssemblyScript compile sandbox contract must contain %q", marker)
		}
	}
}

func TestFactorParserGetsOnlyThreadSignalCompatibilityException(t *testing.T) {
	raw, err := os.ReadFile("sandbox_command.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, marker := range []string{
		`allowProcesses := commandName != "aonohako-assemblyscript-compile" && commandName != "factor"`,
		`allowUnixSockets := commandName != "aonohako-assemblyscript-compile" && commandName != "factor"`,
		`AllowProcesses:           allowProcesses`,
		`AllowUnixSockets:         allowUnixSockets`,
		`allowThreadSignals := isDotnetLike || commandName == "factor"`,
		`AllowThreadSignals:       allowThreadSignals`,
		`AllowMemfdCreate:         isDotnetLike || isIsabelle`,
		`AllowNumaPolicy:          isDotnetLike || isIsabelle`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("Factor compile sandbox contract must contain %q", marker)
		}
	}
	for _, forbidden := range []string{`AllowProcesses:           commandName == "factor"`, `AllowUnixSockets:         commandName == "factor"`, `AllowMemfdCreate:         commandName == "factor"`, `AllowNumaPolicy:          commandName == "factor"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Factor parser unexpectedly receives sandbox privilege %q", forbidden)
		}
	}
}

func TestChezSchemeReaderGetsNoProcessOrSocketException(t *testing.T) {
	raw, err := os.ReadFile("sandbox_command.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, marker := range []string{
		`isSyntaxOnlySchemeReader := commandName == "chezscheme"`,
		`allowProcesses := commandName != "aonohako-assemblyscript-compile" && commandName != "factor" && !isSyntaxOnlySchemeReader`,
		`allowUnixSockets := commandName != "aonohako-assemblyscript-compile" && commandName != "factor" && !isSyntaxOnlySchemeReader`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("Chez Scheme reader sandbox contract must contain %q", marker)
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
