package execute

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/timing"
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
		if cfg.Execution.Platform.DeploymentTarget == platform.DeploymentTargetSelfHosted && strings.TrimSpace(cfg.Execution.Cgroup.ParentDir) == "" {
			return nil, fmt.Errorf("selfhosted embedded helper execution requires a cgroup parent")
		}
		service := NewWithConfig(cfg)
		if cfg.Execution.CPUNormalization.Enabled {
			normalizer, err := timing.CalibrateCPU(cfg.Execution.CPUNormalization.ReferenceTimeNs)
			if err != nil {
				return nil, fmt.Errorf("CPU normalization calibration failed: %w", err)
			}
			service.cpuNormalizer = normalizer
			info, _ := normalizer.Info()
			slog.Info(
				"aonohako CPU normalization calibrated",
				"method", info.Method,
				"scale_ppm", info.ScalePPM,
				"reference_time_ns", info.ReferenceTimeNs,
				"observed_time_ns", info.ObservedTimeNs,
			)
		}
		return service, nil
	case platform.ExecutionTransportRemote:
		if cfg.Execution.Platform.SandboxBackend != platform.SandboxBackendNone {
			return nil, fmt.Errorf("remote execution requires sandbox backend none")
		}
		return newRemoteRunner(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported execution transport: %s", cfg.Execution.Platform.ExecutionTransport)
	}
}
