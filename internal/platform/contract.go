package platform

import "fmt"

type SecurityCapability string

const (
	CapabilityCompileExecuteForwarding SecurityCapability = "compile-execute-forwarding"
	CapabilityNoLocalUntrustedWork     SecurityCapability = "no-local-untrusted-work"
	CapabilitySetrlimit                SecurityCapability = "setrlimit"
	CapabilityNoNewPrivileges          SecurityCapability = "no-new-privileges"
	CapabilitySeccompDenylist          SecurityCapability = "seccomp-denylist"
	CapabilityNetworkSyscallGate       SecurityCapability = "network-syscall-gate"
	CapabilityFDCleanup                SecurityCapability = "fd-cleanup"
	CapabilityProcessGroupCleanup      SecurityCapability = "process-group-cleanup"
	CapabilityImmutableSubmissions     SecurityCapability = "immutable-submissions"
	CapabilitySymlinkSafeOutputCapture SecurityCapability = "symlink-safe-output-capture"
	CapabilityWorkspaceAccounting      SecurityCapability = "workspace-accounting"
	CapabilitySingleSandboxUID         SecurityCapability = "single-sandbox-uid"
	CapabilityPerRunCgroup             SecurityCapability = "per-run-cgroup"
	CapabilityMountNamespace           SecurityCapability = "mount-namespace"
	CapabilityReadOnlyRootFS           SecurityCapability = "read-only-rootfs"
	CapabilityMaskedProc               SecurityCapability = "masked-proc"
	CapabilityPerRunUID                SecurityCapability = "per-run-uid"
	CapabilityChildProcessAccounting   SecurityCapability = "child-process-accounting"
	CapabilitySeccompAllowlist         SecurityCapability = "seccomp-allowlist"
)

type SecurityContract struct {
	Name                      string
	Implemented               bool
	RequiresRootParent        bool
	RequiresDedicatedWorkRoot bool
	RequiresSingleActiveRun   bool
	DelegatesIsolation        bool
	Capabilities              []SecurityCapability
	MissingCapabilities       []SecurityCapability
}

func (opts RuntimeOptions) SecurityContract() (SecurityContract, error) {
	switch opts.ExecutionTransport {
	case ExecutionTransportEmbedded:
		switch opts.SandboxBackend {
		case SandboxBackendHelper:
			return SecurityContract{
				Name:                      "embedded-helper-process-hardening",
				Implemented:               true,
				RequiresRootParent:        true,
				RequiresDedicatedWorkRoot: opts.DeploymentTarget == DeploymentTargetCloudRun || opts.DeploymentTarget == DeploymentTargetSelfHosted,
				RequiresSingleActiveRun:   true,
				Capabilities: []SecurityCapability{
					CapabilitySetrlimit,
					CapabilityNoNewPrivileges,
					CapabilitySeccompDenylist,
					CapabilityNetworkSyscallGate,
					CapabilityFDCleanup,
					CapabilityProcessGroupCleanup,
					CapabilityImmutableSubmissions,
					CapabilitySymlinkSafeOutputCapture,
					CapabilityWorkspaceAccounting,
					CapabilitySingleSandboxUID,
				},
				MissingCapabilities: []SecurityCapability{
					CapabilityPerRunCgroup,
					CapabilityMountNamespace,
					CapabilityReadOnlyRootFS,
					CapabilityMaskedProc,
					CapabilityPerRunUID,
					CapabilityChildProcessAccounting,
					CapabilitySeccompAllowlist,
				},
			}, nil
		case SandboxBackendContainer:
			return SecurityContract{
				Name:        "reserved-container-isolation",
				Implemented: false,
				Capabilities: []SecurityCapability{
					CapabilityPerRunCgroup,
					CapabilityMountNamespace,
					CapabilityReadOnlyRootFS,
					CapabilityMaskedProc,
					CapabilityPerRunUID,
					CapabilityChildProcessAccounting,
					CapabilitySeccompAllowlist,
				},
			}, nil
		default:
			return SecurityContract{}, fmt.Errorf("embedded execution supports only helper sandbox backend")
		}
	case ExecutionTransportRemote:
		if opts.SandboxBackend != SandboxBackendNone {
			return SecurityContract{}, fmt.Errorf("remote execution requires AONOHAKO_SANDBOX_BACKEND=none")
		}
		return SecurityContract{
			Name:                      "remote-control-plane",
			Implemented:               true,
			RequiresDedicatedWorkRoot: opts.DeploymentTarget == DeploymentTargetCloudRun,
			DelegatesIsolation:        true,
			Capabilities: []SecurityCapability{
				CapabilityCompileExecuteForwarding,
				CapabilityNoLocalUntrustedWork,
			},
		}, nil
	default:
		return SecurityContract{}, fmt.Errorf("unsupported execution transport: %s", opts.ExecutionTransport)
	}
}
