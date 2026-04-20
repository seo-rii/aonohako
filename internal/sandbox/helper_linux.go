//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func MaybeRunFromEnv() bool {
	if os.Getenv(HelperModeEnv) != HelperModeExec {
		return false
	}

	fail := func(format string, args ...any) {
		_, _ = fmt.Fprintf(os.Stderr, "sandbox-init: "+format+"\n", args...)
		os.Exit(120)
	}

	reqPath := os.Getenv(RequestPathEnv)
	if reqPath == "" {
		fail("missing %s", RequestPathEnv)
	}

	raw, err := os.ReadFile(reqPath)
	if err != nil {
		fail("read request: %v", err)
	}
	_ = os.Remove(reqPath)

	var req ExecRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		fail("decode request: %v", err)
	}
	if len(req.Command) == 0 {
		fail("empty command")
	}
	if !filepath.IsAbs(req.Command[0]) {
		fail("command must be absolute: %s", req.Command[0])
	}
	if req.Dir == "" || !filepath.IsAbs(req.Dir) {
		fail("dir must be absolute: %s", req.Dir)
	}

	if err := unix.CloseRange(3, ^uint(0), 0); err != nil && err != unix.ENOSYS && err != unix.EINVAL {
		for fd := 3; fd < 1024; fd++ {
			_ = unix.Close(fd)
		}
	}

	timeMs := req.Limits.TimeMs
	if timeMs < 1 {
		timeMs = 1
	}
	cpuSec := (timeMs+999)/1000 + 1
	memMB := req.Limits.MemoryMB
	if memMB < 16 {
		memMB = 16
	}
	asMB := memMB + 64
	if asMB < 256 {
		asMB = 256
	}
	threadLimit := req.ThreadLimit
	if threadLimit < 32 {
		threadLimit = 32
	}
	nprocLimit := uint64(threadLimit)
	if entries, err := os.ReadDir("/proc"); err == nil {
		currentUID := fmt.Sprintf("%d", os.Getuid())
		currentCount := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if name == "" || name[0] < '0' || name[0] > '9' {
				continue
			}
			rawStatus, err := os.ReadFile(filepath.Join("/proc", name, "status"))
			if err != nil {
				continue
			}
			matchesUID := false
			threads := 1
			for _, line := range bytes.Split(rawStatus, []byte{'\n'}) {
				if !bytes.HasPrefix(line, []byte("Uid:")) {
					if bytes.HasPrefix(line, []byte("Threads:")) {
						fields := bytes.Fields(line)
						if len(fields) >= 2 {
							if parsed, err := strconv.Atoi(string(fields[1])); err == nil && parsed > 0 {
								threads = parsed
							}
						}
					}
					continue
				}
				fields := bytes.Fields(line)
				if len(fields) >= 2 && string(fields[1]) == currentUID {
					matchesUID = true
				}
			}
			if matchesUID {
				currentCount += threads
			}
		}
		nprocLimit = uint64(currentCount + threadLimit + 8)
	}
	fileSizeLimit := uint64(128 * 1024 * 1024)
	if req.Limits.WorkspaceBytes > 0 {
		fileSizeLimit = uint64(req.Limits.WorkspaceBytes)
	}
	limits := []struct {
		resource int
		value    uint64
	}{
		{unix.RLIMIT_CPU, uint64(cpuSec)},
		{unix.RLIMIT_AS, uint64(asMB) * 1024 * 1024},
		{unix.RLIMIT_NOFILE, 64},
		{unix.RLIMIT_NPROC, nprocLimit},
		{unix.RLIMIT_FSIZE, fileSizeLimit},
		{unix.RLIMIT_CORE, 0},
	}
	for _, item := range limits {
		if err := unix.Setrlimit(item.resource, &unix.Rlimit{Cur: item.value, Max: item.value}); err != nil {
			fail("setrlimit(%d): %v", item.resource, err)
		}
	}

	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		fail("prctl dumpable: %v", err)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		fail("prctl no_new_privs: %v", err)
	}

	seccompDataNrOffset := uint32(0)
	seccompDataArchOffset := uint32(4)
	seccompDataArg0Offset := uint32(16)
	allow := uint32(unix.SECCOMP_RET_ALLOW)
	deny := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	clone3Deny := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS))
	kill := uint32(unix.SECCOMP_RET_KILL_PROCESS)

	archAudit := uint32(unix.AUDIT_ARCH_X86_64)
	switch runtime.GOARCH {
	case "arm64":
		archAudit = unix.AUDIT_ARCH_AARCH64
	case "amd64":
		archAudit = unix.AUDIT_ARCH_X86_64
	case "386":
		archAudit = unix.AUDIT_ARCH_I386
	case "arm":
		archAudit = unix.AUDIT_ARCH_ARM
	case "ppc64le":
		archAudit = unix.AUDIT_ARCH_PPC64LE
	case "riscv64":
		archAudit = unix.AUDIT_ARCH_RISCV64
	case "s390x":
		archAudit = unix.AUDIT_ARCH_S390X
	}

	program := make([]unix.SockFilter, 0, 96)
	appendStmt := func(code uint16, k uint32) {
		program = append(program, unix.SockFilter{Code: code, K: k})
	}
	appendJump := func(code uint16, k uint32, jt, jf uint8) {
		program = append(program, unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k})
	}

	appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArchOffset)
	appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, archAudit, 1, 0)
	appendStmt(unix.BPF_RET|unix.BPF_K, kill)
	appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataNrOffset)

	for _, sysno := range []uint32{
		uint32(unix.SYS_FORK),
		uint32(unix.SYS_VFORK),
		uint32(unix.SYS_UNSHARE),
		uint32(unix.SYS_SETNS),
		uint32(unix.SYS_CHROOT),
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_UMOUNT2),
		uint32(unix.SYS_PIVOT_ROOT),
		uint32(unix.SYS_OPEN_TREE),
		uint32(unix.SYS_MOVE_MOUNT),
		uint32(unix.SYS_FSOPEN),
		uint32(unix.SYS_FSCONFIG),
		uint32(unix.SYS_FSMOUNT),
		uint32(unix.SYS_FSPICK),
		uint32(unix.SYS_MOUNT_SETATTR),
		uint32(unix.SYS_PTRACE),
		uint32(unix.SYS_PROCESS_VM_READV),
		uint32(unix.SYS_PROCESS_VM_WRITEV),
		uint32(unix.SYS_PIDFD_OPEN),
		uint32(unix.SYS_PIDFD_GETFD),
		uint32(unix.SYS_PIDFD_SEND_SIGNAL),
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_USERFAULTFD),
		uint32(unix.SYS_PERF_EVENT_OPEN),
		uint32(unix.SYS_OPEN_BY_HANDLE_AT),
		uint32(unix.SYS_NAME_TO_HANDLE_AT),
		uint32(unix.SYS_FANOTIFY_INIT),
		uint32(unix.SYS_FANOTIFY_MARK),
		uint32(unix.SYS_INIT_MODULE),
		uint32(unix.SYS_FINIT_MODULE),
		uint32(unix.SYS_DELETE_MODULE),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_KEXEC_FILE_LOAD),
		uint32(unix.SYS_CHMOD),
		uint32(unix.SYS_FCHMOD),
		uint32(unix.SYS_FCHMODAT),
		uint32(unix.SYS_CHOWN),
		uint32(unix.SYS_FCHOWN),
		uint32(unix.SYS_LCHOWN),
		uint32(unix.SYS_FCHOWNAT),
	} {
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 1)
		appendStmt(unix.BPF_RET|unix.BPF_K, deny)
	}

	appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_CLONE3), 0, 1)
	appendStmt(unix.BPF_RET|unix.BPF_K, clone3Deny)

	appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_CLONE), 0, 4)
	appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArg0Offset)
	appendJump(unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K, unix.CLONE_THREAD, 0, 1)
	appendStmt(unix.BPF_RET|unix.BPF_K, allow)
	appendStmt(unix.BPF_RET|unix.BPF_K, deny)

	if !req.EnableNetwork {
		for _, sysno := range []uint32{uint32(unix.SYS_SOCKET), uint32(unix.SYS_SOCKETPAIR)} {
			appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 3)
			appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArg0Offset)
			appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_UNIX, 1, 0)
			appendStmt(unix.BPF_RET|unix.BPF_K, deny)
			appendStmt(unix.BPF_RET|unix.BPF_K, allow)
		}
	}

	appendStmt(unix.BPF_RET|unix.BPF_K, allow)

	prog := unix.SockFprog{Len: uint16(len(program)), Filter: &program[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, uintptr(unix.SECCOMP_MODE_FILTER), uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		fail("prctl seccomp: %v", err)
	}

	if err := os.Chdir(req.Dir); err != nil {
		fail("chdir %s: %v", req.Dir, err)
	}
	if err := syscall.Exec(req.Command[0], req.Command, req.Env); err != nil {
		fail("exec %s: %v", req.Command[0], err)
	}
	return true
}
