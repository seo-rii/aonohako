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

// TestValidateOptionalCrateModeRequiresCanonicalValue mirrors the API contract:
// empty is the accepted default, canonical values pass, and non-canonical
// variants ParseCrateMode would normalize are rejected because the request
// value is stored and compared verbatim downstream.
func TestValidateOptionalCrateModeRequiresCanonicalValue(t *testing.T) {
	for _, mode := range []CrateMode{"", CrateModeStdlib, CrateModeInstalled} {
		if err := ValidateOptionalCrateMode(mode); err != nil {
			t.Fatalf("ValidateOptionalCrateMode(%q) = %v, want nil", mode, err)
		}
	}
	for _, mode := range []CrateMode{"STDLIB", " installed ", "Installed", "vendor", "all"} {
		if err := ValidateOptionalCrateMode(mode); err == nil {
			t.Fatalf("ValidateOptionalCrateMode(%q) unexpectedly succeeded", mode)
		}
	}
}

func TestEffectiveCrateModeDefaultsToStdlib(t *testing.T) {
	for _, mode := range []CrateMode{"", CrateModeStdlib, "unknown", "INSTALLED"} {
		if got := EffectiveCrateMode(mode); got != CrateModeStdlib {
			t.Fatalf("EffectiveCrateMode(%q) = %q, want stdlib", mode, got)
		}
	}
	if got := EffectiveCrateMode(CrateModeInstalled); got != CrateModeInstalled {
		t.Fatalf("EffectiveCrateMode(installed) = %q, want installed", got)
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
