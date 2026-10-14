package execute

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestPreparedSPJInputTransfersWithoutCopy(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "judge-input")
	if err := os.WriteFile(source, []byte("trusted input\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(root, "spj")
	if err := os.Mkdir(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dest, err := writeStdinTempFile(context.Background(), targetDir, "input-*", &model.RunRequest{}, source)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("checker input was copied instead of transferred")
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("old fixture path still exists: %v", err)
	}
	if after.Mode().Perm() != 0o444 || filepath.Dir(dest) != targetDir {
		t.Fatalf("input not readable in clean checker workspace: %s %v", dest, after.Mode())
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "trusted input\n" {
		t.Fatalf("fixture changed: %q %v", data, err)
	}
}

func TestPreparedSPJInputRejectsUnsafeOrOversizedFixture(t *testing.T) {
	for _, kind := range []string{"symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "judge-input")
			var err error
			if kind == "symlink" {
				err = os.Symlink("missing", source)
			} else {
				err = os.WriteFile(source, []byte(strings.Repeat("x", 1025)), 0o400)
			}
			if err != nil {
				t.Fatal(err)
			}
			req := &model.RunRequest{Limits: model.Limits{WorkspaceBytes: 1024}}
			if _, err := writeStdinTempFile(context.Background(), root, "input-*", req, source); err == nil {
				t.Fatal("unsafe or oversized prepared input accepted")
			}
			if _, err := os.Lstat(source); err != nil {
				t.Fatalf("rejected input was moved: %v", err)
			}
		})
	}
}
