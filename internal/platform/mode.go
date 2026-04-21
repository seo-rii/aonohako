package platform

import (
	"os"
	"strings"
)

type ExecutionMode string

const (
	ExecutionModeLocalDev  ExecutionMode = "local-dev"
	ExecutionModeLocalRoot ExecutionMode = "local-root"
	ExecutionModeCloudRun  ExecutionMode = "cloudrun"
)

type DeploymentTarget string

const (
	DeploymentTargetDev        DeploymentTarget = "dev"
	DeploymentTargetSelfHosted DeploymentTarget = "selfhosted"
	DeploymentTargetCloudRun   DeploymentTarget = "cloudrun"
)

type ExecutionTransport string

const (
	ExecutionTransportEmbedded ExecutionTransport = "embedded"
	ExecutionTransportRemote   ExecutionTransport = "remote"
)

type SandboxBackend string

const (
	SandboxBackendHelper    SandboxBackend = "helper"
	SandboxBackendContainer SandboxBackend = "container"
	SandboxBackendNone      SandboxBackend = "none"
)

type RuntimeOptions struct {
	DeploymentTarget   DeploymentTarget
	ExecutionTransport ExecutionTransport
	SandboxBackend     SandboxBackend
}

func CurrentExecutionMode() ExecutionMode {
	switch strings.TrimSpace(strings.ToLower(os.Getenv("AONOHAKO_EXECUTION_MODE"))) {
	case string(ExecutionModeCloudRun):
		return ExecutionModeCloudRun
	case string(ExecutionModeLocalRoot):
		return ExecutionModeLocalRoot
	default:
		return ExecutionModeLocalDev
	}
}

func CurrentRuntimeOptions() RuntimeOptions {
	legacy := runtimeOptionsForMode(CurrentExecutionMode())
	target := legacy.DeploymentTarget
	switch strings.TrimSpace(strings.ToLower(os.Getenv("AONOHAKO_DEPLOYMENT_TARGET"))) {
	case string(DeploymentTargetCloudRun):
		target = DeploymentTargetCloudRun
	case string(DeploymentTargetSelfHosted):
		target = DeploymentTargetSelfHosted
	case string(DeploymentTargetDev):
		target = DeploymentTargetDev
	}

	transport := legacy.ExecutionTransport
	switch strings.TrimSpace(strings.ToLower(os.Getenv("AONOHAKO_EXECUTION_TRANSPORT"))) {
	case string(ExecutionTransportRemote):
		transport = ExecutionTransportRemote
	case string(ExecutionTransportEmbedded):
		transport = ExecutionTransportEmbedded
	}

	backend := legacy.SandboxBackend
	switch strings.TrimSpace(strings.ToLower(os.Getenv("AONOHAKO_SANDBOX_BACKEND"))) {
	case string(SandboxBackendContainer):
		backend = SandboxBackendContainer
	case string(SandboxBackendNone):
		backend = SandboxBackendNone
	case string(SandboxBackendHelper):
		backend = SandboxBackendHelper
	case "":
		if transport == ExecutionTransportRemote {
			backend = SandboxBackendNone
		}
	}

	return RuntimeOptions{
		DeploymentTarget:   target,
		ExecutionTransport: transport,
		SandboxBackend:     backend,
	}
}

func IsCloudRun() bool {
	return CurrentRuntimeOptions().DeploymentTarget == DeploymentTargetCloudRun
}

func CloudRunMarkersPresent() bool {
	for _, key := range []string{"K_SERVICE", "CLOUD_RUN_JOB", "CLOUD_RUN_WORKER_POOL"} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func UsesDedicatedWorkRoot() bool {
	opts := CurrentRuntimeOptions()
	if opts.DeploymentTarget == DeploymentTargetCloudRun {
		return true
	}
	return opts.DeploymentTarget == DeploymentTargetSelfHosted && opts.ExecutionTransport == ExecutionTransportEmbedded && opts.SandboxBackend == SandboxBackendHelper
}

func RequiresRootForExecution() bool {
	opts := CurrentRuntimeOptions()
	return opts.ExecutionTransport == ExecutionTransportEmbedded && opts.SandboxBackend == SandboxBackendHelper
}

func runtimeOptionsForMode(mode ExecutionMode) RuntimeOptions {
	switch mode {
	case ExecutionModeCloudRun:
		return RuntimeOptions{
			DeploymentTarget:   DeploymentTargetCloudRun,
			ExecutionTransport: ExecutionTransportEmbedded,
			SandboxBackend:     SandboxBackendHelper,
		}
	case ExecutionModeLocalRoot:
		return RuntimeOptions{
			DeploymentTarget:   DeploymentTargetSelfHosted,
			ExecutionTransport: ExecutionTransportEmbedded,
			SandboxBackend:     SandboxBackendHelper,
		}
	default:
		return RuntimeOptions{
			DeploymentTarget:   DeploymentTargetDev,
			ExecutionTransport: ExecutionTransportEmbedded,
			SandboxBackend:     SandboxBackendHelper,
		}
	}
}
