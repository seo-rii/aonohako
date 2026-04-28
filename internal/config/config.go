package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/isolation/cgroup"
	"aonohako/internal/platform"
	"aonohako/internal/remoteio"
	"aonohako/internal/runtimepolicy"
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
	Mode                        InboundAuthMode
	BearerToken                 string
	PlatformPrincipalHMACSecret string
}

type ExecutionConfig struct {
	Platform               platform.RuntimeOptions
	Remote                 RemoteExecutorConfig
	RuntimeTuning          RuntimeTuningConfig
	RuntimeTuningProfiles  map[string]RuntimeTuningConfig
	ProblemRuntimeProfiles map[string]string
	Cgroup                 CgroupConfig
}

type CgroupConfig struct {
	ParentDir string
}

type RuntimeTuningConfig struct {
	JVMHeapPercent             int
	GoMemoryReserveMB          int
	GoGOGC                     int
	ErlangSchedulers           int
	ErlangAsyncThreads         int
	DotnetGCHeapPercent        int
	KotlinNativeCompilerHeapMB int
	NodeOldSpacePercent        int
	NodeMaxSemiSpaceMB         int
	NodeStackSizeKB            int
	WasmtimeMemoryGuardBytes   int
	WasmtimeMaxWasmStackBytes  int
}

const (
	defaultMaxPendingQueue  = 16
	defaultMaxActiveStreams = 64

	defaultJVMHeapPercent             = 50
	minJVMHeapPercent                 = 25
	maxJVMHeapPercent                 = 75
	defaultGoMemoryReserveMB          = 32
	minGoMemoryReserveMB              = 0
	maxGoMemoryReserveMB              = 256
	defaultGoGOGC                     = 50
	minGoGOGC                         = 10
	maxGoGOGC                         = 200
	defaultErlangSchedulers           = 1
	minErlangSchedulers               = 1
	maxErlangSchedulers               = 4
	defaultErlangAsyncThreads         = 1
	minErlangAsyncThreads             = 0
	maxErlangAsyncThreads             = 4
	defaultDotnetGCHeapPercent        = 60
	minDotnetGCHeapPercent            = 25
	maxDotnetGCHeapPercent            = 80
	defaultKotlinNativeCompilerHeapMB = 1024
	minKotlinNativeCompilerHeapMB     = 256
	maxKotlinNativeCompilerHeapMB     = 1536
	defaultNodeOldSpacePercent        = 60
	minNodeOldSpacePercent            = 30
	maxNodeOldSpacePercent            = 75
	defaultNodeMaxSemiSpaceMB         = 8
	minNodeMaxSemiSpaceMB             = 1
	maxNodeMaxSemiSpaceMB             = 16
	defaultNodeStackSizeKB            = 2048
	minNodeStackSizeKB                = 512
	maxNodeStackSizeKB                = 8192
	defaultWasmtimeMemoryGuardBytes   = 64 << 10
	minWasmtimeMemoryGuardBytes       = 64 << 10
	maxWasmtimeMemoryGuardBytes       = 16 << 20
	defaultWasmtimeMaxWasmStackBytes  = 1 << 20
	minWasmtimeMaxWasmStackBytes      = 256 << 10
	maxWasmtimeMaxWasmStackBytes      = 8 << 20
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
	AllowRequestRuntimeProfile    bool
	RequireWorkRootTmpfs          bool
	TrustedRunnerIngress          bool
	TrustedPlatformHeaders        bool
	TrustedPlatformHeaderCIDRs    []string
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
	allowRequestRuntimeProfile, err := parseBoolEnv("AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE", os.Getenv("AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE"), defaultAllowRequestRuntimeProfile(runtimePlatform))
	if err != nil {
		return Config{}, err
	}
	requireWorkRootTmpfs, err := parseBoolEnv("AONOHAKO_REQUIRE_WORK_ROOT_TMPFS", os.Getenv("AONOHAKO_REQUIRE_WORK_ROOT_TMPFS"), false)
	if err != nil {
		return Config{}, err
	}
	trustedRunnerIngress, err := parseBoolEnv("AONOHAKO_TRUSTED_RUNNER_INGRESS", os.Getenv("AONOHAKO_TRUSTED_RUNNER_INGRESS"), defaultTrustedRunnerIngress(runtimePlatform))
	if err != nil {
		return Config{}, err
	}
	trustedPlatformHeaders, err := parseBoolEnv("AONOHAKO_TRUSTED_PLATFORM_HEADERS", os.Getenv("AONOHAKO_TRUSTED_PLATFORM_HEADERS"), defaultTrustedPlatformHeaders(runtimePlatform))
	if err != nil {
		return Config{}, err
	}
	trustedPlatformHeaderCIDRs := []string(nil)
	if rawCIDRs := strings.TrimSpace(os.Getenv("AONOHAKO_PLATFORM_TRUSTED_PROXY_CIDRS")); rawCIDRs != "" {
		for _, rawCIDR := range strings.Split(rawCIDRs, ",") {
			cidr := strings.TrimSpace(rawCIDR)
			if cidr == "" {
				continue
			}
			_, parsed, err := net.ParseCIDR(cidr)
			if err != nil {
				return Config{}, fmt.Errorf("AONOHAKO_PLATFORM_TRUSTED_PROXY_CIDRS contains invalid CIDR %q: %w", cidr, err)
			}
			trustedPlatformHeaderCIDRs = append(trustedPlatformHeaderCIDRs, parsed.String())
		}
		if len(trustedPlatformHeaderCIDRs) == 0 {
			return Config{}, fmt.Errorf("AONOHAKO_PLATFORM_TRUSTED_PROXY_CIDRS must contain at least one CIDR when set")
		}
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
	runtimeTuning.ErlangSchedulers, err = parseBoundedIntEnv("AONOHAKO_ERLANG_SCHEDULERS", os.Getenv("AONOHAKO_ERLANG_SCHEDULERS"), runtimeTuning.ErlangSchedulers, minErlangSchedulers, maxErlangSchedulers)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.ErlangAsyncThreads, err = parseBoundedIntEnv("AONOHAKO_ERLANG_ASYNC_THREADS", os.Getenv("AONOHAKO_ERLANG_ASYNC_THREADS"), runtimeTuning.ErlangAsyncThreads, minErlangAsyncThreads, maxErlangAsyncThreads)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.DotnetGCHeapPercent, err = parseBoundedIntEnv("AONOHAKO_DOTNET_GC_HEAP_PERCENT", os.Getenv("AONOHAKO_DOTNET_GC_HEAP_PERCENT"), runtimeTuning.DotnetGCHeapPercent, minDotnetGCHeapPercent, maxDotnetGCHeapPercent)
	if err != nil {
		return Config{}, err
	}
	runtimeTuning.KotlinNativeCompilerHeapMB, err = parseBoundedIntEnv("AONOHAKO_KOTLIN_NATIVE_COMPILER_HEAP_MB", os.Getenv("AONOHAKO_KOTLIN_NATIVE_COMPILER_HEAP_MB"), runtimeTuning.KotlinNativeCompilerHeapMB, minKotlinNativeCompilerHeapMB, maxKotlinNativeCompilerHeapMB)
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
	runtimeTuningProfiles := map[string]RuntimeTuningConfig(nil)
	if rawProfiles := strings.TrimSpace(os.Getenv("AONOHAKO_RUNTIME_TUNING_PROFILES")); rawProfiles != "" {
		parsedProfiles := map[string]map[string]int{}
		if err := json.Unmarshal([]byte(rawProfiles), &parsedProfiles); err != nil {
			return Config{}, fmt.Errorf("AONOHAKO_RUNTIME_TUNING_PROFILES must be a JSON object of numeric profile overrides: %w", err)
		}
		if len(parsedProfiles) == 0 {
			return Config{}, fmt.Errorf("AONOHAKO_RUNTIME_TUNING_PROFILES must define at least one profile when set")
		}
		runtimeTuningProfiles = make(map[string]RuntimeTuningConfig, len(parsedProfiles))
		for profileName, values := range parsedProfiles {
			if profileName == "" {
				return Config{}, fmt.Errorf("AONOHAKO_RUNTIME_TUNING_PROFILES contains an empty profile name")
			}
			if err := runtimepolicy.ValidateProfileName(profileName); err != nil {
				return Config{}, fmt.Errorf("AONOHAKO_RUNTIME_TUNING_PROFILES profile %q is invalid: %w", profileName, err)
			}
			profileTuning := runtimeTuning
			for key, value := range values {
				envName := "AONOHAKO_RUNTIME_TUNING_PROFILES." + profileName + "." + key
				rawValue := strconv.Itoa(value)
				switch key {
				case "jvm_heap_percent":
					profileTuning.JVMHeapPercent, err = parseBoundedIntEnv(envName, rawValue, profileTuning.JVMHeapPercent, minJVMHeapPercent, maxJVMHeapPercent)
				case "go_memory_reserve_mb":
					profileTuning.GoMemoryReserveMB, err = parseBoundedIntEnv(envName, rawValue, profileTuning.GoMemoryReserveMB, minGoMemoryReserveMB, maxGoMemoryReserveMB)
				case "go_gogc":
					profileTuning.GoGOGC, err = parseBoundedIntEnv(envName, rawValue, profileTuning.GoGOGC, minGoGOGC, maxGoGOGC)
				case "erlang_schedulers":
					profileTuning.ErlangSchedulers, err = parseBoundedIntEnv(envName, rawValue, profileTuning.ErlangSchedulers, minErlangSchedulers, maxErlangSchedulers)
				case "erlang_async_threads":
					profileTuning.ErlangAsyncThreads, err = parseBoundedIntEnv(envName, rawValue, profileTuning.ErlangAsyncThreads, minErlangAsyncThreads, maxErlangAsyncThreads)
				case "dotnet_gc_heap_percent":
					profileTuning.DotnetGCHeapPercent, err = parseBoundedIntEnv(envName, rawValue, profileTuning.DotnetGCHeapPercent, minDotnetGCHeapPercent, maxDotnetGCHeapPercent)
				case "kotlin_native_compiler_heap_mb":
					profileTuning.KotlinNativeCompilerHeapMB, err = parseBoundedIntEnv(envName, rawValue, profileTuning.KotlinNativeCompilerHeapMB, minKotlinNativeCompilerHeapMB, maxKotlinNativeCompilerHeapMB)
				case "node_old_space_percent":
					profileTuning.NodeOldSpacePercent, err = parseBoundedIntEnv(envName, rawValue, profileTuning.NodeOldSpacePercent, minNodeOldSpacePercent, maxNodeOldSpacePercent)
				case "node_max_semi_space_mb":
					profileTuning.NodeMaxSemiSpaceMB, err = parseBoundedIntEnv(envName, rawValue, profileTuning.NodeMaxSemiSpaceMB, minNodeMaxSemiSpaceMB, maxNodeMaxSemiSpaceMB)
				case "node_stack_size_kb":
					profileTuning.NodeStackSizeKB, err = parseBoundedIntEnv(envName, rawValue, profileTuning.NodeStackSizeKB, minNodeStackSizeKB, maxNodeStackSizeKB)
				case "wasmtime_memory_guard_bytes":
					profileTuning.WasmtimeMemoryGuardBytes, err = parseBoundedIntEnv(envName, rawValue, profileTuning.WasmtimeMemoryGuardBytes, minWasmtimeMemoryGuardBytes, maxWasmtimeMemoryGuardBytes)
				case "wasmtime_max_wasm_stack_bytes":
					profileTuning.WasmtimeMaxWasmStackBytes, err = parseBoundedIntEnv(envName, rawValue, profileTuning.WasmtimeMaxWasmStackBytes, minWasmtimeMaxWasmStackBytes, maxWasmtimeMaxWasmStackBytes)
				default:
					return Config{}, fmt.Errorf("AONOHAKO_RUNTIME_TUNING_PROFILES profile %q contains unsupported key %q", profileName, key)
				}
				if err != nil {
					return Config{}, err
				}
			}
			runtimeTuningProfiles[profileName] = profileTuning.WithSafeDefaults()
		}
	}
	problemRuntimeProfiles := map[string]string(nil)
	if rawProblemProfiles := strings.TrimSpace(os.Getenv("AONOHAKO_PROBLEM_RUNTIME_PROFILES")); rawProblemProfiles != "" {
		parsedProblemProfiles := map[string]string{}
		if err := json.Unmarshal([]byte(rawProblemProfiles), &parsedProblemProfiles); err != nil {
			return Config{}, fmt.Errorf("AONOHAKO_PROBLEM_RUNTIME_PROFILES must be a JSON object mapping problem IDs to runtime profile names: %w", err)
		}
		if len(parsedProblemProfiles) == 0 {
			return Config{}, fmt.Errorf("AONOHAKO_PROBLEM_RUNTIME_PROFILES must define at least one problem mapping when set")
		}
		problemRuntimeProfiles = make(map[string]string, len(parsedProblemProfiles))
		for problemID, profileName := range parsedProblemProfiles {
			if problemID == "" {
				return Config{}, fmt.Errorf("AONOHAKO_PROBLEM_RUNTIME_PROFILES contains an empty problem_id")
			}
			if err := runtimepolicy.ValidateProblemID(problemID); err != nil {
				return Config{}, fmt.Errorf("AONOHAKO_PROBLEM_RUNTIME_PROFILES problem_id %q is invalid: %w", problemID, err)
			}
			if profileName == "" {
				return Config{}, fmt.Errorf("AONOHAKO_PROBLEM_RUNTIME_PROFILES problem_id %q maps to an empty runtime_profile", problemID)
			}
			if err := runtimepolicy.ValidateProfileName(profileName); err != nil {
				return Config{}, fmt.Errorf("AONOHAKO_PROBLEM_RUNTIME_PROFILES problem_id %q maps to invalid runtime_profile %q: %w", problemID, profileName, err)
			}
			if _, ok := runtimeTuningProfiles[profileName]; !ok {
				return Config{}, fmt.Errorf("AONOHAKO_PROBLEM_RUNTIME_PROFILES problem_id %q maps to unknown runtime_profile %q", problemID, profileName)
			}
			problemRuntimeProfiles[problemID] = profileName
		}
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
		RuntimeTuning:          runtimeTuning,
		RuntimeTuningProfiles:  runtimeTuningProfiles,
		ProblemRuntimeProfiles: problemRuntimeProfiles,
		Cgroup: CgroupConfig{
			ParentDir: strings.TrimSpace(os.Getenv("AONOHAKO_CGROUP_PARENT")),
		},
	}
	inboundAuth := InboundAuthConfig{
		Mode:                        parseInboundAuth(os.Getenv("AONOHAKO_INBOUND_AUTH"), runtimePlatform),
		BearerToken:                 strings.TrimSpace(os.Getenv("AONOHAKO_API_BEARER_TOKEN")),
		PlatformPrincipalHMACSecret: strings.TrimSpace(os.Getenv("AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET")),
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
	if inboundAuth.Mode == InboundAuthPlatform && runtimePlatform.DeploymentTarget != platform.DeploymentTargetDev && inboundAuth.PlatformPrincipalHMACSecret == "" {
		return Config{}, fmt.Errorf("AONOHAKO_INBOUND_AUTH=platform outside dev requires AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET")
	}

	if contract.RequiresRootParent && runtimePlatform.DeploymentTarget != platform.DeploymentTargetDev && !trustedRunnerIngress {
		return Config{}, fmt.Errorf("embedded helper execution outside dev requires AONOHAKO_TRUSTED_RUNNER_INGRESS=true")
	}

	if execution.Cgroup.ParentDir != "" {
		if runtimePlatform.DeploymentTarget != platform.DeploymentTargetSelfHosted || runtimePlatform.ExecutionTransport != platform.ExecutionTransportEmbedded || runtimePlatform.SandboxBackend != platform.SandboxBackendHelper {
			return Config{}, fmt.Errorf("AONOHAKO_CGROUP_PARENT is supported only with selfhosted embedded helper execution")
		}
		if err := cgroup.ValidateParent(execution.Cgroup.ParentDir, []string{"cpu", "memory", "pids"}); err != nil {
			return Config{}, fmt.Errorf("AONOHAKO_CGROUP_PARENT validation failed: %w", err)
		}
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
		if requireWorkRootTmpfs {
			fsType, err := workRootFilesystemAt(workRoot, "/proc/self/mountinfo")
			if err != nil {
				return Config{}, fmt.Errorf("AONOHAKO_REQUIRE_WORK_ROOT_TMPFS validation failed: %w", err)
			}
			if fsType != "tmpfs" {
				return Config{}, fmt.Errorf("AONOHAKO_WORK_ROOT must be on tmpfs when AONOHAKO_REQUIRE_WORK_ROOT_TMPFS=true; got %s", fsType)
			}
		}
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
		AllowRequestRuntimeProfile:    allowRequestRuntimeProfile,
		RequireWorkRootTmpfs:          requireWorkRootTmpfs,
		TrustedRunnerIngress:          trustedRunnerIngress,
		TrustedPlatformHeaders:        trustedPlatformHeaders,
		TrustedPlatformHeaderCIDRs:    trustedPlatformHeaderCIDRs,
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

func workRootFilesystemAt(workRoot, mountInfoPath string) (string, error) {
	workRoot, err := filepath.Abs(workRoot)
	if err != nil {
		return "", fmt.Errorf("resolve work root: %w", err)
	}
	workRoot, err = filepath.EvalSymlinks(workRoot)
	if err != nil {
		return "", fmt.Errorf("resolve work root symlinks: %w", err)
	}
	mountInfo, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return "", fmt.Errorf("read mountinfo: %w", err)
	}
	bestMountLen := -1
	bestFSType := ""
	scanner := bufio.NewScanner(strings.NewReader(string(mountInfo)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || separator+1 >= len(fields) || len(fields) <= 4 {
			continue
		}
		mountPoint := unescapeMountInfoField(fields[4])
		if mountPoint == "" {
			continue
		}
		if workRoot != mountPoint && !strings.HasPrefix(workRoot, strings.TrimRight(mountPoint, "/")+"/") {
			continue
		}
		if len(mountPoint) > bestMountLen {
			bestMountLen = len(mountPoint)
			bestFSType = fields[separator+1]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan mountinfo: %w", err)
	}
	if bestFSType == "" {
		return "", fmt.Errorf("no mountinfo entry covers %s", workRoot)
	}
	return bestFSType, nil
}

func unescapeMountInfoField(path string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(path)
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

func defaultAllowRequestRuntimeProfile(opts platform.RuntimeOptions) bool {
	return opts.DeploymentTarget == platform.DeploymentTargetDev
}

func defaultTrustedRunnerIngress(opts platform.RuntimeOptions) bool {
	return opts.DeploymentTarget == platform.DeploymentTargetDev || opts.ExecutionTransport == platform.ExecutionTransportRemote
}

func defaultTrustedPlatformHeaders(opts platform.RuntimeOptions) bool {
	return opts.DeploymentTarget == platform.DeploymentTargetDev
}

func DefaultRuntimeTuningConfig() RuntimeTuningConfig {
	return RuntimeTuningConfig{
		JVMHeapPercent:             defaultJVMHeapPercent,
		GoMemoryReserveMB:          defaultGoMemoryReserveMB,
		GoGOGC:                     defaultGoGOGC,
		ErlangSchedulers:           defaultErlangSchedulers,
		ErlangAsyncThreads:         defaultErlangAsyncThreads,
		DotnetGCHeapPercent:        defaultDotnetGCHeapPercent,
		KotlinNativeCompilerHeapMB: defaultKotlinNativeCompilerHeapMB,
		NodeOldSpacePercent:        defaultNodeOldSpacePercent,
		NodeMaxSemiSpaceMB:         defaultNodeMaxSemiSpaceMB,
		NodeStackSizeKB:            defaultNodeStackSizeKB,
		WasmtimeMemoryGuardBytes:   defaultWasmtimeMemoryGuardBytes,
		WasmtimeMaxWasmStackBytes:  defaultWasmtimeMaxWasmStackBytes,
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
	if c.ErlangSchedulers == 0 {
		c.ErlangSchedulers = defaults.ErlangSchedulers
	}
	if c.ErlangSchedulers < minErlangSchedulers {
		c.ErlangSchedulers = minErlangSchedulers
	}
	if c.ErlangSchedulers > maxErlangSchedulers {
		c.ErlangSchedulers = maxErlangSchedulers
	}
	if c.ErlangAsyncThreads < minErlangAsyncThreads {
		c.ErlangAsyncThreads = minErlangAsyncThreads
	}
	if c.ErlangAsyncThreads > maxErlangAsyncThreads {
		c.ErlangAsyncThreads = maxErlangAsyncThreads
	}
	if c.DotnetGCHeapPercent == 0 {
		c.DotnetGCHeapPercent = defaults.DotnetGCHeapPercent
	}
	if c.DotnetGCHeapPercent < minDotnetGCHeapPercent {
		c.DotnetGCHeapPercent = minDotnetGCHeapPercent
	}
	if c.DotnetGCHeapPercent > maxDotnetGCHeapPercent {
		c.DotnetGCHeapPercent = maxDotnetGCHeapPercent
	}
	if c.KotlinNativeCompilerHeapMB == 0 {
		c.KotlinNativeCompilerHeapMB = defaults.KotlinNativeCompilerHeapMB
	}
	if c.KotlinNativeCompilerHeapMB < minKotlinNativeCompilerHeapMB {
		c.KotlinNativeCompilerHeapMB = minKotlinNativeCompilerHeapMB
	}
	if c.KotlinNativeCompilerHeapMB > maxKotlinNativeCompilerHeapMB {
		c.KotlinNativeCompilerHeapMB = maxKotlinNativeCompilerHeapMB
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
