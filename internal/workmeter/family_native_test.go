package workmeter

import "testing"

func TestNativeFamilySelectionAndIsolation(t *testing.T) {
	cfg, err := ParseConfig("native", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"RUST2024", "GO", "go-binary", "CHAPEL", "chapel-binary", "PONY", "MOJO", "OCAML", "CHICKEN_SCHEME", "C3", "KOTLIN", "CYTHON"} {
		if family, err := cfg.Family(language); err != nil || family != "native" {
			t.Errorf("Family(%q) = %q, %v", language, family, err)
		}
	}
	for _, language := range []string{"CPP20", "binary", "unknown-workmeter-language", "ROCq", "WHY3", "PYTHON3", "PYPY3"} {
		if family, err := cfg.Family(language); err == nil {
			t.Errorf("unexpected native admission of %q as %q", language, family)
		}
	}
	disabled, err := ParseConfig("", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Family("RUST2024"); err == nil {
		t.Fatal("registered family was enabled without configuration")
	}
	cfg.DispatchProfile = DispatchGLIBC241
	if _, err := cfg.Family("RUST2024"); err == nil {
		t.Fatal("C/C++-qualified libc dispatch profile admitted another runtime family")
	}
}
