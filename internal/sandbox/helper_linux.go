//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

func MaybeRunFromEnv() bool {
	if os.Getenv(HelperModeEnv) != HelperModeExec {
		return false
	}
	runtime.GOMAXPROCS(1)
	runtime.LockOSThread()
	debug.SetGCPercent(-1)

	fail := func(format string, args ...any) {
		_, _ = fmt.Fprintf(os.Stderr, "sandbox-init: "+format+"\n", args...)
		os.Exit(120)
	}

	var raw []byte
	if reqFD := os.Getenv(RequestFDEnv); reqFD != "" {
		fd, err := strconv.Atoi(reqFD)
		if err != nil {
			fail("invalid %s: %v", RequestFDEnv, err)
		}
		reqFile := os.NewFile(uintptr(fd), "sandbox-request")
		if reqFile == nil {
			fail("open request fd %d", fd)
		}
		raw, err = io.ReadAll(reqFile)
		_ = reqFile.Close()
		if err != nil {
			fail("read request fd: %v", err)
		}
	} else {
		reqPath := os.Getenv(RequestPathEnv)
		if reqPath == "" {
			fail("missing %s", RequestPathEnv)
		}

		var err error
		raw, err = os.ReadFile(reqPath)
		if err != nil {
			fail("read request: %v", err)
		}
		_ = os.Remove(reqPath)
	}

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
	execPath, err := unix.BytePtrFromString(req.Command[0])
	if err != nil {
		fail("encode exec path: %v", err)
	}
	argv := make([]*byte, len(req.Command)+1)
	for i, arg := range req.Command {
		ptr, err := unix.BytePtrFromString(arg)
		if err != nil {
			fail("encode argv[%d]: %v", i, err)
		}
		argv[i] = ptr
	}
	envv := make([]*byte, len(req.Env)+1)
	for i, item := range req.Env {
		ptr, err := unix.BytePtrFromString(item)
		if err != nil {
			fail("encode env[%d]: %v", i, err)
		}
		envv[i] = ptr
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
	addressSpaceLimitBytes := req.AddressSpaceLimitBytes
	if addressSpaceLimitBytes == 0 {
		asMB := memMB + 64
		if asMB < 512 {
			asMB = 512
		}
		addressSpaceLimitBytes = uint64(asMB) * 1024 * 1024
	}
	threadLimit := req.ThreadLimit
	if threadLimit < 32 {
		threadLimit = 32
	}
	openFileLimit := req.OpenFileLimit
	if openFileLimit < 64 {
		openFileLimit = 64
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
	limits := []struct {
		resource int
		value    uint64
	}{
		{unix.RLIMIT_CPU, uint64(cpuSec)},
		{unix.RLIMIT_NOFILE, 64},
		{unix.RLIMIT_NPROC, nprocLimit},
		{unix.RLIMIT_CORE, 0},
		{unix.RLIMIT_STACK, 8 * 1024 * 1024},
		{unix.RLIMIT_MEMLOCK, 0},
		{unix.RLIMIT_MSGQUEUE, 0},
	}
	limits[1].value = uint64(openFileLimit)
	if !req.DisableAddressSpaceLimit {
		limits = append(limits, struct {
			resource int
			value    uint64
		}{unix.RLIMIT_AS, addressSpaceLimitBytes})
	}
	if !req.DisableFileSizeLimit {
		fileSizeLimit := uint64(128 * 1024 * 1024)
		if req.Limits.WorkspaceBytes > 0 {
			fileSizeLimit = uint64(req.Limits.WorkspaceBytes)
		}
		if req.FileSizeLimitBytes > 0 {
			fileSizeLimit = req.FileSizeLimitBytes
		}
		limits = append(limits, struct {
			resource int
			value    uint64
		}{unix.RLIMIT_FSIZE, fileSizeLimit})
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

	program := make([]unix.SockFilter, 0, 112)
	appendStmt := func(code uint16, k uint32) {
		program = append(program, unix.SockFilter{Code: code, K: k})
	}
	appendJump := func(code uint16, k uint32, jt, jf uint8) {
		program = append(program, unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k})
	}
	appendAllowOnlyInternetDomain := func(sysno uint32) {
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 5)
		appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArg0Offset)
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_INET, 2, 0)
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_INET6, 1, 0)
		appendStmt(unix.BPF_RET|unix.BPF_K, deny)
		appendStmt(unix.BPF_RET|unix.BPF_K, allow)
	}
	appendAllowOnlyUnixDomain := func(sysno uint32) {
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 4)
		appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArg0Offset)
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_UNIX, 1, 0)
		appendStmt(unix.BPF_RET|unix.BPF_K, deny)
		appendStmt(unix.BPF_RET|unix.BPF_K, allow)
	}
	appendAllowOnlyZeroArg := func(sysno uint32, argIndex uint32) {
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 4)
		appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArg0Offset+argIndex*8)
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, 0, 1, 0)
		appendStmt(unix.BPF_RET|unix.BPF_K, deny)
		appendStmt(unix.BPF_RET|unix.BPF_K, allow)
	}

	appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataArchOffset)
	appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, archAudit, 1, 0)
	appendStmt(unix.BPF_RET|unix.BPF_K, kill)
	appendStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataNrOffset)

	for _, sysno := range []uint32{
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
		uint32(unix.SYS_STATMOUNT),
		uint32(unix.SYS_LISTMOUNT),
		uint32(unix.SYS_PTRACE),
		uint32(unix.SYS_PROCESS_VM_READV),
		uint32(unix.SYS_PROCESS_VM_WRITEV),
		uint32(unix.SYS_PIDFD_OPEN),
		uint32(unix.SYS_PIDFD_GETFD),
		uint32(unix.SYS_PIDFD_SEND_SIGNAL),
		uint32(unix.SYS_KILL),
		uint32(unix.SYS_TKILL),
		uint32(unix.SYS_TGKILL),
		uint32(unix.SYS_SETPRIORITY),
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_IO_SETUP),
		uint32(unix.SYS_IO_DESTROY),
		uint32(unix.SYS_IO_SUBMIT),
		uint32(unix.SYS_IO_GETEVENTS),
		uint32(unix.SYS_IO_URING_SETUP),
		uint32(unix.SYS_IO_URING_ENTER),
		uint32(unix.SYS_IO_URING_REGISTER),
		uint32(unix.SYS_USERFAULTFD),
		uint32(unix.SYS_MLOCK),
		uint32(unix.SYS_MLOCK2),
		uint32(unix.SYS_MLOCKALL),
		uint32(unix.SYS_MUNLOCK),
		uint32(unix.SYS_MUNLOCKALL),
		uint32(unix.SYS_SHMGET),
		uint32(unix.SYS_SHMAT),
		uint32(unix.SYS_SHMDT),
		uint32(unix.SYS_SHMCTL),
		uint32(unix.SYS_PERF_EVENT_OPEN),
		uint32(unix.SYS_CACHESTAT),
		uint32(unix.SYS_OPEN_BY_HANDLE_AT),
		uint32(unix.SYS_NAME_TO_HANDLE_AT),
		uint32(unix.SYS_FANOTIFY_INIT),
		uint32(unix.SYS_FANOTIFY_MARK),
		uint32(unix.SYS_LOOKUP_DCOOKIE),
		uint32(unix.SYS_ADD_KEY),
		uint32(unix.SYS_REQUEST_KEY),
		uint32(unix.SYS_KEYCTL),
		uint32(unix.SYS_INIT_MODULE),
		uint32(unix.SYS_FINIT_MODULE),
		uint32(unix.SYS_DELETE_MODULE),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_KEXEC_FILE_LOAD),
		uint32(unix.SYS_ACCT),
		uint32(unix.SYS_NFSSERVCTL),
		uint32(unix.SYS_QUOTACTL),
		uint32(unix.SYS_QUOTACTL_FD),
		uint32(unix.SYS_PROCESS_MADVISE),
		uint32(unix.SYS_PROCESS_MRELEASE),
		uint32(unix.SYS_GET_MEMPOLICY),
		uint32(unix.SYS_MBIND),
		uint32(unix.SYS_SET_MEMPOLICY),
		uint32(unix.SYS_SET_MEMPOLICY_HOME_NODE),
		uint32(unix.SYS_MIGRATE_PAGES),
		uint32(unix.SYS_MOVE_PAGES),
		uint32(unix.SYS_KCMP),
		uint32(unix.SYS_SECCOMP),
		uint32(unix.SYS_LANDLOCK_CREATE_RULESET),
		uint32(unix.SYS_LANDLOCK_ADD_RULE),
		uint32(unix.SYS_LANDLOCK_RESTRICT_SELF),
		uint32(unix.SYS_LSM_GET_SELF_ATTR),
		uint32(unix.SYS_LSM_SET_SELF_ATTR),
		uint32(unix.SYS_LSM_LIST_MODULES),
		uint32(unix.SYS_CLOCK_SETTIME),
		uint32(unix.SYS_SETTIMEOFDAY),
		uint32(unix.SYS_ADJTIMEX),
		uint32(unix.SYS_SYSLOG),
		uint32(unix.SYS_REBOOT),
		uint32(unix.SYS_SWAPON),
		uint32(unix.SYS_SWAPOFF),
		uint32(unix.SYS_SETHOSTNAME),
		uint32(unix.SYS_SETDOMAINNAME),
		uint32(unix.SYS_CHMOD),
		uint32(unix.SYS_FCHMOD),
		uint32(unix.SYS_FCHMODAT),
		uint32(unix.SYS_FCHMODAT2),
		uint32(unix.SYS_CHOWN),
		uint32(unix.SYS_FCHOWN),
		uint32(unix.SYS_LCHOWN),
		uint32(unix.SYS_FCHOWNAT),
		uint32(unix.SYS_MKNOD),
		uint32(unix.SYS_MKNODAT),
		uint32(unix.SYS_EXECVEAT),
	} {
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 1)
		appendStmt(unix.BPF_RET|unix.BPF_K, deny)
	}
	if !req.AllowMemfdCreate {
		appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_MEMFD_CREATE), 0, 1)
		appendStmt(unix.BPF_RET|unix.BPF_K, deny)
	}

	if !req.AllowProcessGroups {
		for _, sysno := range []uint32{
			uint32(unix.SYS_SETPGID),
			uint32(unix.SYS_SETSID),
		} {
			appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 1)
			appendStmt(unix.BPF_RET|unix.BPF_K, deny)
		}
	}

	if !req.AllowProcesses {
		for _, sysno := range []uint32{
			uint32(unix.SYS_FORK),
			uint32(unix.SYS_VFORK),
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
	}

	if !req.EnableNetwork {
		if req.AllowUnixSockets {
			appendAllowOnlyUnixDomain(uint32(unix.SYS_SOCKET))
			appendAllowOnlyUnixDomain(uint32(unix.SYS_SOCKETPAIR))
			appendAllowOnlyZeroArg(uint32(unix.SYS_SENDTO), 5)
			appendAllowOnlyZeroArg(uint32(unix.SYS_RECVFROM), 4)
			if !req.AllowUnixSocketMessages {
				for _, sysno := range []uint32{
					uint32(unix.SYS_SENDMSG),
					uint32(unix.SYS_RECVMSG),
				} {
					appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 1)
					appendStmt(unix.BPF_RET|unix.BPF_K, deny)
				}
			}
		} else {
			for _, sysno := range []uint32{
				uint32(unix.SYS_SOCKET),
				uint32(unix.SYS_SOCKETPAIR),
				uint32(unix.SYS_SENDTO),
				uint32(unix.SYS_RECVFROM),
				uint32(unix.SYS_SENDMSG),
				uint32(unix.SYS_RECVMSG),
			} {
				appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 1)
				appendStmt(unix.BPF_RET|unix.BPF_K, deny)
			}
		}
		for _, sysno := range []uint32{
			uint32(unix.SYS_CONNECT),
			uint32(unix.SYS_BIND),
			uint32(unix.SYS_LISTEN),
			uint32(unix.SYS_ACCEPT),
			uint32(unix.SYS_ACCEPT4),
			uint32(unix.SYS_SHUTDOWN),
			uint32(unix.SYS_SENDMMSG),
			uint32(unix.SYS_RECVMMSG),
		} {
			appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 1)
			appendStmt(unix.BPF_RET|unix.BPF_K, deny)
		}
	} else {
		appendAllowOnlyInternetDomain(uint32(unix.SYS_SOCKET))
		if req.AllowUnixSockets {
			appendAllowOnlyUnixDomain(uint32(unix.SYS_SOCKETPAIR))
			appendAllowOnlyZeroArg(uint32(unix.SYS_SENDTO), 5)
			appendAllowOnlyZeroArg(uint32(unix.SYS_RECVFROM), 4)
		} else {
			appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_SOCKETPAIR), 0, 1)
			appendStmt(unix.BPF_RET|unix.BPF_K, deny)
		}
		for _, sysno := range []uint32{
			uint32(unix.SYS_BIND),
			uint32(unix.SYS_LISTEN),
			uint32(unix.SYS_ACCEPT),
			uint32(unix.SYS_ACCEPT4),
		} {
			appendJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, sysno, 0, 1)
			appendStmt(unix.BPF_RET|unix.BPF_K, deny)
		}
	}

	appendStmt(unix.BPF_RET|unix.BPF_K, allow)

	prog := unix.SockFprog{Len: uint16(len(program)), Filter: &program[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, uintptr(unix.SECCOMP_MODE_FILTER), uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		fail("prctl seccomp: %v", err)
	}

	if err := unix.Chdir(req.Dir); err != nil {
		fail("chdir %s: %v", req.Dir, err)
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil && err != unix.ENOSYS && err != unix.EINVAL {
		for fd := 3; fd < 1024; fd++ {
			unix.CloseOnExec(fd)
		}
	}
	_, _, errno := unix.RawSyscall(unix.SYS_EXECVE, uintptr(unsafe.Pointer(execPath)), uintptr(unsafe.Pointer(&argv[0])), uintptr(unsafe.Pointer(&envv[0])))
	runtime.KeepAlive(execPath)
	runtime.KeepAlive(argv)
	runtime.KeepAlive(envv)
	if errno != 0 {
		fail("exec %s: %v", req.Command[0], errno)
	}
	return true
}
