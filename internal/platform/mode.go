package platform

import (
	"fmt"
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

func CurrentExecutionMode() (ExecutionMode, error) {
	switch raw := strings.TrimSpace(strings.ToLower(os.Getenv("AONOHAKO_EXECUTION_MODE"))); raw {
	case "":
		return ExecutionModeLocalDev, nil
	case string(ExecutionModeCloudRun):
		return ExecutionModeCloudRun, nil
	case string(ExecutionModeLocalRoot):
		return ExecutionModeLocalRoot, nil
	case string(ExecutionModeLocalDev):
		return ExecutionModeLocalDev, nil
	}
	return "", fmt.Errorf("unsupported AONOHAKO_EXECUTION_MODE: %q", os.Getenv("AONOHAKO_EXECUTION_MODE"))
}

func CurrentRuntimeOptions() (RuntimeOptions, error) {
	mode, err := CurrentExecutionMode()
	if err != nil {
		return RuntimeOptions{}, err
	}
	legacy := runtimeOptionsForMode(mode)
	target := legacy.DeploymentTarget
	switch raw := strings.TrimSpace(strings.ToLower(os.Getenv("AONOHAKO_DEPLOYMENT_TARGET"))); raw {
	case "":
	case string(DeploymentTargetCloudRun):
		target = DeploymentTargetCloudRun
	case string(DeploymentTargetSelfHosted):
		target = DeploymentTargetSelfHosted
	case string(DeploymentTargetDev):
		target = DeploymentTargetDev
	default:
		return RuntimeOptions{}, fmt.Errorf("unsupported AONOHAKO_DEPLOYMENT_TARGET: %q", os.Getenv("AONOHAKO_DEPLOYMENT_TARGET"))
	}

	transport := legacy.ExecutionTransport
	switch raw := strings.TrimSpace(strings.ToLower(os.Getenv("AONOHAKO_EXECUTION_TRANSPORT"))); raw {
	case "":
	case string(ExecutionTransportRemote):
		transport = ExecutionTransportRemote
	case string(ExecutionTransportEmbedded):
		transport = ExecutionTransportEmbedded
	default:
		return RuntimeOptions{}, fmt.Errorf("unsupported AONOHAKO_EXECUTION_TRANSPORT: %q", os.Getenv("AONOHAKO_EXECUTION_TRANSPORT"))
	}

	backend := legacy.SandboxBackend
	switch raw := strings.TrimSpace(strings.ToLower(os.Getenv("AONOHAKO_SANDBOX_BACKEND"))); raw {
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
	default:
		return RuntimeOptions{}, fmt.Errorf("unsupported AONOHAKO_SANDBOX_BACKEND: %q", os.Getenv("AONOHAKO_SANDBOX_BACKEND"))
	}

	return RuntimeOptions{
		DeploymentTarget:   target,
		ExecutionTransport: transport,
		SandboxBackend:     backend,
	}, nil
}

func IsCloudRun() bool {
	opts, err := CurrentRuntimeOptions()
	return err == nil && opts.DeploymentTarget == DeploymentTargetCloudRun
}

func CloudRunMarkersPresent() bool {
	for _, key := range []string{"K_SERVICE", "CLOUD_RUN_JOB", "CLOUD_RUN_WORKER_POOL"} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func UsesDedicatedWorkRoot() (bool, error) {
	opts, err := CurrentRuntimeOptions()
	if err != nil {
		return false, err
	}
	if opts.DeploymentTarget == DeploymentTargetCloudRun {
		return true, nil
	}
	return opts.DeploymentTarget == DeploymentTargetSelfHosted && opts.ExecutionTransport == ExecutionTransportEmbedded && opts.SandboxBackend == SandboxBackendHelper, nil
}

func RequiresRootForExecution() (bool, error) {
	opts, err := CurrentRuntimeOptions()
	if err != nil {
		return false, err
	}
	return opts.ExecutionTransport == ExecutionTransportEmbedded && opts.SandboxBackend == SandboxBackendHelper, nil
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
