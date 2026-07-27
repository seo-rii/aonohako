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

func TestEffectiveLibraryModeDefaultsToStdlib(t *testing.T) {
	if got := EffectiveLibraryMode(""); got != LibraryModeStdlib {
		t.Fatalf("empty effective mode = %q, want stdlib", got)
	}
	if got := EffectiveLibraryMode(LibraryModeInstalled); got != LibraryModeInstalled {
		t.Fatalf("installed effective mode = %q, want installed", got)
	}
}
