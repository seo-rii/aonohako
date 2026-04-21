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

func IsCloudRun() bool {
	return CurrentExecutionMode() == ExecutionModeCloudRun
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
	switch CurrentExecutionMode() {
	case ExecutionModeCloudRun, ExecutionModeLocalRoot:
		return true
	default:
		return false
	}
}
