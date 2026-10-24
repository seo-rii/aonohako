//go:build linux && amd64

package sandbox

import "golang.org/x/sys/unix"

// fifoCreationFilter requires the accumulator to contain the syscall number.
// Nonmatching syscalls fall through unchanged. Even with this opt-in, device,
// socket, and regular-file creation through mknod remain forbidden.
func fifoCreationFilter(sysno, modeArg, deviceArg uint32, enabled bool) []unix.SockFilter {
	deny := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	if !enabled {
		return []unix.SockFilter{
			{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: sysno, Jf: 1},
			{Code: unix.BPF_RET | unix.BPF_K, K: deny},
		}
	}
	const arg0Offset = 16
	return []unix.SockFilter{
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: sysno, Jf: 11},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: arg0Offset + modeArg*8 + 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: 0, Jf: 7},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: arg0Offset + modeArg*8},
		{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: unix.S_IFMT},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.S_IFIFO, Jf: 4},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: arg0Offset + deviceArg*8 + 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: 0, Jf: 2},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: arg0Offset + deviceArg*8},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: 0, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: deny},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
}
