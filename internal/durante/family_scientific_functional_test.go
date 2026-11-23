package durante

import "testing"

func TestScientificFunctionalFamilySelectionAndIsolation(t *testing.T) {
	cfg, err := ParseConfig("scientific-functional", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"JULIA", "R", "OCTAVE", "GDL", "SQLITE", "DUCKDB", "BQN", "RACKET", "SMLNJ", "LISP", "RAKU", "HAXE"} {
		if family, err := cfg.Family(language); err != nil || family != "scientific-functional" {
			t.Errorf("Family(%q) = %q, %v", language, family, err)
		}
	}
	for _, language := range []string{"CPP20", "binary", "unknown-durante-language", "ROCq", "WHY3", "PYTHON3", "PYPY3", "RUST2024"} {
		if family, err := cfg.Family(language); err == nil {
			t.Errorf("unexpected scientific-functional admission of %q as %q", language, family)
		}
	}
	disabled, err := ParseConfig("", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Family("JULIA"); err == nil {
		t.Fatal("registered family was enabled without configuration")
	}
	cfg.DispatchProfile = DispatchGLIBC241
	if _, err := cfg.Family("JULIA"); err == nil {
		t.Fatal("C/C++-qualified libc dispatch profile admitted another runtime family")
	}
}
