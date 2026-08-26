package gomodulepolicy

import "testing"

func TestParseMode(t *testing.T) {
	for _, raw := range []string{"stdlib", "STDLIB", " installed "} {
		if _, err := ParseMode(raw); err != nil {
			t.Fatalf("ParseMode(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "all", "vendor"} {
		if _, err := ParseMode(raw); err == nil {
			t.Fatalf("ParseMode(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestUsesGoCompiler(t *testing.T) {
	for _, lang := range []string{"GO", "go", "golang", " Go "} {
		if !UsesGoCompiler(lang) {
			t.Fatalf("UsesGoCompiler(%q) = false", lang)
		}
	}
	for _, lang := range []string{"", "C11", "go-binary"} {
		if UsesGoCompiler(lang) {
			t.Fatalf("UsesGoCompiler(%q) = true", lang)
		}
	}
}
