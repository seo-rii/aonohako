//go:build linux

package processhardening

import "golang.org/x/sys/unix"

// DisableDumpability prevents same-container untrusted UIDs from using ptrace
// style procfs access against this long-lived parent process.
func DisableDumpability() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
