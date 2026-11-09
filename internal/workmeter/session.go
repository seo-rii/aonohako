package workmeter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// EngineProfile identifies fixed engine reservations; requests cannot override them.
const EngineProfile = "dynamorio-11.3-vm32m-v1"

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
	dir      string
	config   Config
	family   string
	hostLibc string
}

func Prepare(ctx context.Context, root string, cfg Config, language string, uid, gid int) (*Session, error) {
	return prepare(ctx, root, cfg, language, uid, gid, hostGLIBC)
}

func prepare(ctx context.Context, root string, cfg Config, language string, uid, gid int, libc func(context.Context) (string, error)) (*Session, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, fmt.Errorf("workmeter requires Linux x86-64")
	}
	family, err := cfg.Family(language)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{cfg.Drrun, cfg.Client} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
			return nil, fmt.Errorf("workmeter installation is unavailable or writable by other users")
		}
	}
	info, _ := os.Stat(cfg.Drrun)
	if info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("workmeter launcher is not executable")
	}
	s := &Session{config: cfg, family: family}
	if cfg.DispatchProfile == DispatchGLIBC241 {
		s.hostLibc, err = libc(ctx)
		if err != nil || s.hostLibc != "2.41" {
			return nil, fmt.Errorf("workmeter profile %s requires host glibc 2.41", cfg.DispatchProfile)
		}
	}
	// The root is root-owned after sandbox hardening. The contestant may write
	// report contents but cannot replace or rename this directory itself.
	s.dir, err = os.MkdirTemp(root, ".workmeter-")
	if err != nil {
		return nil, fmt.Errorf("create workmeter directory: %w", err)
	}
	if err := os.Chown(s.dir, uid, gid); err != nil {
		os.Remove(s.dir)
		return nil, fmt.Errorf("set workmeter directory owner: %w", err)
	}
	return s, nil
}

func (s *Session) Wrap(command []string) []string {
	args := []string{s.config.Drrun, "-quiet", "-follow_children", "-vm_size", "32M", "-vmheap_size", "32M", "-c", s.config.Client, "-outdir", s.dir, "-rep_bytes", "--"}
	return append(args, command...)
}

func (s *Session) Environment(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, value := range env {
		if !strings.HasPrefix(value, "GLIBC_TUNABLES=") {
			out = append(out, value)
		}
	}
	if s.config.DispatchProfile == DispatchGLIBC241 {
		out = append(out, "GLIBC_TUNABLES="+portableGLIBC241)
	}
	return out
}

func (s *Session) Collect(rootPID int) *Report {
	report := CollectDirectory(s.dir, rootPID)
	report.Family = s.family
	report.EngineProfile = EngineProfile
	report.DispatchProfile = s.config.DispatchProfile
	if report.DispatchProfile == "" {
		report.DispatchProfile = DispatchDefault
	}
	report.HostLibc = s.hostLibc
	return &report
}
