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
		{name: "managed-unix-sendmsg", fn: TestExecuteSandboxBlocksUnixSendmsgForManagedRuntimeSocketAllowance},
		{name: "socketpair", fn: TestRunBlocksSocketPairCreationWhenNetworkDisabled},
		{name: "namespace", fn: TestRunBlocksNamespaceEscapeAttempts},
		{name: "process-group", fn: TestRunBlocksProcessGroupEscapeAttempts},
		{name: "sibling-signal", fn: TestRunCannotSignalSiblingProcess},
		{name: "same-uid-peer-control", fn: TestRunBlocksUnsafePrlimitAndQueuedSignalsAgainstSameUIDPeer},
		{name: "prlimit-safe-query", fn: TestRunAllowsPrlimitQueriesNeededByManagedRuntimes},
		{name: "sysv-message-semaphore", fn: TestRunBlocksSysVMessageQueuesAndSemaphores},
		{name: "host-path", fn: TestRunCannotReadHostPathOutsideSandbox},
		{name: "devices", fn: TestRunExposesOnlySafeDevices},
		{name: "filesystem-metadata-syscalls", fn: TestRunBlocksFilesystemMetadataSyscalls},
		{name: "fork", fn: TestRunBlocksForkAttempts},
		{name: "kernel-attack-surface-syscalls", fn: TestRunBlocksKernelAttackSurfaceSyscalls},
		{name: "execveat", fn: TestRunBlocksExecveatAttempts},
		{name: "spj-clean-workspace", fn: TestRunSPJUsesCleanWorkspaceAndReadableFiles},
		{name: "spj-argument-order", fn: TestRunSPJUsesFileArgumentsWithoutDuplicatingStdoutOnStdin},
		{name: "spj-requested-sidecars", fn: TestRunSPJCanReadRequestedSidecarOutputs},
		{name: "spj-default-sidecars", fn: TestRunSPJCanReadTopLevelSidecarOutputsByDefault},
		{name: "spj-stable-stdin-url", fn: TestRunSPJUsesSingleStableStdinURLFetch},
		{name: "spj-step-input", fn: TestRunStepSPJReceivesExactFinalStepStdin},
		{name: "spj-finite-score", fn: TestRunSPJRejectsNonFiniteScore},
		{name: "interactor-output-limit", fn: TestRunInteractiveUsesInteractorOutputLimit},
		{name: "proc-fd", fn: TestRunBlocksProcFDBrowsingOutsideSandbox},
		{name: "proc-environ", fn: TestRunBlocksProcEnvironRead},
		{name: "proc-sensitive-links", fn: TestRunBlocksSensitiveProcSymlinksOutsideSandbox},
		{name: "scratch-writes", fn: TestRunBlocksWritesOutsideWorkspaceTempDirs},
		{name: "submitted-file-removal", fn: TestRunPreventsRemovingOrReplacingSubmittedFiles},
		{name: "submitted-file-overwrite", fn: TestRunPreventsOverwritingSubmittedFilesButAllowsNewFiles},
		{name: "thread-storm", fn: TestRunBlocksThreadStorms},
		{name: "many-small-files", fn: TestRunMarksWorkspaceEntryLimitExceeded},
		{name: "deep-workspace-tree", fn: TestRunMarksWorkspaceDepthLimitExceeded},
		{name: "nested-path-permissions", fn: TestMaterializeFilesKeepsNestedPathsReadableAndWritableToSandboxUser},
		{name: "java-jar-permissions", fn: TestMaterializeFilesBuildsReadableSubmissionJarForSandboxUser},
	}

	for _, tc := range tests {
		t.Run(tc.name, tc.fn)
	}
}
