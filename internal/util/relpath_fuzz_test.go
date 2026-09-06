package util

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidateRelativePath asserts the security-critical containment invariant
// of the workspace path sanitizer: any name it accepts, when joined onto a
// root, must stay inside that root. A counterexample is a path-traversal escape.
func FuzzValidateRelativePath(f *testing.F) {
	seeds := []string{
		"", ".", "..", "a", "a/b", "a/../b", "../b", "/etc/passwd",
		"a/./b", "a//b", "a/..", "..//..//x", "a/b/../../..", "  a  ",
		"a\\..\\b", "a\x00b", ".hidden/x", "a/..b/c",
		strings.Repeat("../", 40) + "x", "./../.", "a/b/c/../../../../d",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		clean, err := ValidateRelativePath(name)
		if err != nil {
			return // rejected inputs carry no guarantee
		}
		if clean == "" || clean == "." {
			t.Fatalf("accepted %q -> empty/dot clean %q", name, clean)
		}
		if filepath.IsAbs(clean) {
			t.Fatalf("accepted %q -> absolute clean %q", name, clean)
		}
		for _, seg := range strings.Split(clean, string(filepath.Separator)) {
			if seg == ".." {
				t.Fatalf("accepted %q -> clean %q has a .. segment", name, clean)
			}
		}
		const root = "/srv/box"
		joined := filepath.Join(root, clean)
		rel, relErr := filepath.Rel(root, joined)
		if relErr != nil {
			t.Fatalf("Rel(%q,%q): %v", root, joined, relErr)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("accepted %q escapes root: join=%q rel=%q", name, joined, rel)
		}
	})
}
