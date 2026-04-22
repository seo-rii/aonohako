package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/sandbox"
)

type suiteCase struct {
	name  string
	req   model.RunRequest
	check func(model.RunResponse) error
}

func main() {
	if sandbox.MaybeRunFromEnv() {
		return
	}

	if len(os.Args) != 2 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: aonohako-selftest image-permissions|permissions")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "image-permissions":
		if err := runImagePermissionsSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "permissions":
		if err := runPermissionsSuite(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown selftest suite: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func runImagePermissionsSuite() error {
	if err := runDirectImagePermissionChecks(); err != nil {
		return err
	}
	return nil
}

func runPermissionsSuite() error {
	if err := runDirectImagePermissionChecks(); err != nil {
		return err
	}

	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-selftest-unixgram-%d.sock", time.Now().UnixNano()))
	addr := &net.UnixAddr{Name: socketPath, Net: "unixgram"}
	listener, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return fmt.Errorf("listen unixgram socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o777); err != nil {
		return fmt.Errorf("chmod unixgram socket: %w", err)
	}

	cases := []suiteCase{
		{
			name: "unix-datagram-send-is-blocked",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "main.py",
					DataB64: encodeScript(fmt.Sprintf(
						"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)\n    s.sendto(b'escape', %q)\n    print('sent')\nexcept OSError:\n    print('blocked')\n",
						socketPath,
					)),
				}},
				ExpectedStdout: "blocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
			check: func(model.RunResponse) error {
				_ = listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				buf := make([]byte, 64)
				if n, _, err := listener.ReadFromUnix(buf); err == nil {
					return fmt.Errorf("unix-datagram-send-is-blocked: unexpected datagram %q", string(buf[:n]))
				}
				return nil
			},
		},
		{
			name: "socketpair-creation-is-blocked",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "main.py",
					DataB64: encodeScript(
						"import socket\ntry:\n    socket.socketpair()\n    print('created')\nexcept OSError:\n    print('blocked')\n",
					),
				}},
				ExpectedStdout: "blocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		{
			name: "namespace-unshare-is-blocked",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "main.py",
					DataB64: encodeScript(
						"import ctypes\nlibc = ctypes.CDLL(None, use_errno=True)\ntry:\n    rc = libc.unshare(0x00020000)\n    if rc == 0:\n        print('escaped')\n    else:\n        print('blocked')\nexcept Exception:\n    print('blocked')\n",
					),
				}},
				ExpectedStdout: "blocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		{
			name: "submitted-files-cannot-be-replaced",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{
					{
						Name: "main.py",
						DataB64: encodeScript(
							"from pathlib import Path\nimport os\ntry:\n    os.unlink('data.txt')\n    print('unlinked')\nexcept OSError:\n    print('blocked-unlink')\nPath('swap.txt').write_text('mutated\\n')\ntry:\n    os.replace('swap.txt', 'data.txt')\n    print('replaced')\nexcept OSError:\n    print('blocked-replace')\nprint(Path('data.txt').read_text(), end='')\n",
						),
					},
					{
						Name:    "data.txt",
						DataB64: encodeScript("original\n"),
					},
				},
				ExpectedStdout: "blocked-unlink\nblocked-replace\noriginal\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		{
			name: "outside-temp-dirs-are-not-writable",
			req: model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name: "main.py",
					DataB64: encodeScript(
						"from pathlib import Path\nfor target in ['/tmp/aonohako-outside.txt', '/var/tmp/aonohako-outside.txt']:\n    try:\n        Path(target).write_text('escape')\n        print('wrote')\n    except OSError:\n        print('blocked')\n",
					),
				}},
				ExpectedStdout: "blocked\nblocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128, WorkspaceBytes: 8 << 10},
			},
		},
	}

	if err := runSuiteCases(cases); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(os.Stdout, "sandbox permissions ok")
	return nil
}

func runDirectImagePermissionChecks() error {
	protectedOut, protectedErr, err := runAsSandboxUser(
		"if [ -x /var/aonohako/protected ]; then echo leaked; else echo blocked; fi; "+
			"if [ -r /var/aonohako/protected/probe.txt ]; then echo leaked; else echo blocked; fi; "+
			"if [ -x /root ]; then echo leaked; else echo blocked; fi",
		"",
	)
	if err != nil {
		return fmt.Errorf("protected-paths-are-not-readable: %w\n%s", err, protectedErr)
	}
	if protectedOut != "blocked\nblocked\nblocked\n" {
		return fmt.Errorf("protected-paths-are-not-readable: unexpected stdout %q stderr %q", protectedOut, protectedErr)
	}

	imageOut, imageErr, err := runAsSandboxUser(
		"for p in /etc/debian_version /etc/os-release /var/lib/dpkg/status; do if [ -r \"$p\" ]; then echo leaked; else echo blocked; fi; done; "+
			"for d in /usr/share/doc /usr/share/common-licenses /usr/share/bash-completion /var/cache/debconf /etc/apt; do "+
			"if cd \"$d\" 2>/dev/null; then echo leaked; else echo blocked; fi; "+
			"done",
		"",
	)
	if err != nil {
		return fmt.Errorf("image-metadata-paths-are-not-readable: %w\n%s", err, imageErr)
	}
	if imageOut != "blocked\nblocked\nblocked\nblocked\nblocked\nblocked\nblocked\nblocked\n" {
		return fmt.Errorf("image-metadata-paths-are-not-readable: unexpected stdout %q stderr %q", imageOut, imageErr)
	}

	scratchOut, scratchErr, err := runAsSandboxUser(
		"for p in /tmp /var/tmp /run/lock /dev/shm /dev/mqueue; do "+
			"if [ -e \"$p\" ]; then "+
			"if [ -w \"$p\" ]; then echo \"$p leaked\"; else echo \"$p blocked\"; fi; "+
			"fi; "+
			"done",
		"",
	)
	if err != nil {
		return fmt.Errorf("global-scratch-dirs-are-not-writable: %w\n%s", err, scratchErr)
	}
	lines := strings.Fields(strings.TrimSpace(scratchOut))
	if len(lines) == 0 {
		return fmt.Errorf("global-scratch-dirs-are-not-writable: no scratch dirs were checked")
	}
	for i := 0; i+1 < len(lines); i += 2 {
		if lines[i+1] != "blocked" {
			return fmt.Errorf("global-scratch-dirs-are-not-writable: unexpected stdout %q stderr %q", scratchOut, scratchErr)
		}
	}

	workDir, err := os.MkdirTemp("", "aonohako-selftest-work-*")
	if err != nil {
		return fmt.Errorf("mktemp selftest workdir: %w", err)
	}
	defer os.RemoveAll(workDir)
	if err := os.Chmod(workDir, 0o755); err != nil {
		return fmt.Errorf("chmod selftest workdir: %w", err)
	}

	boxDir := filepath.Join(workDir, "box")
	if err := os.MkdirAll(boxDir, 0o777); err != nil {
		return fmt.Errorf("mkdir selftest box: %w", err)
	}
	if err := os.Chmod(boxDir, 0o777|os.ModeSticky); err != nil {
		return fmt.Errorf("chmod selftest box: %w", err)
	}
	probePath := filepath.Join(boxDir, "probe")
	if err := os.WriteFile(probePath, []byte("immutable\n"), 0o555); err != nil {
		return fmt.Errorf("write selftest probe: %w", err)
	}

	workspaceOut, workspaceErr, err := runAsSandboxUser(
		"if printf mutated > probe 2>/dev/null; then echo overwrote; else echo blocked; fi; printf ok > note.txt; cat note.txt",
		boxDir,
	)
	if err != nil {
		return fmt.Errorf("workspace-is-writable-but-submission-is-immutable: %w\n%s", err, workspaceErr)
	}
	if workspaceOut != "blocked\nok" && workspaceOut != "blocked\nok\n" {
		return fmt.Errorf("workspace-is-writable-but-submission-is-immutable: unexpected stdout %q stderr %q", workspaceOut, workspaceErr)
	}
	return nil
}

func runAsSandboxUser(script, dir string) (string, string, error) {
	cmd := exec.Command("/bin/sh", "-lc", script)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"HOME=/tmp",
	}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: 65532, Gid: 65532},
		}
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func runSuiteCases(cases []suiteCase) error {
	svc := execute.New()
	for _, tc := range cases {
		resp := svc.Run(context.Background(), &tc.req, execute.Hooks{})
		if resp.Status != model.RunStatusAccepted {
			return fmt.Errorf("%s: expected Accepted, got %+v", tc.name, resp)
		}
		if tc.check != nil {
			if err := tc.check(resp); err != nil {
				return err
			}
		}
	}
	return nil
}

func encodeScript(body string) string {
	return base64.StdEncoding.EncodeToString([]byte(body))
}
