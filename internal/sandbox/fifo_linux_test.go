//go:build linux && amd64

package sandbox

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFIFOOnlyCreationFilter(t *testing.T) {
	for _, call := range []struct{ number, modeArg, deviceArg uint32 }{
		{unix.SYS_MKNOD, 1, 2}, {unix.SYS_MKNODAT, 2, 3},
	} {
		for _, enabled := range []bool{false, true} {
			for _, tc := range []struct {
				name         string
				mode, device uint64
				allowed      bool
			}{
				{"fifo", unix.S_IFIFO | 0600, 0, true},
				{"fifo permissions", unix.S_IFIFO | 0777, 0, true},
				{"regular file", unix.S_IFREG | 0600, 0, false},
				{"socket", unix.S_IFSOCK | 0600, 0, false},
				{"character device", unix.S_IFCHR | 0600, 0, false},
				{"block device", unix.S_IFBLK | 0600, 0, false},
				{"missing type", 0600, 0, false},
				{"mode high word", 1<<32 | unix.S_IFIFO | 0600, 0, false},
				{"device low word", unix.S_IFIFO | 0600, 1, false},
				{"device high word", unix.S_IFIFO | 0600, 1 << 32, false},
			} {
				var data [64]byte
				binary.LittleEndian.PutUint64(data[16+call.modeArg*8:], tc.mode)
				binary.LittleEndian.PutUint64(data[16+call.deviceArg*8:], tc.device)
				program := fifoCreationFilter(call.number, call.modeArg, call.deviceArg, enabled)
				want := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
				if enabled && tc.allowed {
					want = unix.SECCOMP_RET_ALLOW
				}
				if got := evaluateFIFOFilter(t, program, call.number, data[:]); got != want {
					t.Fatalf("syscall=%d enabled=%t %s: got %#x, want %#x", call.number, enabled, tc.name, got, want)
				}
				if got := evaluateFIFOFilter(t, program, unix.SYS_GETPID, data[:]); got != 0 {
					t.Fatalf("unrelated syscall must fall through unchanged, got %#x", got)
				}
			}
		}
	}
}

func evaluateFIFOFilter(t *testing.T, program []unix.SockFilter, accumulator uint32, data []byte) uint32 {
	t.Helper()
	for pc := 0; pc < len(program); pc++ {
		instruction := program[pc]
		switch instruction.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			accumulator = binary.LittleEndian.Uint32(data[instruction.K:])
		case unix.BPF_ALU | unix.BPF_AND | unix.BPF_K:
			accumulator &= instruction.K
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
			if accumulator == instruction.K {
				pc += int(instruction.Jt)
			} else {
				pc += int(instruction.Jf)
			}
		case unix.BPF_RET | unix.BPF_K:
			return instruction.K
		default:
			t.Fatalf("unsupported BPF instruction %#x", instruction.Code)
		}
	}
	return 0
}
