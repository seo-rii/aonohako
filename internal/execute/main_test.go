package execute

import (
	"os"
	"testing"

	"aonohako/internal/sandbox"
)

func TestMain(m *testing.M) {
	if sandbox.MaybeRunFromEnv() {
		return
	}
	os.Exit(m.Run())
}
