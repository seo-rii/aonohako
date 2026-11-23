package durante

import "testing"

func TestScriptingFamilySelectionAndIsolation(t *testing.T) {
	cfg, err := ParseConfig("scripting", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"BASH", "POSIX_SH", "FISH", "RUBY", "PHP8", "FENNEL", "LUAJIT", "PERL", "AWK", "TCL", "SED", "BC"} {
		if family, err := cfg.Family(language); err != nil || family != "scripting" {
			t.Errorf("Family(%q) = %q, %v", language, family, err)
		}
	}
	for _, language := range []string{"CPP20", "binary", "unknown-durante-language", "ROCq", "WHY3", "PYTHON3", "PYPY3", "RUST2024"} {
		if family, err := cfg.Family(language); err == nil {
			t.Errorf("unexpected scripting admission of %q as %q", language, family)
		}
	}
	disabled, err := ParseConfig("", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Family("BASH"); err == nil {
		t.Fatal("registered family was enabled without configuration")
	}
	cfg.DispatchProfile = DispatchGLIBC241
	if _, err := cfg.Family("BASH"); err == nil {
		t.Fatal("C/C++-qualified libc dispatch profile admitted another runtime family")
	}
}
