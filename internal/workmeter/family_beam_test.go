package workmeter

import "testing"

func TestBeamFamilySelectionAndIsolation(t *testing.T) {
	cfg, err := ParseConfig("beam", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"ERLANG", "ELIXIR", "GLEAM"} {
		if family, err := cfg.Family(language); err != nil || family != "beam" {
			t.Errorf("Family(%q) = %q, %v", language, family, err)
		}
	}
	for _, language := range []string{"CPP20", "binary", "unknown-workmeter-language", "ROCq", "WHY3", "PYTHON3", "PYPY3", "RUST2024"} {
		if family, err := cfg.Family(language); err == nil {
			t.Errorf("unexpected beam admission of %q as %q", language, family)
		}
	}
	disabled, err := ParseConfig("", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Family("ERLANG"); err == nil {
		t.Fatal("registered family was enabled without configuration")
	}
	cfg.DispatchProfile = DispatchGLIBC241
	if _, err := cfg.Family("ERLANG"); err == nil {
		t.Fatal("C/C++-qualified libc dispatch profile admitted another runtime family")
	}
}
