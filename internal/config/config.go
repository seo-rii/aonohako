package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/platform"
)

type RemoteAuthMode string

const (
	RemoteAuthNone            RemoteAuthMode = "none"
	RemoteAuthBearer          RemoteAuthMode = "bearer"
	RemoteAuthCloudRunIDToken RemoteAuthMode = "cloudrun-idtoken"
)

type RemoteExecutorConfig struct {
	URL         string
	Auth        RemoteAuthMode
	BearerToken string
	Audience    string
}

type ExecutionConfig struct {
	Platform platform.RuntimeOptions
	Remote   RemoteExecutorConfig
}

type Config struct {
	Port              string
	MaxActiveRuns     int
	MaxPendingQueue   int
	HeartbeatInterval time.Duration
	Execution         ExecutionConfig
}

func Load() (Config, error) {
	port := getenv("PORT", "8080")
	runtimePlatform, err := platform.CurrentRuntimeOptions()
	if err != nil {
		return Config{}, err
	}
	maxActive := parsePositiveInt(getenv("AONOHAKO_MAX_ACTIVE_RUNS", ""), defaultMaxActiveRuns(runtimePlatform))
	maxPending := parseNonNegativeInt(getenv("AONOHAKO_MAX_PENDING_QUEUE", "0"), 0)
	heartbeatSec := parsePositiveInt(getenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", "10"), 10)
	execution := ExecutionConfig{
		Platform: runtimePlatform,
		Remote: RemoteExecutorConfig{
			URL:         strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_URL")),
			Auth:        parseRemoteAuth(os.Getenv("AONOHAKO_REMOTE_RUNNER_AUTH")),
			BearerToken: strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_TOKEN")),
			Audience:    strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_AUDIENCE")),
		},
	}
	workRoot := strings.TrimSpace(os.Getenv("AONOHAKO_WORK_ROOT"))

	if platform.CloudRunMarkersPresent() && execution.Platform.DeploymentTarget != platform.DeploymentTargetCloudRun {
		return Config{}, fmt.Errorf("AONOHAKO_DEPLOYMENT_TARGET=cloudrun is required when Cloud Run markers are present")
	}
	switch execution.Platform.ExecutionTransport {
	case platform.ExecutionTransportEmbedded:
		if execution.Platform.SandboxBackend != platform.SandboxBackendHelper {
			return Config{}, fmt.Errorf("embedded execution supports only helper sandbox backend")
		}
	case platform.ExecutionTransportRemote:
		if execution.Platform.SandboxBackend != platform.SandboxBackendNone {
			return Config{}, fmt.Errorf("remote execution requires AONOHAKO_SANDBOX_BACKEND=none")
		}
		if execution.Remote.URL == "" {
			return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_URL is required for remote execution")
		}
		switch execution.Remote.Auth {
		case RemoteAuthNone:
		case RemoteAuthBearer:
			if execution.Remote.BearerToken == "" {
				return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_TOKEN is required for bearer remote auth")
			}
		case RemoteAuthCloudRunIDToken:
			if execution.Remote.Audience == "" {
				execution.Remote.Audience = execution.Remote.URL
			}
		default:
			return Config{}, fmt.Errorf("unsupported remote auth mode: %s", execution.Remote.Auth)
		}
	default:
		return Config{}, fmt.Errorf("unsupported execution transport: %s", execution.Platform.ExecutionTransport)
	}

	if execution.Platform.ExecutionTransport == platform.ExecutionTransportEmbedded && execution.Platform.SandboxBackend == platform.SandboxBackendHelper && maxActive != 1 {
		return Config{}, fmt.Errorf("embedded helper execution requires AONOHAKO_MAX_ACTIVE_RUNS=1")
	}

	if execution.Platform.DeploymentTarget == platform.DeploymentTargetCloudRun || (execution.Platform.DeploymentTarget == platform.DeploymentTargetSelfHosted && execution.Platform.ExecutionTransport == platform.ExecutionTransportEmbedded && execution.Platform.SandboxBackend == platform.SandboxBackendHelper) {
		if workRoot == "" {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT is required for %s target", execution.Platform.DeploymentTarget)
		}
		info, err := os.Stat(workRoot)
		if err != nil {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT validation failed: %w", err)
		}
		if !info.IsDir() {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT is not a directory: %s", workRoot)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT must not be group/world writable: %s", workRoot)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT must be owned by uid %d", os.Geteuid())
		}
		probe, err := os.MkdirTemp(workRoot, ".aonohako-contract-*")
		if err != nil {
			return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT is not writable: %w", err)
		}
		_ = os.RemoveAll(probe)
	}
	requiresRoot := execution.Platform.ExecutionTransport == platform.ExecutionTransportEmbedded && execution.Platform.SandboxBackend == platform.SandboxBackendHelper
	if requiresRoot && os.Geteuid() != 0 {
		return Config{}, fmt.Errorf("execution backend %s/%s requires root", execution.Platform.ExecutionTransport, execution.Platform.SandboxBackend)
	}

	return Config{
		Port:              port,
		MaxActiveRuns:     maxActive,
		MaxPendingQueue:   maxPending,
		HeartbeatInterval: time.Duration(heartbeatSec) * time.Second,
		Execution:         execution,
	}, nil
}

func defaultMaxActiveRuns(opts platform.RuntimeOptions) int {
	if opts.ExecutionTransport == platform.ExecutionTransportEmbedded && opts.SandboxBackend == platform.SandboxBackendHelper {
		return 1
	}
	if opts.DeploymentTarget == platform.DeploymentTargetCloudRun {
		return 1
	}
	cpu := runtime.NumCPU()
	if cpu <= 1 {
		return 1
	}
	v := cpu - 2
	if v < 1 {
		return 1
	}
	return v
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parsePositiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func parseNonNegativeInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

func parseRemoteAuth(raw string) RemoteAuthMode {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", string(RemoteAuthNone):
		return RemoteAuthNone
	case string(RemoteAuthBearer):
		return RemoteAuthBearer
	case string(RemoteAuthCloudRunIDToken):
		return RemoteAuthCloudRunIDToken
	default:
		return RemoteAuthMode(strings.TrimSpace(strings.ToLower(raw)))
	}
}
