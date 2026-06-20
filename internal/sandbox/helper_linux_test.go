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
