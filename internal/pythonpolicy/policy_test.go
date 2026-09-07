package pythonpolicy

import "testing"

func TestParseLibraryMode(t *testing.T) {
	tests := []struct {
		raw  string
		want LibraryMode
	}{
		{raw: "stdlib", want: LibraryModeStdlib},
		{raw: "installed", want: LibraryModeInstalled},
		{raw: " INSTALLED ", want: LibraryModeInstalled},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseLibraryMode(tc.raw)
			if err != nil {
				t.Fatalf("ParseLibraryMode(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ParseLibraryMode(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	for _, raw := range []string{"", "all", "true"} {
		if _, err := ParseLibraryMode(raw); err == nil {
			t.Fatalf("ParseLibraryMode(%q) unexpectedly succeeded", raw)
		}
	}
}

// TestValidateOptionalLibraryModeRequiresCanonicalValue pins the API contract:
// empty is the accepted default, canonical values pass, and non-canonical
// variants ParseLibraryMode would normalize are rejected because the request
// value is stored and compared verbatim downstream.
func TestValidateOptionalLibraryModeRequiresCanonicalValue(t *testing.T) {
	for _, mode := range []LibraryMode{"", LibraryModeStdlib, LibraryModeInstalled} {
		if err := ValidateOptionalLibraryMode(mode); err != nil {
			t.Fatalf("ValidateOptionalLibraryMode(%q) = %v, want nil", mode, err)
		}
	}
	for _, mode := range []LibraryMode{"STDLIB", " installed ", "Installed", "all", "true"} {
		if err := ValidateOptionalLibraryMode(mode); err == nil {
			t.Fatalf("ValidateOptionalLibraryMode(%q) unexpectedly succeeded", mode)
		}
	}
}

func TestEffectiveLibraryModeDefaultsToStdlib(t *testing.T) {
	for _, mode := range []LibraryMode{"", LibraryModeStdlib, "unknown", "INSTALLED"} {
		if got := EffectiveLibraryMode(mode); got != LibraryModeStdlib {
			t.Fatalf("EffectiveLibraryMode(%q) = %q, want stdlib", mode, got)
		}
	}
	if got := EffectiveLibraryMode(LibraryModeInstalled); got != LibraryModeInstalled {
		t.Fatalf("installed effective mode = %q, want installed", got)
	}
}
