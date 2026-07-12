package execute

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGDLRunnerValidatesCommandStreamInputs(t *testing.T) {
	binDir := t.TempDir()
	fakeGDL := filepath.Join(binDir, "gdl")
	if err := os.WriteFile(fakeGDL, []byte("#!/usr/bin/env bash\nset -euo pipefail\n[ \"$#\" -eq 1 ]\n[ \"$1\" = -quiet ]\nprintf invoked > \"${GDL_TEST_MARKER}\"\ncat\n"), 0o755); err != nil {
		t.Fatalf("WriteFile fake gdl: %v", err)
	}
	script := filepath.Join("..", "..", "scripts", "gdl_run.sh")
	tests := []struct {
		name       string
		source     string
		entry      string
		wantOutput string
		wantError  string
	}{
		{
			name:       "valid",
			source:     "Main.pro",
			entry:      "solve_2$",
			wantOutput: ".compile Main.pro\nsolve_2$\nexit\n",
		},
		{
			name:      "source newline",
			source:    "dummy\nexit\nMain.pro",
			entry:     "main",
			wantError: "invalid GDL source path",
		},
		{
			name:      "source command separators",
			source:    "dummy.pro & exit & Main.pro",
			entry:     "main",
			wantError: "invalid GDL source path",
		},
		{
			name:      "entry newline",
			source:    "Main.pro",
			entry:     "main\nexit",
			wantError: "invalid GDL entrypoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "invoked")
			cmd := exec.Command("bash", script, tt.source, tt.entry)
			cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "GDL_TEST_MARKER="+marker)
			out, err := cmd.CombinedOutput()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("gdl_run.sh: %v\n%s", err, out)
				}
				if string(out) != tt.wantOutput {
					t.Fatalf("command stream = %q, want %q", out, tt.wantOutput)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("fake gdl was not invoked: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(string(out), tt.wantError) {
				t.Fatalf("gdl_run.sh error = %v output=%q, want %q", err, out, tt.wantError)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("invalid command stream reached gdl: %v", err)
			}
		})
	}
}
