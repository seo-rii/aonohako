package workmeter

import "testing"

func TestPypyFamilySelectionAndIsolation(t *testing.T) {
	cfg, err := ParseConfig("pypy", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"PYPY3", "pypy", " pypy3 "} {
		if family, err := cfg.Family(language); err != nil || family != "pypy" {
			t.Errorf("Family(%q) = %q, %v", language, family, err)
		}
	}
	for _, language := range []string{"CPP20", "binary", "unknown-workmeter-language", "ROCq", "WHY3", "PYTHON3", "RUST2024"} {
		if family, err := cfg.Family(language); err == nil {
			t.Errorf("unexpected pypy admission of %q as %q", language, family)
		}
	}
	disabled, err := ParseConfig("", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Family("PYPY3"); err == nil {
		t.Fatal("registered family was enabled without configuration")
	}
	cfg.DispatchProfile = DispatchGLIBC241
	if _, err := cfg.Family("PYPY3"); err == nil {
		t.Fatal("C/C++-qualified libc dispatch profile admitted another runtime family")
	}
}
