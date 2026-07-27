//go:build linux

package sandbox

import (
	"os"
	"strings"
	"testing"
)

func TestHelperAppliesAddressSpaceLimitAfterGoSetupBeforeSeccomp(t *testing.T) {
	raw, err := os.ReadFile("helper_linux.go")
	if err != nil {
		t.Fatalf("read helper_linux.go: %v", err)
	}
	source := string(raw)

	if strings.Contains(source, "}{unix.RLIMIT_AS, addressSpaceLimitBytes}") {
		t.Fatalf("RLIMIT_AS must not be part of the early helper rlimit batch")
	}

	programBuilt := strings.Index(source, "prog := unix.SockFprog")
	addressSpaceLimit := strings.Index(source, "unix.Setrlimit(unix.RLIMIT_AS")
	seccompInstall := strings.Index(source, "unix.Prctl(unix.PR_SET_SECCOMP")
	if programBuilt < 0 || addressSpaceLimit < 0 || seccompInstall < 0 {
		t.Fatalf("helper_linux.go is missing expected sandbox setup anchors")
	}
	if !(programBuilt < addressSpaceLimit && addressSpaceLimit < seccompInstall) {
		t.Fatalf("RLIMIT_AS must be applied after Go setup and before seccomp install")
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
