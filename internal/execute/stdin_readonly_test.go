package execute

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/model"
)

const stdinReadonlyProbe = `import errno
import fcntl
import mmap
import os
import sys

original = b"original\n"
errors = []
for operation in ("write", "pwrite", "shared mmap"):
    try:
        if operation == "write":
            os.write(0, b"MUT")
        elif operation == "pwrite":
            os.pwrite(0, b"MUT", 0)
        else:
            with mmap.mmap(0, len(original), flags=mmap.MAP_SHARED,
                           prot=mmap.PROT_READ | mmap.PROT_WRITE) as mapped:
                mapped[:3] = b"MAP"
                mapped.flush()
    except OSError as error:
        expected_errno = errno.EACCES if operation == "shared mmap" else errno.EBADF
        if error.errno != expected_errno:
            errors.append(operation + " failed with unexpected errno " + str(error.errno))
    else:
        errors.append(operation + " succeeded")

if fcntl.fcntl(0, fcntl.F_GETFL) & os.O_ACCMODE != os.O_RDONLY:
    errors.append("stdin descriptor is writable")
if os.pread(0, len(original) + 1, 0) != original:
    errors.append("stdin contents changed")
if errors:
    print("\n".join(errors))
    sys.exit(1)
print("read-only stdin; write/pwrite/shared mmap blocked; content preserved")
`

const stdinReadonlyResult = "read-only stdin; write/pwrite/shared mmap blocked; content preserved\n"

// Check the probe with both descriptor modes outside the sandbox too. A chmod
// on the original writable descriptor must not be enough to satisfy it.
func TestStdinReadonlyProbeDistinguishesOpenModes(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	for _, readonly := range []bool{false, true} {
		name := "writable descriptor after chmod"
		if readonly {
			name = "read-only descriptor after reopen"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stdin")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			if _, err := file.WriteString("original\n"); err != nil {
				t.Fatal(err)
			}
			if err := file.Chmod(0o400); err != nil {
				t.Fatal(err)
			}
			if readonly {
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				file, err = os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
			} else if _, err := file.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(python, "-c", stdinReadonlyProbe)
			cmd.Stdin = file
			output, err := cmd.CombinedOutput()
			if readonly {
				if err != nil || string(output) != stdinReadonlyResult {
					t.Fatalf("read-only probe failed: %v; output = %s", err, output)
				}
				contents, readErr := os.ReadFile(path)
				if readErr != nil || string(contents) != "original\n" {
					t.Fatalf("input changed: %q, %v", contents, readErr)
				}
				return
			}
			if err == nil {
				t.Fatalf("probe accepted a writable descriptor: %s", output)
			}
			for _, want := range []string{"write succeeded", "pwrite succeeded", "shared mmap succeeded", "stdin contents changed"} {
				if !strings.Contains(string(output), want) {
					t.Errorf("writable control output missing %q: %s", want, output)
				}
			}
		})
	}
}

func TestRunStdinDescriptorIsReadOnly(t *testing.T) {
	requireSandboxSupport(t)
	response := New().Run(context.Background(), &model.RunRequest{
		Lang: "python",
		Binaries: []model.Binary{{
			Name:    "main.py",
			DataB64: b64(stdinReadonlyProbe),
		}},
		Stdin:          "original\n",
		ExpectedStdout: stdinReadonlyResult,
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 128},
	}, Hooks{})
	if response.Status != model.RunStatusAccepted {
		t.Fatalf("stdin descriptor must reject writes and preserve its original bytes: %+v", response)
	}
}
