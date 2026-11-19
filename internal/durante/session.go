package durante

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"aonohako/internal/cpupolicy"
)

// This profile was qualified only as a C/C++ diagnostic control on glibc 2.41.
// The host version does not attest the embedded libc of static submissions.
const portableGLIBC241 = `glibc.cpu.hwcaps=-AVX,-AVX2,-AVX512F,-AVX512VL,-AVX512BW,-AVX512DQ,-AVX512CD,-AVX512ER,-AVX512PF,-SSSE3,-SSE4_1,-SSE4_2,-BMI1,-BMI2,-LZCNT,-MOVBE,-POPCNT,-RTM,-ERMS,-Prefer_ERMS,-Prefer_FSRM,-AVX_Fast_Unaligned_Load,-Fast_Unaligned_Load,-Fast_Unaligned_Copy,-Fast_Copy_Backward,-Fast_Rep_String,Avoid_Non_Temporal_Memset:glibc.cpu.x86_data_cache_size=32768:glibc.cpu.x86_shared_cache_size=8388608:glibc.cpu.x86_non_temporal_threshold=1073741824:glibc.cpu.x86_memset_non_temporal_threshold=1073741824:glibc.cpu.x86_rep_movsb_threshold=1073741824:glibc.cpu.x86_rep_stosb_threshold=1073741824`

var hostLibc struct {
	sync.Once
	version string
	err     error
}

func hostGLIBC(ctx context.Context) (string, error) {
	hostLibc.Do(func() {
		// Do not cache cancellation from a request: the bounded probe is image state.
		probe, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probe, "/usr/bin/getconf", "GNU_LIBC_VERSION")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
		output, err := cmd.Output()
		if err != nil {
			hostLibc.err = fmt.Errorf("host glibc version is unavailable")
			return
		}
		fields := strings.Fields(string(output))
		if len(fields) != 2 || fields[0] != "glibc" {
			hostLibc.err = fmt.Errorf("unexpected host libc version")
			return
		}
		hostLibc.version = fields[1]
	})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hostLibc.version, hostLibc.err
}

type Session struct {
	dir          string
	config       Config
	family       string
	hostLibc     string
	installation InstallationIdentity
	cpuFeatures  []cpupolicy.Feature
	// runtimeDeterminism is the option set Runtime applied to this execution.
	runtimeDeterminism string
}

func Prepare(ctx context.Context, root string, cfg Config, language string, uid, gid int) (*Session, error) {
	return prepare(ctx, root, cfg, language, uid, gid, hostGLIBC)
}

// PrepareCPUFeaturePolicy uses the native client without opting the caller into
// a work-measurement family. Policy-only runs retain the ordinary dispatch
// environment and do not publish a measurement report.
func PrepareCPUFeaturePolicy(ctx context.Context, root string, cfg Config, uid, gid int) (*Session, error) {
	policyConfig, err := ParseConfig("", cfg.Drrun, cfg.Client, DispatchDefault)
	if err != nil {
		return nil, err
	}
	return prepareSession(ctx, root, policyConfig, "", uid, gid, hostGLIBC)
}

func prepare(ctx context.Context, root string, cfg Config, language string, uid, gid int, libc func(context.Context) (string, error)) (*Session, error) {
	family, err := cfg.Family(language)
	if err != nil {
		return nil, err
	}
	return prepareSession(ctx, root, cfg, family, uid, gid, libc)
}

func prepareSession(ctx context.Context, root string, cfg Config, family string, uid, gid int, libc func(context.Context) (string, error)) (*Session, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, fmt.Errorf("durante requires Linux x86-64")
	}
	for _, path := range []string{cfg.Drrun, cfg.Client} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
			return nil, fmt.Errorf("durante installation is unavailable or writable by other users")
		}
	}
	info, _ := os.Stat(cfg.Drrun)
	if info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("durante launcher is not executable")
	}
	identity, err := snapshotInstallation(cfg)
	if err != nil {
		return nil, fmt.Errorf("durante patched runtime verification failed: %w", err)
	}
	s := &Session{config: cfg, family: family, installation: identity}
	if cfg.DispatchProfile == DispatchGLIBC241 {
		s.hostLibc, err = libc(ctx)
		if err != nil || s.hostLibc != "2.41" {
			return nil, fmt.Errorf("durante profile %s requires host glibc 2.41", cfg.DispatchProfile)
		}
	}
	// The root is root-owned after sandbox hardening. The contestant may write
	// report contents but cannot replace or rename this directory itself.
	s.dir, err = os.MkdirTemp(root, ".durante-")
	if err != nil {
		return nil, fmt.Errorf("create durante directory: %w", err)
	}
	if err := os.Chown(s.dir, uid, gid); err != nil {
		os.Remove(s.dir)
		return nil, fmt.Errorf("set durante directory owner: %w", err)
	}
	return s, nil
}

func (s *Session) SetDisallowedCPUFeatures(features []cpupolicy.Feature) error {
	if err := cpupolicy.Validate(features); err != nil {
		return err
	}
	if s == nil || s.dir == "" {
		return fmt.Errorf("CPU feature enforcement requires a prepared Durante session")
	}
	s.cpuFeatures = slices.Clone(features)
	slices.Sort(s.cpuFeatures)
	return nil
}

func (s *Session) Wrap(command []string) []string {
	return s.WrapWithWorkClock(command, "")
}

func (s *Session) WrapWithWorkClock(command []string, clockPath string) []string {
	args := []string{s.config.Drrun, "-quiet", "-follow_children", "-disable_traces", "-vm_size", "32M", "-vmheap_size", "32M", "-c", s.config.Client, "-outdir", s.dir, "-rep_bytes"}
	if clockPath != "" {
		args = append(args, "-live_work_v1", clockPath)
	}
	for _, feature := range s.cpuFeatures {
		args = append(args, "-disallow_cpu_feature", string(feature))
	}
	if s.config.CPUIDProfile != "" {
		args = append(args, "-cpuid_profile", s.config.CPUIDProfile)
	}
	if s.config.LatencyBound {
		args = append(args, "-latency_bound")
	}
	args = append(args, "--")
	return append(args, command...)
}

// Runtime applies the measured-execution argv and environment (see
// Config.MeasuredRuntime) and records the applied option set for the report.
// Call it once, with the normalized run language, before Wrap.
func (s *Session) Runtime(runLang string, command, env []string) ([]string, []string, error) {
	command, env, err := s.config.MeasuredRuntime(runLang, command, env)
	if err != nil {
		return nil, nil, err
	}
	s.runtimeDeterminism = s.config.RuntimeDeterminismFor(runLang)
	return command, env, nil
}

// RuntimeEnvironment applies the same dispatch environment to execution and
// effective runtime-options descriptors without opening a measurement session.
func (c Config) RuntimeEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, value := range env {
		if !strings.HasPrefix(value, "GLIBC_TUNABLES=") {
			out = append(out, value)
		}
	}
	if c.DispatchProfile == DispatchGLIBC241 {
		out = append(out, "GLIBC_TUNABLES="+portableGLIBC241)
	}
	return out
}

func (s *Session) Collect(rootPID int) *Report {
	report := CollectDirectory(s.dir, rootPID)
	current, err := snapshotInstallation(s.config)
	identity := s.installation
	identity.Unchanged = err == nil && current == s.installation
	if !identity.Unchanged {
		report = incomplete("durante installation changed during observation")
	} else if report.Complete && report.CPUIDProfile != s.config.CPUIDProfile {
		// A legacy client, host CPUID or another profile cannot satisfy the
		// configured virtual processor; never publish those totals.
		report = incomplete("durante CPUID profile receipt does not match the configuration")
	} else if report.Complete && (report.LatencyBound != nil) != s.config.LatencyBound {
		report = incomplete("durante latency bound receipt does not match the configuration")
	}
	report.Installation = &identity
	report.Family = s.family
	report.EngineProfile = EngineProfile
	report.DispatchProfile = s.config.DispatchProfile
	if report.DispatchProfile == "" {
		report.DispatchProfile = DispatchDefault
	}
	report.HostLibc = s.hostLibc
	if report.Complete {
		report.RuntimeDeterminism = s.runtimeDeterminism
	}
	return &report
}
