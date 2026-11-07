package config

import (
	"os"
	"strings"
	"testing"

	"aonohako/internal/timing"
)

func TestLoadCPUNormalizationDisabledByDefault(t *testing.T) {
	for _, target := range []string{"dev", "selfhosted", "cloudrun"} {
		for _, value := range []string{"unset", "", "false"} {
			t.Run(target+"/"+value, func(t *testing.T) {
				setCPUNormalizationTestEnv(t, target, "remote", "none")
				setCPUNormalizationTestValue(t, value)
				cfg, err := Load()
				if err != nil {
					t.Fatal(err)
				}
				if cfg.Execution.CPUNormalization.Enabled || cfg.Execution.CPUNormalization.ShortCaseSampling {
					t.Fatalf("normalization and inherited sampling must default off: %+v", cfg.Execution.CPUNormalization)
				}
				if cfg.Execution.CPUNormalization.ReferenceTimeNs != timing.DefaultCPUReferenceTimeNs {
					t.Fatalf("normalization reference = %d, want %d", cfg.Execution.CPUNormalization.ReferenceTimeNs, timing.DefaultCPUReferenceTimeNs)
				}
			})
		}
	}
}

func TestLoadCPUNormalizationRejectsUnsupportedOptIn(t *testing.T) {
	for _, tc := range []struct{ target, transport, backend string }{
		{"dev", "remote", "none"},
		{"selfhosted", "remote", "none"},
		{"cloudrun", "remote", "none"},
		{"dev", "embedded", "helper"},
		{"selfhosted", "embedded", "helper"},
	} {
		t.Run(tc.target+"/"+tc.transport, func(t *testing.T) {
			setCPUNormalizationTestEnv(t, tc.target, tc.transport, tc.backend)
			t.Setenv("AONOHAKO_CPU_NORMALIZATION", "true")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "AONOHAKO_CPU_NORMALIZATION=true requires cloudrun embedded helper execution") {
				t.Fatalf("unsupported normalization opt-in error = %v", err)
			}
		})
	}
}

func TestLoadCPUNormalizationAcceptsCloudRunHelperOptInBeforeHostValidation(t *testing.T) {
	for _, value := range []string{"unset", "", "false", "true"} {
		t.Run(value, func(t *testing.T) {
			setCPUNormalizationTestEnv(t, "cloudrun", "embedded", "helper")
			setCPUNormalizationTestValue(t, value)
			t.Setenv("AONOHAKO_TRUSTED_RUNNER_INGRESS", "false")
			// Reach a deterministic later validation without requiring root or a tmpfs mount.
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "requires AONOHAKO_TRUSTED_RUNNER_INGRESS=true") {
				t.Fatalf("expected ingress validation after normalization validation, got %v", err)
			}
		})
	}
}

func setCPUNormalizationTestEnv(t *testing.T, target, transport, backend string) {
	t.Helper()
	for _, key := range []string{
		"K_SERVICE", "CLOUD_RUN_JOB", "CLOUD_RUN_WORKER_POOL",
		"AONOHAKO_CPU_NORMALIZATION_REFERENCE_MS", "AONOHAKO_CPU_SHORT_CASE_SAMPLING",
		"AONOHAKO_CGROUP_PARENT", "AONOHAKO_WORK_ROOT_MAX_BYTES", "AONOHAKO_WORK_ROOT_MAX_FILES",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", target)
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", transport)
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", backend)
	t.Setenv("AONOHAKO_MAX_ACTIVE_RUNS", "1")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "bearer")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_TOKEN", "runner-test-token")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "bearer")
	t.Setenv("AONOHAKO_API_BEARER_TOKEN", "api-test-token")
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	t.Setenv("AONOHAKO_REQUIRE_WORK_ROOT_TMPFS", "false")
	t.Setenv("AONOHAKO_COMMUNICATION_ENABLED", "false")
}

func setCPUNormalizationTestValue(t *testing.T, value string) {
	t.Helper()
	t.Setenv("AONOHAKO_CPU_NORMALIZATION", value)
	if value == "unset" {
		if err := os.Unsetenv("AONOHAKO_CPU_NORMALIZATION"); err != nil {
			t.Fatal(err)
		}
	}
}
