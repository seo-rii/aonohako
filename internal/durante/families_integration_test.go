package durante

import (
	"reflect"
	"testing"
)

func TestConsolidatedFamiliesAreCompiledOnHost(t *testing.T) {
	// In particular JavaScript's Go filename must not end in _wasm.go: Go would
	// silently restrict that registration to GOARCH=wasm rather than Linux x86-64.
	want := []string{"beam", "cpp", "cpython", "dotnet", "javascript-wasm", "jvm", "native", "pypy", "scientific-functional", "scripting", "specialized"}
	if got := RegisteredFamilies(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registered families: got %v want %v", got, want)
	}
	cfg, err := ParseConfig("cpp,cpython,pypy,native,beam,dotnet,javascript-wasm,scientific-functional,scripting,specialized", "", "", DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for language, want := range map[string]string{"CPP20": "cpp", "PYTHON3": "cpython", "PYPY3": "pypy", "RUST2024": "native", "JAVASCRIPT": "javascript-wasm", "ERLANG": "beam", "CSHARP": "dotnet", "JULIA": "scientific-functional", "RUBY": "scripting"} {
		if got, err := cfg.Family(language); err != nil || got != want {
			t.Errorf("%s: got %s (%v), want %s", language, got, err, want)
		}
	}
}
