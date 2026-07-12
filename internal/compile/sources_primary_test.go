package compile

import (
	"path/filepath"
	"testing"

	"aonohako/internal/model"
)

func TestSelectPrimarySourceKeepsPreferredSourceAheadOfLaterHelper(t *testing.T) {
	workDir := t.TempDir()
	sources := []model.Source{
		{Name: "Main.nim"},
		{Name: "A.nim"},
	}

	got := selectPrimarySource(workDir, sources, []string{".nim"}, "Main.nim")
	want := filepath.Join(workDir, "Main.nim")
	if got != want {
		t.Fatalf("selectPrimarySource() = %q, want preferred source %q", got, want)
	}
}

func TestSelectPrimarySourceUsesLexicalOrderWithoutPreferredSource(t *testing.T) {
	workDir := t.TempDir()
	sources := []model.Source{
		{Name: "Z.nim"},
		{Name: "A.nim"},
	}

	got := selectPrimarySource(workDir, sources, []string{".nim"}, "Main.nim")
	want := filepath.Join(workDir, "A.nim")
	if got != want {
		t.Fatalf("selectPrimarySource() = %q, want lexical source %q", got, want)
	}
}
