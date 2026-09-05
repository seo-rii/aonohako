package util

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBase64RoundTrip(t *testing.T) {
	for _, payload := range [][]byte{
		nil,
		[]byte(""),
		[]byte("hello"),
		{0x00, 0x01, 0xff, 0xfe, 0x80},
	} {
		encoded := EncodeB64(payload)
		decoded, err := DecodeB64(encoded)
		if err != nil {
			t.Fatalf("DecodeB64(EncodeB64(%v)): %v", payload, err)
		}
		if !bytes.Equal(decoded, payload) {
			t.Fatalf("round trip = %v, want %v", decoded, payload)
		}
	}
}

func TestDecodeB64RejectsMalformedInput(t *testing.T) {
	if _, err := DecodeB64("not*base*64!"); err == nil {
		t.Fatal("DecodeB64 accepted malformed base64")
	}
}

func TestMaterializeBase64FilesWritesDecodedContentAndMode(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"main.py":       EncodeB64([]byte("print('hi')\n")),
		"pkg/helper.py": EncodeB64([]byte("X = 1\n")),
		"data/blob.bin": EncodeB64([]byte{0x00, 0xff, 0x10}),
	}
	if err := MaterializeBase64Files(root, files, 0o640); err != nil {
		t.Fatalf("MaterializeBase64Files: %v", err)
	}

	for name, b64 := range files {
		want, _ := DecodeB64(b64)
		dest := filepath.Join(root, filepath.FromSlash(name))
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s content = %q, want %q", name, got, want)
		}
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("%s mode = %o, want 640", name, info.Mode().Perm())
		}
	}
}

// TestMaterializeBase64FilesRejectsPathTraversal ensures a malicious file map
// cannot escape the workspace root, and that a rejected entry never lands on
// disk outside root.
func TestMaterializeBase64FilesRejectsPathTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	parent := filepath.Dir(root)

	for _, name := range []string{
		"../escape.txt",
		"sub/../../escape.txt",
		"/etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			err := MaterializeBase64Files(root, map[string]string{
				name: EncodeB64([]byte("owned")),
			}, 0o644)
			if err == nil {
				t.Fatalf("MaterializeBase64Files(%q) unexpectedly succeeded", name)
			}
			if _, statErr := os.Stat(filepath.Join(parent, "escape.txt")); statErr == nil {
				t.Fatalf("traversal wrote a file outside root for %q", name)
			}
		})
	}
}

// TestMaterializeBase64FilesRejectsInvalidBase64 surfaces a decode failure by
// name rather than writing a corrupt file.
func TestMaterializeBase64FilesRejectsInvalidBase64(t *testing.T) {
	root := t.TempDir()
	err := MaterializeBase64Files(root, map[string]string{
		"main.py": "@@not-base64@@",
	}, 0o644)
	if err == nil {
		t.Fatal("MaterializeBase64Files accepted invalid base64")
	}
	if _, statErr := os.Stat(filepath.Join(root, "main.py")); statErr == nil {
		t.Fatal("invalid base64 left a file on disk")
	}
}
