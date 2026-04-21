package runtimepacks

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRepositoryCatalogIncludesPlainRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	production, err := catalog.ProductionImages()
	if err != nil {
		t.Fatalf("ProductionImages returned error: %v", err)
	}
	if len(production) != 11 {
		t.Fatalf("expected 11 production images, got %d", len(production))
	}

	if production[0].Name != "type-a" || !reflect.DeepEqual(production[0].Languages, []string{"bf", "elixir", "erlang", "haskell", "lisp", "lua", "ocaml", "perl", "php", "plain", "prolog", "pypy", "python", "r", "racket", "ruby", "sqlite", "wasm", "whitespace"}) {
		t.Fatalf("type-a production image = %+v", production[0])
	}
	if production[1].Name != "type-b" || !reflect.DeepEqual(production[1].Languages, []string{"clojure", "groovy", "java", "javascript", "scala", "typescript"}) {
		t.Fatalf("type-b production image = %+v", production[1])
	}
	if production[2].Name != "type-c" || !reflect.DeepEqual(production[2].Languages, []string{"ada", "d", "fortran", "go", "nim", "pascal", "rust", "zig"}) {
		t.Fatalf("type-c production image = %+v", production[2])
	}
	if production[3].Name != "type-d" || !reflect.DeepEqual(production[3].Languages, []string{"kotlin"}) {
		t.Fatalf("type-d production image = %+v", production[3])
	}
	if production[4].Name != "type-e" || !reflect.DeepEqual(production[4].Languages, []string{"csharp", "fsharp"}) {
		t.Fatalf("type-e production image = %+v", production[4])
	}
	if production[5].Name != "type-f" || !reflect.DeepEqual(production[5].Languages, []string{"uhmlang"}) {
		t.Fatalf("type-f production image = %+v", production[5])
	}
	if production[6].Name != "type-g" || !reflect.DeepEqual(production[6].Languages, []string{"julia"}) {
		t.Fatalf("type-g production image = %+v", production[6])
	}
	if production[7].Name != "type-h" || !reflect.DeepEqual(production[7].Languages, []string{"swift"}) {
		t.Fatalf("type-h production image = %+v", production[7])
	}
	if production[8].Name != "type-i" || !reflect.DeepEqual(production[8].Languages, []string{"java", "plain", "python"}) {
		t.Fatalf("type-i production image = %+v", production[8])
	}
	if production[9].Name != "type-j" || !reflect.DeepEqual(production[9].Languages, []string{"coq"}) {
		t.Fatalf("type-j production image = %+v", production[9])
	}
	if production[10].Name != "type-k" || !reflect.DeepEqual(production[10].Languages, []string{"dart"}) {
		t.Fatalf("type-k production image = %+v", production[10])
	}

	ci, err := catalog.CILanguageImages()
	if err != nil {
		t.Fatalf("CILanguageImages returned error: %v", err)
	}
	names := make([]string, 0, len(ci))
	for _, spec := range ci {
		names = append(names, spec.Name)
	}
	if !reflect.DeepEqual(names, []string{
		"ci-ada",
		"ci-bf",
		"ci-clojure",
		"ci-coq",
		"ci-csharp",
		"ci-d",
		"ci-dart",
		"ci-elixir",
		"ci-erlang",
		"ci-fortran",
		"ci-fsharp",
		"ci-go",
		"ci-groovy",
		"ci-haskell",
		"ci-java",
		"ci-javascript",
		"ci-julia",
		"ci-kotlin",
		"ci-lisp",
		"ci-lua",
		"ci-nim",
		"ci-ocaml",
		"ci-pascal",
		"ci-perl",
		"ci-php",
		"ci-plain",
		"ci-prolog",
		"ci-pypy",
		"ci-python",
		"ci-r",
		"ci-racket",
		"ci-ruby",
		"ci-rust",
		"ci-scala",
		"ci-sqlite",
		"ci-swift",
		"ci-typescript",
		"ci-uhmlang",
		"ci-wasm",
		"ci-whitespace",
		"ci-zig",
	}) {
		t.Fatalf("ci image names = %v", names)
	}
}

func TestRepositoryCatalogStrengthensNewLanguageSmokeCoverage(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	tests := map[string][]string{
		"ada":     {"gnatmake", "Broken.adb"},
		"clojure": {"PushbackReader", "Main.clj"},
		"dart":    {"dart compile exe", "Broken.dart"},
		"erlang":  {"Broken.erl", "erlc"},
		"nim":     {"nim c", "Broken.nim"},
		"pascal":  {"fpc", "Broken.pas"},
		"racket":  {"raco make", "Broken.rkt"},
		"zig":     {"Broken.zig", "zig build-exe"},
		"r":       {"Broken.R", "parse(file=commandArgs(TRUE)[1])"},
		"fortran": {"Broken.f90", "gfortran"},
		"d":       {"Broken.d", "ldc2"},
		"groovy":  {"Broken.groovy", "groovyc"},
		"prolog":  {"Broken.pl", "swipl"},
		"lisp":    {"Broken.lisp", "sbcl"},
		"coq":     {"Broken.v", "coqc"},
	}

	for language, patterns := range tests {
		spec, ok := catalog.Languages[language]
		if !ok {
			t.Fatalf("missing language %q in catalog", language)
		}
		body := strings.Join(spec.Smoke.Command, "\n")
		for _, pattern := range patterns {
			if !strings.Contains(body, pattern) {
				t.Fatalf("language %q smoke command must contain %q, got %q", language, pattern, body)
			}
		}
	}
}

func TestRepositoryCatalogPythonIncludesJudgeLibrariesAndPyPy(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	python, ok := catalog.Languages["python"]
	if !ok {
		t.Fatalf("python language missing from catalog")
	}
	for _, pkg := range []string{
		"numpy",
		"pandas",
		"seaborn",
		"matplotlib",
		"pillow",
		"six",
		"qiskit",
		"pyparsing",
		"pylatexenc",
		"jax[cpu]",
	} {
		if !slices.Contains(python.Install.Pip, pkg) {
			t.Fatalf("python runtime must include %q, got %v", pkg, python.Install.Pip)
		}
	}
	scriptBody := strings.Join(python.Install.Script, "\n")
	for _, marker := range []string{
		"download.pytorch.org/whl/cpu",
		"torch",
		"torchvision",
	} {
		if !strings.Contains(scriptBody, marker) {
			t.Fatalf("python runtime install script must contain %q, got %q", marker, scriptBody)
		}
	}

	if _, ok := catalog.Languages["pypy"]; !ok {
		t.Fatalf("pypy language missing from catalog")
	}
}

func TestRepositoryCatalogKeepsKotlinCIJavaRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	ci, err := catalog.CILanguageImages()
	if err != nil {
		t.Fatalf("CILanguageImages returned error: %v", err)
	}

	for _, spec := range ci {
		if spec.Name != "ci-kotlin" {
			continue
		}
		if !slices.Contains(spec.AptPackages, "openjdk-17-jre-headless") {
			t.Fatalf("ci-kotlin apt packages = %v, want openjdk-17-jre-headless for run_konan", spec.AptPackages)
		}
		return
	}

	t.Fatalf("ci-kotlin image not found")
}
