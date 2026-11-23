package durante

import "testing"

func TestJvmFamilySelectionAndIsolation(t *testing.T) {
	cfg, err := ParseConfig("jvm", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"JAVA8", "JAVA25", "KOTLIN_JVM21", "SCALA", "CLOJURE", "GROOVY"} {
		if family, ok := FamilyForLanguage(language); !ok || family != "jvm" {
			t.Errorf("FamilyForLanguage(%q) = %q, %v", language, family, ok)
		}
	}
	for _, language := range []string{"JAVA17", "JAVA25"} {
		if _, err := cfg.Family(language); err == nil {
			t.Fatalf("known incompatible JVM %s admitted", language)
		}
	}
	for _, language := range []string{"CPP20", "binary", "unknown-durante-language", "ROCq", "WHY3", "PYTHON3", "PYPY3", "RUST2024"} {
		if family, err := cfg.Family(language); err == nil {
			t.Errorf("unexpected jvm admission of %q as %q", language, family)
		}
	}
	disabled, err := ParseConfig("", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Family("JAVA8"); err == nil {
		t.Fatal("registered family was enabled without configuration")
	}
	cfg.DispatchProfile = DispatchGLIBC241
	if _, err := cfg.Family("JAVA8"); err == nil {
		t.Fatal("C/C++-qualified libc dispatch profile admitted another runtime family")
	}
}
