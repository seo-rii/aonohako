package runtimepacks

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSmokeScriptKeepsProductionWorkRootPermissions(t *testing.T) {
	for _, throughEntrypoint := range []bool{false, true} {
		name := "new-root"
		if throughEntrypoint {
			name = "cloudrun-entrypoint"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			workRoot := filepath.Join(root, "work")
			binDir := filepath.Join(root, "bin")
			if err := os.Mkdir(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			selftest := `#!/bin/sh
set -eu
case "$1" in
  compile-execute|two-step|language-security|runtime-memory)
    test "$AONOHAKO_EXECUTION_MODE" = local-root
    mode=$(stat -c %a "$AONOHAKO_WORK_ROOT")
    test "$mode" = 711
    printf '%s:%s\n' "$1" "$mode"
    ;;
esac
`
			if err := os.WriteFile(filepath.Join(binDir, "aonohako-selftest"), []byte(selftest), 0o755); err != nil {
				t.Fatal(err)
			}
			smokePath := filepath.Join("..", "..", "scripts", "smoke_runtime.sh")
			cmd := exec.Command("/bin/bash", smokePath)
			if throughEntrypoint {
				if err := os.Mkdir(workRoot, 0o755); err != nil {
					t.Fatal(err)
				}
				cmd = exec.Command("/bin/sh", filepath.Join("..", "..", "scripts", "runtime_entrypoint.sh"), "/bin/bash", smokePath)
			}
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+":"+os.Getenv("PATH"),
				"AONOHAKO_DEPLOYMENT_TARGET=cloudrun",
				"AONOHAKO_WORK_ROOT="+workRoot,
				"AONOHAKO_SMOKE_WORK_ROOT="+workRoot,
				"AONOHAKO_SMOKE_COMMAND=",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("production smoke failed: %v\n%s", err, out)
			}
			want := "compile-execute:711\ntwo-step:711\nlanguage-security:711\nruntime-memory:711\n"
			if string(out) != want {
				t.Fatalf("smoke permissions = %q, want %q", out, want)
			}
		})
	}
}
