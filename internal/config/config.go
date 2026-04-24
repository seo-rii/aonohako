package config

import (
	"fmt"
	"net/url"
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

type InboundAuthMode string

const (
	InboundAuthNone     InboundAuthMode = "none"
	InboundAuthBearer   InboundAuthMode = "bearer"
	InboundAuthPlatform InboundAuthMode = "platform"
)

type RemoteExecutorConfig struct {
	URL         string
	Auth        RemoteAuthMode
	BearerToken string
	Audience    string
}

type InboundAuthConfig struct {
	Mode        InboundAuthMode
	BearerToken string
}

type ExecutionConfig struct {
	Platform platform.RuntimeOptions
	Remote   RemoteExecutorConfig
}

const (
	defaultMaxPendingQueue  = 16
	defaultMaxActiveStreams = 64
)

type Config struct {
	Port                          string
	MaxActiveRuns                 int
	MaxPendingQueue               int
	MaxActiveStreams              int
	MaxPrincipalStreams           int
	MaxPrincipalRequestsPerMinute int
	HeartbeatInterval             time.Duration
	Execution                     ExecutionConfig
	InboundAuth                   InboundAuthConfig
}

func Load() (Config, error) {
	port := getenv("PORT", "8080")
	runtimePlatform, err := platform.CurrentRuntimeOptions()
	if err != nil {
		return Config{}, err
	}
	maxActive, err := parsePositiveIntEnv("AONOHAKO_MAX_ACTIVE_RUNS", os.Getenv("AONOHAKO_MAX_ACTIVE_RUNS"), defaultMaxActiveRuns(runtimePlatform))
	if err != nil {
		return Config{}, err
	}
	maxPending, err := parseNonNegativeIntEnv("AONOHAKO_MAX_PENDING_QUEUE", os.Getenv("AONOHAKO_MAX_PENDING_QUEUE"), defaultMaxPendingQueue)
	if err != nil {
		return Config{}, err
	}
	maxActiveStreams, err := parseNonNegativeIntEnv("AONOHAKO_MAX_ACTIVE_STREAMS", os.Getenv("AONOHAKO_MAX_ACTIVE_STREAMS"), defaultMaxActiveStreams)
	if err != nil {
		return Config{}, err
	}
	maxPrincipalStreams, err := parseNonNegativeIntEnv("AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS", os.Getenv("AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS"), defaultMaxPrincipalStreams(runtimePlatform))
	if err != nil {
		return Config{}, err
	}
	maxPrincipalRequestsPerMinute, err := parseNonNegativeIntEnv("AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE", os.Getenv("AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE"), defaultMaxPrincipalRequestsPerMinute(runtimePlatform))
	if err != nil {
		return Config{}, err
	}
	heartbeatSec, err := parsePositiveIntEnv("AONOHAKO_HEARTBEAT_INTERVAL_SEC", os.Getenv("AONOHAKO_HEARTBEAT_INTERVAL_SEC"), 10)
	if err != nil {
		return Config{}, err
	}
	execution := ExecutionConfig{
		Platform: runtimePlatform,
		Remote: RemoteExecutorConfig{
			URL:         strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_URL")),
			Auth:        parseRemoteAuth(os.Getenv("AONOHAKO_REMOTE_RUNNER_AUTH")),
			BearerToken: strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_TOKEN")),
			Audience:    strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_AUDIENCE")),
		},
	}
	inboundAuth := InboundAuthConfig{
		Mode:        parseInboundAuth(os.Getenv("AONOHAKO_INBOUND_AUTH"), runtimePlatform),
		BearerToken: strings.TrimSpace(os.Getenv("AONOHAKO_API_BEARER_TOKEN")),
	}
	workRoot := strings.TrimSpace(os.Getenv("AONOHAKO_WORK_ROOT"))

	if platform.CloudRunMarkersPresent() && execution.Platform.DeploymentTarget != platform.DeploymentTargetCloudRun {
		return Config{}, fmt.Errorf("AONOHAKO_DEPLOYMENT_TARGET=cloudrun is required when Cloud Run markers are present")
	}
	contract, err := execution.Platform.SecurityContract()
	if err != nil {
		return Config{}, err
	}
	if !contract.Implemented {
		return Config{}, fmt.Errorf("sandbox contract %s is reserved and is not implemented", contract.Name)
	}

	if execution.Platform.ExecutionTransport == platform.ExecutionTransportRemote {
		if execution.Remote.URL == "" {
			return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_URL is required for remote execution")
		}
		parsedURL, err := url.Parse(execution.Remote.URL)
		if err != nil {
			return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_URL is invalid: %w", err)
		}
		if !parsedURL.IsAbs() || parsedURL.Host == "" {
			return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_URL must be an absolute http(s) URL")
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_URL must use http or https")
		}
		if parsedURL.User != nil {
			return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_URL must not include credentials")
		}
		if parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
			return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_URL must not include query or fragment components")
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
	}

	switch inboundAuth.Mode {
	case InboundAuthNone, InboundAuthPlatform:
	case InboundAuthBearer:
		if inboundAuth.BearerToken == "" {
			return Config{}, fmt.Errorf("AONOHAKO_API_BEARER_TOKEN is required for bearer inbound auth")
		}
	default:
		return Config{}, fmt.Errorf("unsupported inbound auth mode: %s", inboundAuth.Mode)
	}

	if contract.RequiresSingleActiveRun && maxActive != 1 {
		return Config{}, fmt.Errorf("embedded helper execution requires AONOHAKO_MAX_ACTIVE_RUNS=1")
	}

	if contract.RequiresDedicatedWorkRoot {
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
	if contract.RequiresRootParent && os.Geteuid() != 0 {
		return Config{}, fmt.Errorf("execution backend %s/%s requires root", execution.Platform.ExecutionTransport, execution.Platform.SandboxBackend)
	}

	return Config{
		Port:                          port,
		MaxActiveRuns:                 maxActive,
		MaxPendingQueue:               maxPending,
		MaxActiveStreams:              maxActiveStreams,
		MaxPrincipalStreams:           maxPrincipalStreams,
		MaxPrincipalRequestsPerMinute: maxPrincipalRequestsPerMinute,
		HeartbeatInterval:             time.Duration(heartbeatSec) * time.Second,
		Execution:                     execution,
		InboundAuth:                   inboundAuth,
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

func defaultMaxPrincipalStreams(opts platform.RuntimeOptions) int {
	if opts.DeploymentTarget == platform.DeploymentTargetDev {
		return 0
	}
	return 16
}

func defaultMaxPrincipalRequestsPerMinute(opts platform.RuntimeOptions) int {
	if opts.DeploymentTarget == platform.DeploymentTargetDev {
		return 0
	}
	return 60
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parsePositiveIntEnv(key, raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return v, nil
}

func parseNonNegativeIntEnv(key, raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return v, nil
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

func parseInboundAuth(raw string, opts platform.RuntimeOptions) InboundAuthMode {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "":
		if opts.DeploymentTarget == platform.DeploymentTargetDev {
			return InboundAuthNone
		}
		return InboundAuthBearer
	case string(InboundAuthNone):
		return InboundAuthNone
	case string(InboundAuthBearer):
		return InboundAuthBearer
	case string(InboundAuthPlatform):
		return InboundAuthPlatform
	default:
		return InboundAuthMode(strings.TrimSpace(strings.ToLower(raw)))
	}
}
