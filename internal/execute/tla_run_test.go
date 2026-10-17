package execute

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTLARunnerPreservesOutcome(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "tla_run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		code   int
		stdout string
		stderr string
		config bool
	}{
		{name: "valid model", stdout: "Model checking completed. No error has been found.\n", config: true},
		{name: "false invariant", code: 12, stdout: "Error: Invariant Safe is violated.\n", config: true},
		{name: "missing configuration", code: 150, stdout: "Error: TLC requires a configuration file.\n"},
		{name: "JVM failure", code: 1, stderr: "Could not create the Java Virtual Machine.\n", config: true},
		{name: "large counterexample", code: 12, stdout: strings.Repeat("x", 65536) + "must be truncated", stderr: "JVM warning\n", config: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			workDir := filepath.Join(root, "work with spaces")
			tempDir := filepath.Join(root, "temporary output")
			for _, dir := range []string{binDir, workDir, tempDir} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			fakeJava := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\0' "$@" > "$TLA_TEST_ARGS"
printf '%s' "$TLA_TEST_STDOUT"
printf '%s' "$TLA_TEST_STDERR" >&2
exit "$TLA_TEST_EXIT"
`
			if err := os.WriteFile(filepath.Join(binDir, "java"), []byte(fakeJava), 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(workDir, "Main.tla")
			if err := os.WriteFile(source, []byte("---- MODULE Main ----\n====\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			config := strings.TrimSuffix(source, ".tla") + ".cfg"
			if tc.config {
				if err := os.WriteFile(config, []byte("SPECIFICATION Spec\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			argsPath := filepath.Join(root, "java-arguments")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", script, source)
			cmd.Dir = workDir
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+":"+os.Getenv("PATH"),
				"TMPDIR="+tempDir,
				"TLA_TEST_ARGS="+argsPath,
				"TLA_TEST_STDOUT="+tc.stdout,
				"TLA_TEST_STDERR="+tc.stderr,
				"TLA_TEST_EXIT="+strconv.Itoa(tc.code),
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("TLA runner timed out: %v", ctx.Err())
			}
			gotCode := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run TLA wrapper: %v", err)
				}
				gotCode = exitErr.ExitCode()
			}
			if gotCode != tc.code {
				t.Errorf("wrapper exit = %d, want TLC exit %d", gotCode, tc.code)
			}
			if stdout.Len() != 0 {
				t.Errorf("model-checker diagnostics leaked to solution stdout: %q", stdout.String())
			}
			wantStderr := tc.stderr
			if tc.code != 0 {
				wantStderr += tc.stdout[:min(len(tc.stdout), 65536)]
			}
			if got := stderr.String(); got != wantStderr {
				t.Errorf("stderr has %d bytes, want %d bytes with failure diagnostics", len(got), len(wantStderr))
			}
			argsRaw, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(argsRaw), "\x00"), "\x00")
			wantArgs := []string{"-workers", "1", "-deadlock"}
			if tc.config {
				wantArgs = append(wantArgs, "-config", config)
			}
			wantArgs = append(wantArgs, source)
			if len(args) < len(wantArgs) || !reflect.DeepEqual(args[len(args)-len(wantArgs):], wantArgs) {
				t.Errorf("TLC arguments = %q, want suffix %q", args, wantArgs)
			}
			files, err := os.ReadDir(tempDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 0 {
				t.Errorf("wrapper left %d captured-output files after exit", len(files))
			}
		})
	}
}
