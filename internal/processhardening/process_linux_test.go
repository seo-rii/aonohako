//go:build linux

package processhardening

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestDisableDumpability(t *testing.T) {
	original, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("get original dumpability: %v", err)
	}
	defer func() {
		if err := unix.Prctl(unix.PR_SET_DUMPABLE, uintptr(original), 0, 0, 0); err != nil {
			t.Fatalf("restore dumpability: %v", err)
		}
	}()

	if err := DisableDumpability(); err != nil {
		t.Fatalf("DisableDumpability: %v", err)
	}
	got, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("get dumpability after hardening: %v", err)
	}
	if got != 0 {
		t.Fatalf("dumpability = %d, want 0", got)
	}
}
