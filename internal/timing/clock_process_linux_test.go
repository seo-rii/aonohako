//go:build linux

package timing

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcessCPUClockMatchesWaitUsage(t *testing.T) {
	if os.Getenv("AONOHAKO_TIMING_CPU_HELPER") == "1" {
		fmt.Println("ready")
		if _, err := bufio.NewReader(os.Stdin).ReadByte(); err != nil {
			t.Fatalf("wait for release: %v", err)
		}
		start, err := CurrentProcessCPUTimeNs()
		if err != nil {
			t.Fatalf("read helper CPU start: %v", err)
		}
		for {
			current, err := CurrentProcessCPUTimeNs()
			if err != nil {
				t.Fatalf("read helper CPU: %v", err)
			}
			if current-start >= uint64(20*time.Millisecond) {
				return
			}
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessCPUClockMatchesWaitUsage$")
	cmd.Env = append(os.Environ(), "AONOHAKO_TIMING_CPU_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("create helper stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("create helper stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("wait for helper readiness: line=%q err=%v", line, err)
	}
	baselineNs, err := ProcessCPUTimeNs(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("read helper baseline: %v", err)
	}
	if _, err := stdin.Write([]byte{'x'}); err != nil {
		t.Fatalf("release helper: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for helper: %v", err)
	}
	processStateNs := uint64((cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Nanoseconds())
	if processStateNs < baselineNs {
		t.Fatalf("wait usage %d ns is below process-clock baseline %d ns", processStateNs, baselineNs)
	}
	if targetNs := processStateNs - baselineNs; targetNs < uint64(20*time.Millisecond) {
		t.Fatalf("wait usage minus process-clock baseline = %d ns, want at least 20 ms", targetNs)
	}
}
