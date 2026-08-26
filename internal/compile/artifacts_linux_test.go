//go:build linux

package compile

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSnapshotCargoArtifactAcceptsHardLinkedSource(t *testing.T) {
	root := t.TempDir()
	hashedArtifact := filepath.Join(root, ".cargo-target", "release", "deps", "submission-deadbeef")
	publicArtifact := filepath.Join(root, ".cargo-target", "release", "submission")
	if err := os.MkdirAll(filepath.Dir(hashedArtifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hashedArtifact, []byte("trusted snapshot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hashedArtifact, publicArtifact); err != nil {
		t.Fatal(err)
	}
	publicInfo, err := os.Stat(publicArtifact)
	if err != nil {
		t.Fatal(err)
	}
	publicStat, ok := publicInfo.Sys().(*syscall.Stat_t)
	if !ok || publicStat.Nlink < 2 {
		t.Fatalf("Cargo source link count = %v, want at least 2", publicStat)
	}

	snapshot, err := snapshotCargoArtifact(root, filepath.Join(".cargo-target", "release", "submission"))
	if err != nil {
		t.Fatalf("snapshotCargoArtifact(): %v", err)
	}
	defer snapshot.cleanup()
	stat, ok := snapshot.info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		t.Fatalf("snapshot link count = %v, want 1", stat)
	}
	if snapshot.info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("snapshot permissions = %o, want private", snapshot.info.Mode().Perm())
	}
	if err := os.Remove(publicArtifact); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicArtifact, []byte("attacker replacement"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(snapshot.file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "trusted snapshot" {
		t.Fatalf("snapshot data = %q", data)
	}
}

func TestSnapshotCargoArtifactRejectsSymlinkPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "final component",
			setup: func(t *testing.T, root string) {
				t.Helper()
				releaseDir := filepath.Join(root, ".cargo-target", "release")
				if err := os.MkdirAll(releaseDir, 0o755); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte("attacker controlled"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(releaseDir, "submission")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ancestor component",
			setup: func(t *testing.T, root string) {
				t.Helper()
				outsideTarget := filepath.Join(t.TempDir(), "target")
				if err := os.MkdirAll(filepath.Join(outsideTarget, "release"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(outsideTarget, "release", "submission"), []byte("attacker controlled"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsideTarget, filepath.Join(root, ".cargo-target")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)
			if snapshot, err := snapshotCargoArtifact(root, filepath.Join(".cargo-target", "release", "submission")); err == nil {
				snapshot.cleanup()
				t.Fatal("snapshotCargoArtifact() accepted a symlink path")
			}
		})
	}
}
