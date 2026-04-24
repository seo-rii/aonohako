package execute

import (
	"os"
	"testing"
)

func TestSandboxSecurityRegressionSuite(t *testing.T) {
	if os.Getenv("AONOHAKO_ENFORCE_SANDBOX_TESTS") == "" {
		t.Skip("set AONOHAKO_ENFORCE_SANDBOX_TESTS=1 to run the root-backed sandbox security suite")
	}

	tests := []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "network", fn: TestRunBlocksNetworkWhenDisabled},
		{name: "cloudrun-network", fn: TestRunBlocksNetworkOnCloudRunWithoutDirectModeFallback},
		{name: "enabled-network-outbound", fn: TestRunAllowsOutboundNetworkWhenEnabledOutsideCloudRun},
		{name: "enabled-network-unix-block", fn: TestRunBlocksUnixSocketConnectWhenNetworkEnabled},
		{name: "unix-stream-connect", fn: TestRunBlocksUnixSocketConnectWhenNetworkDisabled},
		{name: "unix-datagram-send", fn: TestRunBlocksUnixDatagramSendWhenNetworkDisabled},
		{name: "unix-datagram-accessible-send", fn: TestRunBlocksUnixDatagramSendToAccessibleSocketWhenNetworkDisabled},
		{name: "socketpair", fn: TestRunBlocksSocketPairCreationWhenNetworkDisabled},
		{name: "namespace", fn: TestRunBlocksNamespaceEscapeAttempts},
		{name: "process-group", fn: TestRunBlocksProcessGroupEscapeAttempts},
		{name: "sibling-signal", fn: TestRunCannotSignalSiblingProcess},
		{name: "host-path", fn: TestRunCannotReadHostPathOutsideSandbox},
		{name: "devices", fn: TestRunExposesOnlySafeDevices},
		{name: "fork", fn: TestRunBlocksForkAttempts},
		{name: "execveat", fn: TestRunBlocksExecveatAttempts},
		{name: "proc-fd", fn: TestRunBlocksProcFDBrowsingOutsideSandbox},
		{name: "scratch-writes", fn: TestRunBlocksWritesOutsideWorkspaceTempDirs},
		{name: "submitted-file-removal", fn: TestRunPreventsRemovingOrReplacingSubmittedFiles},
		{name: "submitted-file-overwrite", fn: TestRunPreventsOverwritingSubmittedFilesButAllowsNewFiles},
		{name: "thread-storm", fn: TestRunBlocksThreadStorms},
		{name: "nested-path-permissions", fn: TestMaterializeFilesKeepsNestedPathsReadableAndWritableToSandboxUser},
		{name: "java-jar-permissions", fn: TestMaterializeFilesBuildsReadableSubmissionJarForSandboxUser},
	}

	for _, tc := range tests {
		t.Run(tc.name, tc.fn)
	}
}
