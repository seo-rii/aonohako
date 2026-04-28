package execute

import (
	"context"
	"fmt"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

type Runner interface {
	Run(context.Context, *model.RunRequest, Hooks) model.RunResponse
}

func Build(cfg config.Config) (Runner, error) {
	switch cfg.Execution.Platform.ExecutionTransport {
	case platform.ExecutionTransportEmbedded:
		if cfg.Execution.Platform.SandboxBackend != platform.SandboxBackendHelper {
			return nil, fmt.Errorf("embedded execution does not support sandbox backend %s", cfg.Execution.Platform.SandboxBackend)
		}
		profiles := make(map[string]config.RuntimeTuningConfig, len(cfg.Execution.RuntimeTuningProfiles))
		for name, tuning := range cfg.Execution.RuntimeTuningProfiles {
			profiles[name] = tuning.WithSafeDefaults()
		}
		return &Service{deploymentTarget: cfg.Execution.Platform.DeploymentTarget, runtimeTuning: cfg.Execution.RuntimeTuning.WithSafeDefaults(), runtimeTuningProfiles: profiles, cgroupParentDir: cfg.Execution.Cgroup.ParentDir}, nil
	case platform.ExecutionTransportRemote:
		if cfg.Execution.Platform.SandboxBackend != platform.SandboxBackendNone {
			return nil, fmt.Errorf("remote execution requires sandbox backend none")
		}
		return newRemoteRunner(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported execution transport: %s", cfg.Execution.Platform.ExecutionTransport)
	}
}
