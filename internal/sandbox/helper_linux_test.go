//go:build linux

package sandbox

import (
	"os"
	"strings"
	"testing"
)

func TestHelperAppliesAddressSpaceLimitAfterBarrierImmediatelyBeforeExec(t *testing.T) {
	raw, err := os.ReadFile("helper_linux.go")
	if err != nil {
		t.Fatalf("read helper_linux.go: %v", err)
	}
	source := string(raw)

	if strings.Contains(source, "}{unix.RLIMIT_AS, addressSpaceLimitBytes}") {
		t.Fatalf("RLIMIT_AS must not be part of the early helper rlimit batch")
	}

	seccompInstall := strings.Index(source, "unix.Prctl(unix.PR_SET_SECCOMP")
	waitRelease := strings.Index(source, "io.ReadFull(targetReleaseFile")
	addressSpaceLimit := strings.Index(source, "unix.Setrlimit(unix.RLIMIT_AS")
	targetExec := strings.Index(source, "unix.RawSyscall(unix.SYS_EXECVE")
	if seccompInstall < 0 || waitRelease < 0 || addressSpaceLimit < 0 || targetExec < 0 {
		t.Fatalf("helper_linux.go is missing expected sandbox setup anchors")
	}
	if !(waitRelease < addressSpaceLimit && addressSpaceLimit < seccompInstall && seccompInstall < targetExec) {
		t.Fatalf("RLIMIT_AS must be applied after the start barrier and immediately before seccomp plus target exec")
	}
	betweenLimitAndExec := source[addressSpaceLimit:targetExec]
	for _, allocationMarker := range []string{"make(", "fmt.", "os.", "io."} {
		if strings.Contains(betweenLimitAndExec, allocationMarker) {
			t.Fatalf("RLIMIT_AS-to-exec window contains allocation-capable operation %q", allocationMarker)
		}
	}
}

func TestHelperPolicyIncludesPeerControlAndSysVIPCGuards(t *testing.T) {
	raw, err := os.ReadFile("helper_linux.go")
	if err != nil {
		t.Fatalf("read helper_linux.go: %v", err)
	}
	source := string(raw)
	for _, marker := range []string{
		"unix.SYS_RT_SIGQUEUEINFO",
		"unix.SYS_RT_TGSIGQUEUEINFO",
		"unix.SYS_MSGGET",
		"unix.SYS_MSGSND",
		"unix.SYS_MSGRCV",
		"unix.SYS_MSGCTL",
		"unix.SYS_SEMGET",
		"unix.SYS_SEMOP",
		"unix.SYS_SEMTIMEDOP",
		"unix.SYS_SEMCTL",
		"unix.SYS_PRLIMIT64",
		"seccompDataArg0Offset+2*8+4",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("helper policy is missing %s", marker)
		}
	}
}

func TestHelperRejectsX32SyscallNumbersAndUnsupportedArchitectures(t *testing.T) {
	raw, err := os.ReadFile("helper_linux.go")
	if err != nil {
		t.Fatalf("read helper_linux.go: %v", err)
	}
	source := string(raw)
	for _, marker := range []string{
		`runtime.GOARCH != "amd64"`,
		"unsupported sandbox helper architecture",
		"x32SyscallBit = uint32(0x40000000)",
		"unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K, x32SyscallBit",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("helper policy is missing alternate-ABI guard %q", marker)
		}
	}
	if strings.Contains(source, `case "386"`) || strings.Contains(source, "unix.AUDIT_ARCH_I386") {
		t.Fatal("helper must not advertise unsupported 386 syscall filtering")
	}
}

func TestHelperWaitsForParentBaselineBeforeTargetExec(t *testing.T) {
	raw, err := os.ReadFile("helper_linux.go")
	if err != nil {
		t.Fatalf("read helper_linux.go: %v", err)
	}
	source := string(raw)
	closeDescriptors := strings.Index(source, "unix.CloseRange(3")
	signalReady := strings.Index(source, "targetReadyFile.Write")
	waitRelease := strings.Index(source, "io.ReadFull(targetReleaseFile")
	targetExec := strings.Index(source, "unix.RawSyscall(unix.SYS_EXECVE")
	if closeDescriptors < 0 || signalReady < 0 || waitRelease < 0 || targetExec < 0 {
		t.Fatalf("helper_linux.go is missing target synchronization anchors")
	}
	if !(closeDescriptors < signalReady && signalReady < waitRelease && waitRelease < targetExec) {
		t.Fatalf("helper must signal readiness and wait for the parent immediately before target exec")
	}
	if strings.Contains(source[waitRelease:targetExec], "targetReadyFile.Close()") {
		t.Fatal("target ready descriptor must stay open until close-on-exec signals the target transition")
	}
	if !strings.Contains(source[targetExec:], "runtime.KeepAlive(targetReadyFile)") {
		t.Fatal("target ready descriptor must remain live through the exec syscall")
	}
}

func TestHelperAlwaysRejectsUnsafeCloneForms(t *testing.T) {
	raw, err := os.ReadFile("helper_linux.go")
	if err != nil {
		t.Fatalf("read helper_linux.go: %v", err)
	}
	source := string(raw)
	for _, marker := range []string{
		"unsafeCloneFlags := uint32(",
		"unix.CLONE_NEWCGROUP",
		"unix.CLONE_NEWIPC",
		"unix.CLONE_NEWNET",
		"unix.CLONE_NEWNS",
		"unix.CLONE_NEWPID",
		"unix.CLONE_NEWTIME",
		"unix.CLONE_NEWUSER",
		"unix.CLONE_NEWUTS",
		"unix.CLONE_PARENT",
		"unix.CLONE_PTRACE",
		"unix.CLONE_UNTRACED",
		"seccompDataArg0Offset+4",
		"unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K, unsafeCloneFlags",
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("helper clone policy is missing %q", marker)
		}
	}
	clone3 := strings.Index(source, "uint32(unix.SYS_CLONE3)")
	processOptIn := strings.Index(source, "if !req.AllowProcesses")
	if clone3 < 0 || processOptIn < 0 || clone3 > processOptIn {
		t.Fatalf("clone3 must be denied independently of AllowProcesses")
	}
}

func TestHelperGuardsThreadSignalsBehindRuntimeOptIn(t *testing.T) {
	raw, err := os.ReadFile("helper_linux.go")
	if err != nil {
		t.Fatalf("read helper_linux.go: %v", err)
	}
	source := string(raw)
	optIn := strings.Index(source, "if !req.AllowThreadSignals")
	if optIn < 0 {
		t.Fatal("helper policy is missing the thread-signal opt-in guard")
	}
	for _, marker := range []string{
		"unix.SYS_TKILL",
		"unix.SYS_TGKILL",
		"unix.SYS_RT_TGSIGQUEUEINFO",
	} {
		position := strings.Index(source, marker)
		if position < optIn {
			t.Fatalf("%s must only be denied behind AllowThreadSignals", marker)
		}
	}
}

func TestHelperAllowsOnlyPositivePIDSignalZeroBehindExplicitOptIn(t *testing.T) {
	raw, err := os.ReadFile("helper_linux.go")
	if err != nil {
		t.Fatalf("read helper_linux.go: %v", err)
	}
	source := string(raw)
	optIn := strings.Index(source, "if req.AllowPositiveKillProbe")
	threadSignals := strings.Index(source, "if !req.AllowThreadSignals")
	if optIn < 0 || threadSignals < optIn {
		t.Fatal("kill(2) must remain behind the positive-PID signal-zero opt-in")
	}
	policy := source[optIn:threadSignals]
	for _, marker := range []string{
		"uint32(unix.SYS_KILL), 0, 10",
		"seccompDataArg0Offset+4",
		"unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K, signedIntHighBit",
		"seccompDataArg0Offset+8",
		"appendStmt(unix.BPF_RET|unix.BPF_K, allow)",
	} {
		if !strings.Contains(policy, marker) {
			t.Fatalf("Zsh kill policy is missing %q", marker)
		}
	}
	if strings.Contains(policy, "appendAllowOnlyZeroArg") {
		t.Fatal("Zsh kill policy must validate the positive PID argument")
	}
}
