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

// TestValidateOptionalModeRequiresCanonicalValue pins the contract the API
// layer relies on: an empty mode is the (default) accepted no-op, the exact
// canonical values pass, but non-canonical variants that ParseMode would happily
// normalize (uppercase, whitespace-padded) are rejected, because the wire value
// is stored verbatim and compared for equality downstream.
func TestValidateOptionalModeRequiresCanonicalValue(t *testing.T) {
	for _, mode := range []Mode{"", ModeStdlib, ModeInstalled} {
		if err := ValidateOptionalMode(mode); err != nil {
			t.Fatalf("ValidateOptionalMode(%q) = %v, want nil", mode, err)
		}
	}
	for _, mode := range []Mode{"STDLIB", " installed ", "Installed", "vendor", "all"} {
		if err := ValidateOptionalMode(mode); err == nil {
			t.Fatalf("ValidateOptionalMode(%q) unexpectedly succeeded", mode)
		}
	}
}

func TestEffectiveModeDefaultsToStdlib(t *testing.T) {
	for _, mode := range []Mode{"", ModeStdlib, "unknown", "STDLIB"} {
		if got := EffectiveMode(mode); got != ModeStdlib {
			t.Fatalf("EffectiveMode(%q) = %q, want stdlib", mode, got)
		}
	}
	if got := EffectiveMode(ModeInstalled); got != ModeInstalled {
		t.Fatalf("EffectiveMode(installed) = %q, want installed", got)
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
