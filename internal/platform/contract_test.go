package platform

import "testing"

func TestSecurityContractDescribesEmbeddedHelperBoundary(t *testing.T) {
	contract, err := (RuntimeOptions{
		DeploymentTarget:   DeploymentTargetSelfHosted,
		ExecutionTransport: ExecutionTransportEmbedded,
		SandboxBackend:     SandboxBackendHelper,
	}).SecurityContract()
	if err != nil {
		t.Fatalf("SecurityContract() error = %v", err)
	}
	if contract.Name != "embedded-helper-process-hardening" {
		t.Fatalf("contract name = %q", contract.Name)
	}
	if !contract.Implemented {
		t.Fatalf("helper contract should be implemented")
	}
	if !contract.RequiresRootParent {
		t.Fatalf("helper contract should require a root parent")
	}
	if !contract.RequiresDedicatedWorkRoot {
		t.Fatalf("selfhosted helper contract should require a dedicated work root")
	}
	if !contract.RequiresSingleActiveRun {
		t.Fatalf("helper contract should require a single active run")
	}
	for _, want := range []SecurityCapability{
		CapabilitySetrlimit,
		CapabilityNoNewPrivileges,
		CapabilitySeccompDenylist,
		CapabilityNetworkSyscallGate,
		CapabilityImmutableSubmissions,
		CapabilitySymlinkSafeOutputCapture,
		CapabilityWorkspaceAccounting,
		CapabilitySingleSandboxUID,
	} {
		if !hasCapability(contract.Capabilities, want) {
			t.Fatalf("helper contract missing capability %q: %+v", want, contract.Capabilities)
		}
	}
	for _, want := range []SecurityCapability{
		CapabilityPerRunCgroup,
		CapabilityMountNamespace,
		CapabilityReadOnlyRootFS,
		CapabilityMaskedProc,
		CapabilityPerRunUID,
		CapabilityChildProcessAccounting,
		CapabilitySeccompAllowlist,
	} {
		if !hasCapability(contract.MissingCapabilities, want) {
			t.Fatalf("helper contract should record missing capability %q: %+v", want, contract.MissingCapabilities)
		}
	}
}

func TestSecurityContractDescribesRemoteControlPlane(t *testing.T) {
	contract, err := (RuntimeOptions{
		DeploymentTarget:   DeploymentTargetDev,
		ExecutionTransport: ExecutionTransportRemote,
		SandboxBackend:     SandboxBackendNone,
	}).SecurityContract()
	if err != nil {
		t.Fatalf("SecurityContract() error = %v", err)
	}
	if contract.Name != "remote-control-plane" {
		t.Fatalf("contract name = %q", contract.Name)
	}
	if !contract.Implemented {
		t.Fatalf("remote contract should be implemented")
	}
	if contract.RequiresRootParent {
		t.Fatalf("remote control plane must not require root")
	}
	if contract.RequiresSingleActiveRun {
		t.Fatalf("remote control plane should not force local single-slot execution")
	}
	if contract.RequiresDedicatedWorkRoot {
		t.Fatalf("dev remote control plane should not require a local work root")
	}
	if !contract.DelegatesIsolation {
		t.Fatalf("remote contract should record delegated isolation")
	}
	for _, want := range []SecurityCapability{
		CapabilityCompileExecuteForwarding,
		CapabilityNoLocalUntrustedWork,
	} {
		if !hasCapability(contract.Capabilities, want) {
			t.Fatalf("remote contract missing capability %q: %+v", want, contract.Capabilities)
		}
	}
}

func TestSecurityContractCloudRunRemoteRequiresWorkRoot(t *testing.T) {
	contract, err := (RuntimeOptions{
		DeploymentTarget:   DeploymentTargetCloudRun,
		ExecutionTransport: ExecutionTransportRemote,
		SandboxBackend:     SandboxBackendNone,
	}).SecurityContract()
	if err != nil {
		t.Fatalf("SecurityContract() error = %v", err)
	}
	if !contract.RequiresDedicatedWorkRoot {
		t.Fatalf("cloudrun remote control plane should keep the bounded work root requirement")
	}
}

func TestSecurityContractContainerBackendIsReserved(t *testing.T) {
	contract, err := (RuntimeOptions{
		DeploymentTarget:   DeploymentTargetSelfHosted,
		ExecutionTransport: ExecutionTransportEmbedded,
		SandboxBackend:     SandboxBackendContainer,
	}).SecurityContract()
	if err != nil {
		t.Fatalf("SecurityContract() error = %v", err)
	}
	if contract.Implemented {
		t.Fatalf("container backend should remain reserved")
	}
	for _, want := range []SecurityCapability{
		CapabilityPerRunCgroup,
		CapabilityMountNamespace,
		CapabilityReadOnlyRootFS,
		CapabilityMaskedProc,
		CapabilityPerRunUID,
		CapabilityChildProcessAccounting,
		CapabilitySeccompAllowlist,
	} {
		if !hasCapability(contract.Capabilities, want) {
			t.Fatalf("reserved container contract missing future capability %q: %+v", want, contract.Capabilities)
		}
	}
}

func TestSecurityContractRejectsUnsupportedCombinations(t *testing.T) {
	tests := []RuntimeOptions{
		{DeploymentTarget: DeploymentTargetSelfHosted, ExecutionTransport: ExecutionTransportRemote, SandboxBackend: SandboxBackendHelper},
		{DeploymentTarget: DeploymentTargetSelfHosted, ExecutionTransport: ExecutionTransportEmbedded, SandboxBackend: SandboxBackendNone},
	}
	for _, opts := range tests {
		t.Run(string(opts.ExecutionTransport)+"/"+string(opts.SandboxBackend), func(t *testing.T) {
			if _, err := opts.SecurityContract(); err == nil {
				t.Fatalf("SecurityContract(%+v) should reject unsupported combination", opts)
			}
		})
	}
}

func hasCapability(caps []SecurityCapability, want SecurityCapability) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}
