package execute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/model"
)

func TestRequiredFileOutputNeverFallsBackToStdout(t *testing.T) {
	for _, ignoreTLE := range []bool{false, true} {
		for _, kind := range []string{"missing", "oversized", "unreadable", "read-failed", "correct", "wrong", "empty"} {
			t.Run(fmt.Sprintf("%s/ignore_tle=%t", kind, ignoreTLE), func(t *testing.T) {
				ws, err := prepareWorkspaceDirs(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				expected := "42\n"
				path := filepath.Join(ws.BoxDir, "answer")
				switch kind {
				case "oversized":
					file, err := os.Create(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := file.Truncate(maxCapturedFileBytes + 1); err != nil {
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				case "unreadable":
					// A directory cannot supply the required regular-file bytes,
					// even when the collector has permission to open the path.
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				case "correct", "wrong", "empty":
					contents := expected
					if kind == "wrong" {
						contents = "wrong\n"
					} else if kind == "empty" {
						contents, expected = "", ""
					}
					if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				req := &model.RunRequest{
					ExpectedStdout: expected,
					FileOutputs:    []model.OutputFile{{Path: "answer"}},
					IgnoreTLE:      ignoreTLE,
				}
				res := execResult{
					Status:        model.RunStatusTLE,
					Reason:        "cpu time limit exceeded",
					VerdictSource: "cpu_time_cgroup",
					Stdout:        []byte(expected),
				}
				output, source := res.Stdout, "stdout"
				captured, captureErr := captureFileOutput(ws, req.FileOutputs[0])
				if kind == "read-failed" {
					// Exercise a reader failure after opening: directory seeks
					// succeed, but reading bytes from a directory fails. The same
					// reader is shared by file and sidecar collection.
					file, err := os.Open(ws.BoxDir)
					if err != nil {
						t.Fatal(err)
					}
					cleaned := false
					captured, captureErr = readCapturedOutput(capturedOutputFile{
						file: file,
						cleanup: func() {
							cleaned = true
							_ = file.Close()
						},
					})
					if !cleaned {
						t.Fatal("failed read did not release its file")
					}
				}
				if captureErr == nil {
					output, source = captured, "file_output"
				}
				wantAvailable := kind == "correct" || kind == "wrong" || kind == "empty"
				if (captureErr == nil) != wantAvailable {
					t.Fatalf("capture error = %v, want available %t", captureErr, wantAvailable)
				}
				status, score, reason, verdictSource := evaluateRunStatus(context.Background(), ws, req, res, output, source, "", nil, config.DefaultRuntimeTuningConfig(), "")
				if status != res.Status || reason != res.Reason || verdictSource != res.VerdictSource {
					t.Fatalf("capture changed original TLE: (%q, %q, %q)", status, reason, verdictSource)
				}
				if !ignoreTLE {
					if score != nil {
						t.Fatalf("score = %v without ignore_tle", *score)
					}
					return
				}
				wantScore := 0.0
				if kind == "correct" || kind == "empty" {
					wantScore = 1
				}
				if score == nil || *score != wantScore {
					t.Fatalf("score = %v, want %v", score, wantScore)
				}
			})
		}
	}
}

func TestUnavailableRequiredFileOutputDoesNotStartSPJ(t *testing.T) {
	ws, err := prepareWorkspaceDirs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// runSPJ removes its workspace on exit even if the checker cannot run. A
	// preexisting marker proves that unavailable output never invokes it.
	spjDir := filepath.Join(ws.RootDir, ".spj")
	if err := os.Mkdir(spjDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(spjDir, "not-invoked")
	if err := os.WriteFile(marker, []byte("not invoked"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := &model.RunRequest{
		ExpectedStdout: "42\n",
		FileOutputs:    []model.OutputFile{{Path: "answer"}},
		IgnoreTLE:      true,
		SPJ: &model.SPJSpec{
			Lang:   "binary",
			Binary: &model.Binary{Name: "checker", Mode: "exec", DataB64: b64("checker")},
		},
	}
	res := execResult{Status: model.RunStatusTLE, Reason: "time limit exceeded", VerdictSource: "wall_time"}
	status, score, reason, source := evaluateRunStatus(context.Background(), ws, req, res, []byte(req.ExpectedStdout), "stdout", "", nil, config.DefaultRuntimeTuningConfig(), "")
	if status != res.Status || score == nil || *score != 0 || reason != res.Reason || source != res.VerdictSource {
		t.Fatalf("unavailable checker input = (%q, %v, %q, %q)", status, score, reason, source)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "not invoked" {
		t.Fatalf("checker workspace was touched: %q, %v", data, err)
	}
}

func TestRunRequiredFileOutputIgnoredTLE(t *testing.T) {
	requireSandboxSupport(t)
	participant := buildCTestBinary(t, `
#include <stdio.h>
#include <sys/stat.h>
#include <unistd.h>

int main(void) {
    int mode;
    if (scanf("%d", &mode) != 1) return 2;
    if (mode == 1 || mode == 2 || mode == 3) {
        FILE *output = fopen("answer", "w");
        if (!output) return 3;
        if (mode == 1) fputs("42\n", output);
        if (mode == 2) fputs("wrong\n", output);
        if (mode == 3 && ftruncate(fileno(output), (8 << 20) + 1)) return 4;
        fclose(output);
    }
    if (mode == 4 && mkdir("answer", 0755)) return 5;
    puts("42");
    fflush(stdout);
    for (;;) __asm__ volatile("");
}
`)
	checker := buildCTestBinary(t, `
#include <stdio.h>
int main(void) {
    puts("1");
    return 0;
}
`)
	for _, tc := range []struct {
		name      string
		mode      int
		withSPJ   bool
		wantScore float64
	}{
		{name: "missing"},
		{name: "correct", mode: 1, wantScore: 1},
		{name: "wrong", mode: 2},
		{name: "oversized", mode: 3},
		{name: "unreadable", mode: 4},
		{name: "missing-with-spj", withSPJ: true},
	} {
		for _, ignoreTLE := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ignore_tle=%t", tc.name, ignoreTLE), func(t *testing.T) {
				req := &model.RunRequest{
					Lang:           "binary",
					Binaries:       []model.Binary{{Name: "runner", Mode: "exec", DataB64: participant}},
					Stdin:          fmt.Sprintf("%d\n", tc.mode),
					ExpectedStdout: "42\n",
					FileOutputs:    []model.OutputFile{{Path: "answer"}},
					IgnoreTLE:      ignoreTLE,
					Limits:         model.Limits{TimeMs: 100, MemoryMB: 64},
				}
				if tc.withSPJ {
					req.SPJ = &model.SPJSpec{
						Lang:      "binary",
						Binary:    &model.Binary{Name: "checker", Mode: "exec", DataB64: checker},
						EmitScore: true,
					}
				}
				response := New().Run(context.Background(), req, Hooks{})
				if response.Status != model.RunStatusTLE || response.VerdictSource == "file_output" || strings.Contains(response.Reason, "file output capture failed") {
					t.Fatalf("original execution timeout was overwritten: %+v", response)
				}
				if !ignoreTLE {
					if response.Score != nil {
						t.Fatalf("score = %v without ignore_tle", *response.Score)
					}
				} else if response.Score == nil || *response.Score != tc.wantScore {
					t.Fatalf("score = %v, want %v; response = %+v", response.Score, tc.wantScore, response)
				}
				if tc.wantScore == 0 && tc.mode != 2 && response.Stdout != "" {
					t.Fatalf("unavailable file output fell back to stdout: %q", response.Stdout)
				}
			})
		}
	}
}
