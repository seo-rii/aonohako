//go:build linux && amd64

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCloseOnExecFallbackMarksRealDescriptors(t *testing.T) {
	for _, unsupported := range []error{unix.ENOSYS, unix.EINVAL, unix.EPERM} {
		t.Run(unsupported.Error(), func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			defer write.Close()
			// Exercise limits above the old hardcoded 1024 fallback boundary.
			high, err := unix.FcntlInt(write.Fd(), unix.F_DUPFD, 1024)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(high)
			for _, fd := range []uintptr{write.Fd(), uintptr(high)} {
				if _, err := unix.FcntlInt(fd, unix.F_SETFD, 0); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			unavailable := func(first, last, flags uint) error {
				calls++
				if first != 3 || last != ^uint(0) || flags != unix.CLOSE_RANGE_CLOEXEC {
					t.Fatal("incorrect close_range arguments")
				}
				return unsupported
			}
			if err := markInheritedFDsCloseOnExec(high+1, unavailable, unix.FcntlInt); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("close_range was not attempted exactly once")
			}
			for _, fd := range []uintptr{write.Fd(), uintptr(high)} {
				flags, err := unix.FcntlInt(fd, unix.F_GETFD, 0)
				if err != nil || flags&unix.FD_CLOEXEC == 0 {
					t.Fatalf("fd %d lacks FD_CLOEXEC: flags=%d err=%v", fd, flags, err)
				}
			}
		})
	}
}

func TestCloseOnExecFallbackFailsClosedOnFcntlError(t *testing.T) {
	unavailable := func(uint, uint, uint) error { return unix.EPERM }
	setFlags := func(fd uintptr, command, value int) (int, error) {
		if fd == 3 {
			return 0, unix.EBADF
		}
		return 0, unix.EACCES
	}
	if err := markInheritedFDsCloseOnExec(5, unavailable, setFlags); !errors.Is(err, unix.EACCES) {
		t.Fatalf("fcntl error was hidden: %v", err)
	}
	if err := markInheritedFDsCloseOnExec(5, func(uint, uint, uint) error { return nil }, func(uintptr, int, int) (int, error) { t.Fatal("fallback on successful close_range"); return 0, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestExecReadyPipeClosesOnFallbackBeforeTargetExit(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_CLOEXEC_CHILD") == "1" {
		if err := markInheritedFDsCloseOnExec(128, func(uint, uint, uint) error { return unix.ENOSYS }, unix.FcntlInt); err != nil {
			t.Fatal(err)
		}
		if err := unix.Exec("/bin/cat", []string{"cat"}, os.Environ()); err != nil {
			t.Fatal(err)
		}
		return
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputRead.Close()
	defer inputWrite.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecReadyPipeClosesOnFallbackBeforeTargetExit$")
	cmd.Stdin = inputRead
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Env = append(os.Environ(), "AONOHAKO_TEST_CLOEXEC_CHILD=1")
	cmd.ExtraFiles = []*os.File{write}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var b [1]byte
		n, err := read.Read(b[:])
		if n != 0 {
			done <- errors.New("unexpected barrier data")
		} else {
			done <- err
		}
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("exec barrier did not close: %v", err)
		}
		// The target cannot finish until this input arrives. Its echoed output
		// also proves EOF came from successful exec, not helper failure.
		if _, err := io.WriteString(inputWrite, "after-exec\n"); err != nil {
			t.Fatal(err)
		}
		if err := inputWrite.Close(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		if output.String() != "after-exec\n" {
			t.Fatalf("target did not execute: %q", output.String())
		}
	case <-time.After(time.Second):
		t.Fatal("ready descriptor survived exec; watchdog would wait for target exit")
	}
}
