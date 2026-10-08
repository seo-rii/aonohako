package execute

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestRunOctavePlotCapture(t *testing.T) {
	if _, err := exec.LookPath("octave-cli"); err != nil {
		t.Skip("Octave is not installed")
	}
	if _, err := os.Stat("/usr/local/lib/aonohako/octave/plot_capture.m"); err != nil {
		t.Skip("Octave image capture helper is not installed")
	}
	requireSandboxSupport(t)
	for _, tc := range []struct {
		name, source, stdout, status, diagnostic string
		capture                                  bool
		images                                   int
	}{
		{
			name: "figures with isolated helper locals and changed directory",
			source: `clear all;
assert (isempty (argv ()));
values = fscanf (stdin, "%d", 2);
printf ("%d\n", sibling (sum (values)));
output_dir = "/wrong"; figures = []; log_file = -1;
mkdir ("scratch"); cd ("scratch");
figure (2); plot (1:3, [1 4 9]);
figure (1); imagesc ([1 2; 3 4]);`,
			stdout: "13\n", status: model.RunStatusAccepted, capture: true, images: 2,
		},
		{
			name: "ordinary execution still denies child processes and FIFOs",
			source: `[pid, message] = fork ();
if (pid == 0), exit (0); endif
[fifo_status, message] = mkfifo ("blocked-fifo", 600);
printf ("%d %d\n", pid, fifo_status);`,
			stdout: "-1 -1\n", status: model.RunStatusAccepted,
		},
		{
			name:   "script error does not capture",
			source: `figure (1); plot (1:3); error ("expected script failure");`,
			status: model.RunStatusRE, capture: true, diagnostic: "expected script failure",
		},
		{
			name:   "explicit exit does not capture",
			source: `figure (1); plot (1:3); exit (0);`,
			status: model.RunStatusAccepted, capture: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &model.RunRequest{
				Lang: "octave", EntryPoint: "nested/entry'quote.m",
				Binaries: []model.Binary{
					{Name: "nested/entry'quote.m", DataB64: b64(tc.source)},
					{Name: "nested/sibling.m", DataB64: b64("function result = sibling (value)\nresult = value + 1;\nendfunction\n")},
				},
				Stdin: "-4 16\n", ExpectedStdout: tc.stdout,
				Limits: model.Limits{TimeMs: 20000, MemoryMB: 1280},
			}
			if tc.capture {
				request.SidecarOutputs = []model.OutputFile{{Path: "__img__/images.jsonl"}}
			}
			var diagnostics strings.Builder
			result := New().Run(context.Background(), request, Hooks{OnLog: func(stream, message string) {
				if stream == "stderr" {
					diagnostics.WriteString(message)
				}
			}})
			// Accepted means the runner compared stdout against the exact expected
			// bytes; successful response bodies deliberately omit stdout.
			if result.Status != tc.status {
				t.Fatalf("status=%s stdout=%q reason=%q stderr=%q", result.Status, result.Stdout, result.Reason, result.Stderr)
			}
			if !strings.Contains(result.Stderr, tc.diagnostic) {
				t.Fatalf("stderr=%q, want original script diagnostic %q", result.Stderr, tc.diagnostic)
			}
			images := 0
			for _, sidecar := range result.SidecarOutputs {
				data, err := base64.StdEncoding.DecodeString(sidecar.DataB64)
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					if line == "" {
						continue
					}
					var payload struct{ Mime, B64 string }
					if err := json.Unmarshal([]byte(line), &payload); err != nil || payload.Mime != "image/png" {
						t.Fatalf("invalid PNG payload: %v", err)
					}
					raw, err := base64.StdEncoding.DecodeString(payload.B64)
					if err != nil {
						t.Fatal(err)
					}
					config, err := png.DecodeConfig(bytes.NewReader(raw))
					if err != nil || config.Width != 1280 || config.Height != 720 {
						t.Fatalf("invalid plot dimensions %+v: %v", config, err)
					}
					images++
				}
			}
			if images != tc.images {
				t.Fatalf("captured %d images, want %d; stderr=%q", images, tc.images, diagnostics.String())
			}
		})
	}
}
