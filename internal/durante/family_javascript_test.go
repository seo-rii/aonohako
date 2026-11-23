package durante

import "testing"

func TestJavascriptWasmFamilySelectionAndIsolation(t *testing.T) {
	cfg, err := ParseConfig("javascript-wasm", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"JAVASCRIPT", "TYPESCRIPT", "JAVASCRIPT_BUN", "DENO", "JAVASCRIPT_QUICKJS", "WASM", "ASSEMBLYSCRIPT", "ENTRY", "GNU_APL", "RESCRIPT"} {
		if family, err := cfg.Family(language); err != nil || family != "javascript-wasm" {
			t.Errorf("Family(%q) = %q, %v", language, family, err)
		}
	}
	for _, language := range []string{"CPP20", "binary", "unknown-durante-language", "ROCq", "WHY3", "PYTHON3", "PYPY3", "RUST2024"} {
		if family, err := cfg.Family(language); err == nil {
			t.Errorf("unexpected javascript-wasm admission of %q as %q", language, family)
		}
	}
	disabled, err := ParseConfig("", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Family("JAVASCRIPT"); err == nil {
		t.Fatal("registered family was enabled without configuration")
	}
	cfg.DispatchProfile = DispatchGLIBC241
	if _, err := cfg.Family("JAVASCRIPT"); err == nil {
		t.Fatal("C/C++-qualified libc dispatch profile admitted another runtime family")
	}
}
