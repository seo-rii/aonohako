package compile

import (
	"os"
	"testing"
)

func TestCompileSandboxSecurityRegressionSuite(t *testing.T) {
	if os.Getenv("AONOHAKO_ENFORCE_SANDBOX_TESTS") == "" {
		t.Skip("set AONOHAKO_ENFORCE_SANDBOX_TESTS=1 to run the root-backed compile sandbox security suite")
	}

	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "python-sitecustomize", fn: TestRunPythonCompileDoesNotExecuteSitecustomize},
		{name: "background-children", fn: TestRunCommandKillsBackgroundChildren},
		{name: "nested-source-writes", fn: TestRunSandboxedCommandAllowsWritesBesideNestedCompileSources},
		{name: "submitted-source-immutability", fn: TestRunSandboxedCommandPreventsRemovingOrReplacingSubmittedCompileSources},
		{name: "network", fn: TestRunCommandRejectsNetworkSockets},
		{name: "unix-stream-connect", fn: TestRunCommandRejectsUnixSocketConnectToHost},
		{name: "unix-datagram-sendmsg", fn: TestRunCommandRejectsUnixSocketSendmsgToHost},
		{name: "namespace", fn: TestRunCommandRejectsNamespaceEscape},
		{name: "process-group", fn: TestRunCommandRejectsProcessGroupEscape},
		{name: "filesystem-privilege-syscalls", fn: TestRunCommandRejectsFilesystemPrivilegeSyscalls},
		{name: "host-path", fn: TestRunCommandCannotReadOrWriteRootOwnedHostPaths},
		{name: "fd-leak", fn: TestRunCommandDoesNotLeakInheritedFileDescriptors},
	}

	for _, tc := range tests {
		t.Run(tc.name, tc.fn)
	}
}
