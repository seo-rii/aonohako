package workspacequota

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScanCountsBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatalf("mkdir dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "b.txt"), []byte("de"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	usage, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if usage.Bytes != 5 {
		t.Fatalf("workspace bytes = %d, want 5", usage.Bytes)
	}
}

func TestScanRejectsTooManyEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i <= MaxEntries; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%05d.txt", i)), nil, 0o644); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}

	if _, err := Scan(root); !errors.Is(err, ErrEntryLimitExceeded) {
		t.Fatalf("expected entry limit error, got %v", err)
	}
}

func TestScanRejectsTooMuchDepth(t *testing.T) {
	root := t.TempDir()
	dir := root
	for i := 0; i < MaxDepth+1; i++ {
		dir = filepath.Join(dir, "d")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir deep dir: %v", err)
	}

	if _, err := Scan(root); !errors.Is(err, ErrDepthExceeded) {
		t.Fatalf("expected depth error, got %v", err)
	}
}

func TestScanReturnsUnreadableDirectoryErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can traverse unreadable directories")
	}

	root := t.TempDir()
	hidden := filepath.Join(root, "hidden")
	if err := os.Mkdir(hidden, 0o700); err != nil {
		t.Fatalf("mkdir hidden: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "payload.bin"), []byte("hidden"), 0o600); err != nil {
		t.Fatalf("write hidden payload: %v", err)
	}
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatalf("chmod hidden: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(hidden, 0o700)
	})

	if _, err := Scan(root); err == nil {
		t.Fatalf("expected unreadable directory to make workspace scan fail")
	}
}
