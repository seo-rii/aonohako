package rustpolicy

import "testing"

func TestParseCrateMode(t *testing.T) {
	for _, raw := range []string{"stdlib", "STDLIB", " installed "} {
		if _, err := ParseCrateMode(raw); err != nil {
			t.Fatalf("ParseCrateMode(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"", "all", "vendor"} {
		if _, err := ParseCrateMode(raw); err == nil {
			t.Fatalf("ParseCrateMode(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestIsRustLanguage(t *testing.T) {
	for _, lang := range []string{"rust", "RUST2015", "rust2018", "RUST2021", " rust2024 "} {
		if !IsRustLanguage(lang) {
			t.Fatalf("IsRustLanguage(%q) = false", lang)
		}
	}
	for _, lang := range []string{"", "C11", "rust-script"} {
		if IsRustLanguage(lang) {
			t.Fatalf("IsRustLanguage(%q) = true", lang)
		}
	}
}
