package platform

import "os"

func IsCloudRun() bool {
	for _, key := range []string{"K_SERVICE", "CLOUD_RUN_JOB", "CLOUD_RUN_WORKER_POOL"} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}
