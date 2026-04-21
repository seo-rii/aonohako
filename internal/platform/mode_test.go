package platform

import "testing"

func TestCurrentExecutionModeDefaultsToLocalDev(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "")
	if got := CurrentExecutionMode(); got != ExecutionModeLocalDev {
		t.Fatalf("CurrentExecutionMode() = %q, want %q", got, ExecutionModeLocalDev)
	}
}

func TestCurrentExecutionModeParsesExplicitModes(t *testing.T) {
	tests := []struct {
		raw  string
		want ExecutionMode
	}{
		{raw: "cloudrun", want: ExecutionModeCloudRun},
		{raw: "local-root", want: ExecutionModeLocalRoot},
		{raw: "local-dev", want: ExecutionModeLocalDev},
		{raw: " CLOUDRUN ", want: ExecutionModeCloudRun},
		{raw: "unknown", want: ExecutionModeLocalDev},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("AONOHAKO_EXECUTION_MODE", tc.raw)
			if got := CurrentExecutionMode(); got != tc.want {
				t.Fatalf("CurrentExecutionMode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCloudRunMarkersDoNotChangeExecutionMode(t *testing.T) {
	t.Setenv("AONOHAKO_EXECUTION_MODE", "")
	t.Setenv("K_SERVICE", "aonohako")
	t.Setenv("CLOUD_RUN_JOB", "job")
	t.Setenv("CLOUD_RUN_WORKER_POOL", "pool")

	if !CloudRunMarkersPresent() {
		t.Fatalf("CloudRunMarkersPresent() = false, want true")
	}
	if got := CurrentExecutionMode(); got != ExecutionModeLocalDev {
		t.Fatalf("CurrentExecutionMode() = %q, want %q even when markers are present", got, ExecutionModeLocalDev)
	}
	if IsCloudRun() {
		t.Fatalf("IsCloudRun() = true, want false without explicit execution mode")
	}
}

func TestUsesDedicatedWorkRootMatchesExecutionMode(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{mode: "cloudrun", want: true},
		{mode: "local-root", want: true},
		{mode: "local-dev", want: false},
		{mode: "", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv("AONOHAKO_EXECUTION_MODE", tc.mode)
			if got := UsesDedicatedWorkRoot(); got != tc.want {
				t.Fatalf("UsesDedicatedWorkRoot(%q) = %v, want %v", tc.mode, got, tc.want)
			}
		})
	}
}
