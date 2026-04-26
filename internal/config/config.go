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
	"aonohako/internal/remoteio"
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
	URL            string
	Auth           RemoteAuthMode
	BearerToken    string
	Audience       string
	SSEIdleTimeout time.Duration
}

type InboundAuthConfig struct {
	Mode        InboundAuthMode
	BearerToken string
}

type ExecutionConfig struct {
	Platform      platform.RuntimeOptions
	Remote        RemoteExecutorConfig
	RuntimeTuning RuntimeTuningConfig
}

type RuntimeTuningConfig struct {
	JVMHeapPercent            int
	GoMemoryReserveMB         int
	GoGOGC                    int
	NodeOldSpacePercent       int
	NodeMaxSemiSpaceMB        int
	NodeStackSizeKB           int
	WasmtimeMemoryGuardBytes  int
	WasmtimeMaxWasmStackBytes int
}

const (
	defaultMaxPendingQueue  = 16
	defaultMaxActiveStreams = 64

	defaultJVMHeapPercent            = 50
	minJVMHeapPercent                = 25
	maxJVMHeapPercent                = 75
	defaultGoMemoryReserveMB         = 32
	minGoMemoryReserveMB             = 0
	maxGoMemoryReserveMB             = 256
	defaultGoGOGC                    = 50
	minGoGOGC                        = 10
	maxGoGOGC                        = 200
	defaultNodeOldSpacePercent       = 60
	minNodeOldSpacePercent           = 30
	maxNodeOldSpacePercent           = 75
	defaultNodeMaxSemiSpaceMB        = 8
	minNodeMaxSemiSpaceMB            = 1
	maxNodeMaxSemiSpaceMB            = 16
	defaultNodeStackSizeKB           = 2048
	minNodeStackSizeKB               = 512
	maxNodeStackSizeKB               = 8192
	defaultWasmtimeMemoryGuardBytes  = 64 << 10
	minWasmtimeMemoryGuardBytes      = 64 << 10
	maxWasmtimeMemoryGuardBytes      = 16 << 20
	defaultWasmtimeMaxWasmStackBytes = 1 << 20
	minWasmtimeMaxWasmStackBytes     = 256 << 10
	maxWasmtimeMaxWasmStackBytes     = 8 << 20
)

type Config struct {
	Port                          string
	MaxActiveRuns                 int
	MaxPendingQueue               int
	MaxActiveStreams              int
	MaxPrincipalStreams           int
	MaxPrincipalRequestsPerMinute int
	HeartbeatInterval             time.Duration
	BodyReadTimeout               time.Duration
	AllowRequestNetwork           bool
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
	bodyReadTimeoutSec, err := parsePositiveIntEnv("AONOHAKO_BODY_READ_TIMEOUT_SEC", os.Getenv("AONOHAKO_BODY_READ_TIMEOUT_SEC"), 30)
	if err != nil {
		return Config{}, err
	}
	remoteSSEIdleTimeoutSec, err := parsePositiveIntEnv("AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC", os.Getenv("AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC"), int(remoteio.DefaultSSEIdleTimeout/time.Second))
	if err != nil {
		return Config{}, err
	}
	allowRequestNetwork, err := parseBoolEnv("AONOHAKO_ALLOW_REQUEST_NETWORK", os.Getenv("AONOHAKO_ALLOW_REQUEST_NETWORK"), defaultAllowRequestNetwork(runtimePlatform))
	if err != nil {
		return Config{}, err
	}
	runtimeTuning := DefaultRuntimeTuningConfig()
	runtimeTuning.JVMHeapPercent, err = parseBoundedIntEnv("AONOHAKO_JVM_HEAP_PERCENT", os.Getenv("AONOHAKO_JVM_HEAP_PERCENT"), runtimeTuning.JVMHeapPercent, minJVMHeapPercent, maxJVMHeapPercent)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.GoMemoryReserveMB, err = parseBoundedIntEnv("AONOHAKO_GO_MEMORY_RESERVE_MB", os.Getenv("AONOHAKO_GO_MEMORY_RESERVE_MB"), runtimeTuning.GoMemoryReserveMB, minGoMemoryReserveMB, maxGoMemoryReserveMB)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.GoGOGC, err = parseBoundedIntEnv("AONOHAKO_GO_GOGC", os.Getenv("AONOHAKO_GO_GOGC"), runtimeTuning.GoGOGC, minGoGOGC, maxGoGOGC)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.NodeOldSpacePercent, err = parseBoundedIntEnv("AONOHAKO_NODE_OLD_SPACE_PERCENT", os.Getenv("AONOHAKO_NODE_OLD_SPACE_PERCENT"), runtimeTuning.NodeOldSpacePercent, minNodeOldSpacePercent, maxNodeOldSpacePercent)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.NodeMaxSemiSpaceMB, err = parseBoundedIntEnv("AONOHAKO_NODE_MAX_SEMI_SPACE_MB", os.Getenv("AONOHAKO_NODE_MAX_SEMI_SPACE_MB"), runtimeTuning.NodeMaxSemiSpaceMB, minNodeMaxSemiSpaceMB, maxNodeMaxSemiSpaceMB)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.NodeStackSizeKB, err = parseBoundedIntEnv("AONOHAKO_NODE_STACK_SIZE_KB", os.Getenv("AONOHAKO_NODE_STACK_SIZE_KB"), runtimeTuning.NodeStackSizeKB, minNodeStackSizeKB, maxNodeStackSizeKB)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.WasmtimeMemoryGuardBytes, err = parseBoundedIntEnv("AONOHAKO_WASMTIME_MEMORY_GUARD_BYTES", os.Getenv("AONOHAKO_WASMTIME_MEMORY_GUARD_BYTES"), runtimeTuning.WasmtimeMemoryGuardBytes, minWasmtimeMemoryGuardBytes, maxWasmtimeMemoryGuardBytes)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.WasmtimeMaxWasmStackBytes, err = parseBoundedIntEnv("AONOHAKO_WASMTIME_MAX_WASM_STACK_BYTES", os.Getenv("AONOHAKO_WASMTIME_MAX_WASM_STACK_BYTES"), runtimeTuning.WasmtimeMaxWasmStackBytes, minWasmtimeMaxWasmStackBytes, maxWasmtimeMaxWasmStackBytes)
	if err != nil {
		return Config{}, err
	}
	execution := ExecutionConfig{
		Platform: runtimePlatform,
		Remote: RemoteExecutorConfig{
			URL:            strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_URL")),
			Auth:           parseRemoteAuth(os.Getenv("AONOHAKO_REMOTE_RUNNER_AUTH")),
			BearerToken:    strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_TOKEN")),
			Audience:       strings.TrimSpace(os.Getenv("AONOHAKO_REMOTE_RUNNER_AUDIENCE")),
			SSEIdleTimeout: time.Duration(remoteSSEIdleTimeoutSec) * time.Second,
		},
		RuntimeTuning: runtimeTuning,
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
			if runtimePlatform.DeploymentTarget != platform.DeploymentTargetDev {
				return Config{}, fmt.Errorf("AONOHAKO_REMOTE_RUNNER_AUTH=none is only allowed with AONOHAKO_DEPLOYMENT_TARGET=dev; use bearer or cloudrun-idtoken")
			}
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
	if inboundAuth.Mode == InboundAuthNone && runtimePlatform.DeploymentTarget != platform.DeploymentTargetDev {
		return Config{}, fmt.Errorf("AONOHAKO_INBOUND_AUTH=none is only allowed with AONOHAKO_DEPLOYMENT_TARGET=dev; use bearer or platform")
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
		BodyReadTimeout:               time.Duration(bodyReadTimeoutSec) * time.Second,
		AllowRequestNetwork:           allowRequestNetwork,
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

func defaultAllowRequestNetwork(opts platform.RuntimeOptions) bool {
	return opts.DeploymentTarget == platform.DeploymentTargetDev
}

func DefaultRuntimeTuningConfig() RuntimeTuningConfig {
	return RuntimeTuningConfig{
		JVMHeapPercent:            defaultJVMHeapPercent,
		GoMemoryReserveMB:         defaultGoMemoryReserveMB,
		GoGOGC:                    defaultGoGOGC,
		NodeOldSpacePercent:       defaultNodeOldSpacePercent,
		NodeMaxSemiSpaceMB:        defaultNodeMaxSemiSpaceMB,
		NodeStackSizeKB:           defaultNodeStackSizeKB,
		WasmtimeMemoryGuardBytes:  defaultWasmtimeMemoryGuardBytes,
		WasmtimeMaxWasmStackBytes: defaultWasmtimeMaxWasmStackBytes,
	}
}

func (c RuntimeTuningConfig) WithSafeDefaults() RuntimeTuningConfig {
	defaults := DefaultRuntimeTuningConfig()
	if c.JVMHeapPercent == 0 {
		c.JVMHeapPercent = defaults.JVMHeapPercent
	}
	if c.JVMHeapPercent < minJVMHeapPercent {
		c.JVMHeapPercent = minJVMHeapPercent
	}
	if c.JVMHeapPercent > maxJVMHeapPercent {
		c.JVMHeapPercent = maxJVMHeapPercent
	}
	if c.GoMemoryReserveMB < minGoMemoryReserveMB {
		c.GoMemoryReserveMB = minGoMemoryReserveMB
	}
	if c.GoMemoryReserveMB > maxGoMemoryReserveMB {
		c.GoMemoryReserveMB = maxGoMemoryReserveMB
	}
	if c.GoGOGC == 0 {
		c.GoGOGC = defaults.GoGOGC
	}
	if c.GoGOGC < minGoGOGC {
		c.GoGOGC = minGoGOGC
	}
	if c.GoGOGC > maxGoGOGC {
		c.GoGOGC = maxGoGOGC
	}
	if c.NodeOldSpacePercent == 0 {
		c.NodeOldSpacePercent = defaults.NodeOldSpacePercent
	}
	if c.NodeOldSpacePercent < minNodeOldSpacePercent {
		c.NodeOldSpacePercent = minNodeOldSpacePercent
	}
	if c.NodeOldSpacePercent > maxNodeOldSpacePercent {
		c.NodeOldSpacePercent = maxNodeOldSpacePercent
	}
	if c.NodeMaxSemiSpaceMB == 0 {
		c.NodeMaxSemiSpaceMB = defaults.NodeMaxSemiSpaceMB
	}
	if c.NodeMaxSemiSpaceMB < minNodeMaxSemiSpaceMB {
		c.NodeMaxSemiSpaceMB = minNodeMaxSemiSpaceMB
	}
	if c.NodeMaxSemiSpaceMB > maxNodeMaxSemiSpaceMB {
		c.NodeMaxSemiSpaceMB = maxNodeMaxSemiSpaceMB
	}
	if c.NodeStackSizeKB == 0 {
		c.NodeStackSizeKB = defaults.NodeStackSizeKB
	}
	if c.NodeStackSizeKB < minNodeStackSizeKB {
		c.NodeStackSizeKB = minNodeStackSizeKB
	}
	if c.NodeStackSizeKB > maxNodeStackSizeKB {
		c.NodeStackSizeKB = maxNodeStackSizeKB
	}
	if c.WasmtimeMemoryGuardBytes == 0 {
		c.WasmtimeMemoryGuardBytes = defaults.WasmtimeMemoryGuardBytes
	}
	if c.WasmtimeMemoryGuardBytes < minWasmtimeMemoryGuardBytes {
		c.WasmtimeMemoryGuardBytes = minWasmtimeMemoryGuardBytes
	}
	if c.WasmtimeMemoryGuardBytes > maxWasmtimeMemoryGuardBytes {
		c.WasmtimeMemoryGuardBytes = maxWasmtimeMemoryGuardBytes
	}
	if c.WasmtimeMaxWasmStackBytes == 0 {
		c.WasmtimeMaxWasmStackBytes = defaults.WasmtimeMaxWasmStackBytes
	}
	if c.WasmtimeMaxWasmStackBytes < minWasmtimeMaxWasmStackBytes {
		c.WasmtimeMaxWasmStackBytes = minWasmtimeMaxWasmStackBytes
	}
	if c.WasmtimeMaxWasmStackBytes > maxWasmtimeMaxWasmStackBytes {
		c.WasmtimeMaxWasmStackBytes = maxWasmtimeMaxWasmStackBytes
	}
	return c
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

func parseBoundedIntEnv(key, raw string, fallback, minValue, maxValue int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < minValue || v > maxValue {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minValue, maxValue)
	}
	return v, nil
}

func parseBoolEnv(key, raw string, fallback bool) (bool, error) {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return fallback, nil
	}
	switch value {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean", key)
	}
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
