package config

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/timing"
)

func TestLoadCloudRunHelperCPUNormalizationRequiresExplicitOptIn(t *testing.T) {
	if os.Geteuid() != 0 || runtime.GOARCH != "amd64" {
		t.Skip("Cloud Run helper configuration requires linux/amd64 and root")
	}
	workRoot := t.TempDir()
	if err := syscall.Mount("tmpfs", workRoot, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=16m,nr_inodes=1024,mode=0700"); err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOSYS) {
			t.Skipf("dedicated tmpfs mount unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Unmount(workRoot, 0); err != nil {
			t.Errorf("unmount test work root: %v", err)
		}
	})
	for _, tc := range []struct {
		name, normalization, sampling, reference string
		wantEnabled, wantSampling                bool
		wantReference                            uint64
	}{
		{name: "unset", normalization: "unset"},
		{name: "empty"},
		{name: "explicit disable", normalization: "false"},
		{name: "explicit enable", normalization: "true", wantEnabled: true, wantSampling: true},
		{name: "sampling override off", normalization: "true", sampling: "false", wantEnabled: true},
		{name: "sampling override on", normalization: "false", sampling: "true", wantSampling: true},
		{name: "custom reference", normalization: "true", reference: "75", wantEnabled: true, wantSampling: true, wantReference: uint64(75 * time.Millisecond)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setCPUNormalizationTestEnv(t, "cloudrun", "embedded", "helper")
			setCPUNormalizationTestValue(t, tc.normalization)
			t.Setenv("AONOHAKO_CPU_SHORT_CASE_SAMPLING", tc.sampling)
			t.Setenv("AONOHAKO_CPU_NORMALIZATION_REFERENCE_MS", tc.reference)
			t.Setenv("AONOHAKO_TRUSTED_RUNNER_INGRESS", "true")
			t.Setenv("AONOHAKO_WORK_ROOT", workRoot)
			t.Setenv("AONOHAKO_REQUIRE_WORK_ROOT_TMPFS", "true")
			t.Setenv("AONOHAKO_WORK_ROOT_MAX_BYTES", "16777216")
			t.Setenv("AONOHAKO_WORK_ROOT_MAX_FILES", "1024")
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			wantReference := tc.wantReference
			if wantReference == 0 {
				wantReference = timing.DefaultCPUReferenceTimeNs
			}
			want := CPUNormalizationConfig{Enabled: tc.wantEnabled, ReferenceTimeNs: wantReference, ShortCaseSampling: tc.wantSampling}
			if cfg.Execution.CPUNormalization != want {
				t.Fatalf("CPU normalization = %+v, want %+v", cfg.Execution.CPUNormalization, want)
			}
		})
	}
}
