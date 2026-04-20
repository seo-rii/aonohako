package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/sandbox"
)

func main() {
	if sandbox.MaybeRunFromEnv() {
		return
	}

	if len(os.Args) != 2 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: aonohako-selftest permissions")
		os.Exit(2)
	}

	switch os.Args[1] {
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

func runPermissionsSuite() error {
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

	cases := []struct {
		name  string
		req   model.RunRequest
		check func(model.RunResponse) error
	}{
		{
			name: "protected-paths-are-not-readable",
			req: model.RunRequest{
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "run.sh",
					DataB64: encodeScript("#!/bin/sh\nif cd /var/aonohako/protected 2>/dev/null; then echo leaked; else echo blocked; fi\nif [ -r /var/aonohako/protected/probe.txt ]; then echo leaked; else echo blocked; fi\nif cd /root 2>/dev/null; then echo leaked; else echo blocked; fi\n"),
					Mode:    "exec",
				}},
				ExpectedStdout: "blocked\nblocked\nblocked\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
		{
			name: "workspace-is-writable-but-submission-is-immutable",
			req: model.RunRequest{
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "run.sh",
					DataB64: encodeScript("#!/bin/sh\nif echo mutated > run.sh 2>/dev/null; then echo overwrote; else echo blocked; fi\necho ok > note.txt\nread value < note.txt\nprintf '%s\\n' \"$value\"\n"),
					Mode:    "exec",
				}},
				ExpectedStdout: "blocked\nok\n",
				Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
			},
		},
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

	_, _ = fmt.Fprintln(os.Stdout, "sandbox permissions ok")
	return nil
}

func encodeScript(body string) string {
	return base64.StdEncoding.EncodeToString([]byte(body))
}
