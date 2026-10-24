package runtimepacks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOctaveCapturePreservesSubmissionSidecar(t *testing.T) {
	for _, program := range []string{"octave-cli", "gnuplot", "gs"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skipf("Octave capture dependency %s is not installed", program)
		}
	}
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	_, script, ok := strings.Cut(strings.Join(catalog.Languages["octave"].Install.Script, "\n"), "<<'OCTAVE'\n")
	if !ok {
		t.Fatal("Octave capture helper heredoc is missing")
	}
	script, _, ok = strings.Cut(script, "\nOCTAVE")
	if !ok {
		t.Fatal("Octave capture helper heredoc is not terminated")
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewGray(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(map[string]any{
		"mime": "image/png", "b64": base64.StdEncoding.EncodeToString(pngData.Bytes()), "ts": 123,
	})
	if err != nil {
		t.Fatal(err)
	}
	record = append(record, '\n')
	const byteBudget = 8 << 20
	for _, tc := range []struct {
		name              string
		existing          []byte
		figures, appended int
	}{
		{name: "no figures and no sidecar"},
		{name: "no figures preserve unterminated record", existing: bytes.TrimSuffix(record, []byte("\n"))},
		{name: "append after existing event", existing: record, figures: 2, appended: 2},
		{name: "append after unterminated event", existing: bytes.TrimSuffix(record, []byte("\n")), figures: 1, appended: 1},
		{name: "remaining event budget", existing: bytes.Repeat(record, 19), figures: 2, appended: 1},
		{name: "exhausted event budget", existing: bytes.Repeat(record, 20), figures: 1},
		{name: "exhausted byte budget", existing: append(bytes.Clone(record), bytes.Repeat([]byte(" "), byteBudget-len(record))...), figures: 1},
		{name: "insufficient byte budget", existing: append(bytes.Clone(record), bytes.Repeat([]byte(" "), byteBudget-len(record)-1)...), figures: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			outputDir := filepath.Join(workDir, "images")
			if err := os.Mkdir(outputDir, 0o755); err != nil {
				t.Fatal(err)
			}
			helperPath := filepath.Join(workDir, "capture.m")
			if err := os.WriteFile(helperPath, []byte(script), 0o644); err != nil {
				t.Fatal(err)
			}
			// The submission writes its own sidecar before the helper captures
			// figures. Its bytes may already have been consumed by the streamer.
			source := ""
			if tc.existing != nil {
				if err := os.WriteFile(filepath.Join(workDir, "seed.jsonl"), tc.existing, 0o644); err != nil {
					t.Fatal(err)
				}
				source = `copyfile ("seed.jsonl", fullfile (getenv ("IMG_OUT_DIR"), "images.jsonl"));` + "\n"
			}
			source += fmt.Sprintf("for index = 1:%d\nfigure (index); plot (1:3, [1 4 9]);\nendfor\ndisp (\"ok\");\n", tc.figures)
			entryPath := filepath.Join(workDir, "entry.m")
			if err := os.WriteFile(entryPath, []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
			const sentinel = "submission-owned image bytes"
			imagePath := filepath.Join(outputDir, "figure-1.png")
			if err := os.WriteFile(imagePath, []byte(sentinel), 0o644); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "octave-cli", "--quiet", "--no-gui", "--no-history", "--no-init-file", "--no-site-file", helperPath)
			cmd.Dir = workDir
			cmd.Env = append(os.Environ(), "IMG_CAPTURE=1", "IMG_OUT_DIR="+outputDir, "AONOHAKO_OCTAVE_ENTRY="+base64.StdEncoding.EncodeToString([]byte(entryPath)))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("Octave capture: %v; stderr=%s", err, &stderr)
			}
			if stdout.String() != "ok\n" {
				t.Fatalf("stdout=%q, want submission output only", &stdout)
			}
			data, err := os.ReadFile(filepath.Join(outputDir, "images.jsonl"))
			if tc.existing == nil && tc.appended == 0 {
				if !os.IsNotExist(err) {
					t.Fatalf("empty capture created a sidecar: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(data, tc.existing) || len(data) > byteBudget {
				t.Fatal("capture rewrote existing bytes or exceeded the stream budget")
			}
			tail := data[len(tc.existing):]
			if tc.appended == 0 && len(tail) != 0 {
				t.Fatal("capture changed a sidecar with no available figures or budget")
			}
			lines := strings.FieldsFunc(string(tail), func(r rune) bool { return r == '\n' })
			if len(lines) != tc.appended {
				t.Fatalf("appended %d events, want %d; stderr=%s", len(lines), tc.appended, &stderr)
			}
			if tc.appended > 0 && len(tc.existing) > 0 && tc.existing[len(tc.existing)-1] != '\n' && tail[0] != '\n' {
				t.Fatal("capture joined the first appended event to an unterminated record")
			}
			for _, line := range lines {
				var event struct{ Mime, B64 string }
				if err := json.Unmarshal([]byte(line), &event); err != nil || event.Mime != "image/png" || len(line)+1 > 1<<20 {
					t.Fatalf("invalid appended PNG event: %v", err)
				}
				raw, err := base64.StdEncoding.DecodeString(event.B64)
				if err != nil {
					t.Fatal(err)
				}
				config, err := png.DecodeConfig(bytes.NewReader(raw))
				if err != nil || config.Width != 1280 || config.Height != 720 {
					t.Fatalf("invalid appended plot dimensions %+v: %v", config, err)
				}
			}
			if data, err := os.ReadFile(imagePath); err != nil || string(data) != sentinel {
				t.Fatalf("capture replaced or removed a submission image: %v", err)
			}
			entries, err := os.ReadDir(outputDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "octave-capture-") {
					t.Fatalf("capture left temporary output %q", entry.Name())
				}
			}
		})
	}
}
