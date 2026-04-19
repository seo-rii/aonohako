//go:build !linux

package sandbox

import (
	"fmt"
	"os"
)

func MaybeRunFromEnv() bool {
	if os.Getenv(HelperModeEnv) != HelperModeExec {
		return false
	}
	_, _ = fmt.Fprintln(os.Stderr, "sandbox-init: linux sandbox helper is unavailable on this platform")
	os.Exit(120)
	return true
}
