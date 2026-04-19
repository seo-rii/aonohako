package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

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
	cases := []struct {
		name string
		req  model.RunRequest
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
	}

	svc := execute.New()
	for _, tc := range cases {
		resp := svc.Run(context.Background(), &tc.req, execute.Hooks{})
		if resp.Status != model.RunStatusAccepted {
			return fmt.Errorf("%s: expected Accepted, got %+v", tc.name, resp)
		}
	}

	_, _ = fmt.Fprintln(os.Stdout, "sandbox permissions ok")
	return nil
}

func encodeScript(body string) string {
	return base64.StdEncoding.EncodeToString([]byte(body))
}
