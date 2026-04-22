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
	if len(production) != 12 {
		t.Fatalf("expected 12 production images, got %d", len(production))
	}

	if production[0].Name != "type-a" || !reflect.DeepEqual(production[0].Languages, []string{"aheui", "bf", "elixir", "erlang", "haskell", "lisp", "lua", "ocaml", "perl", "php", "plain", "prolog", "pypy", "r", "racket", "ruby", "sqlite", "wasm", "whitespace"}) {
		t.Fatalf("type-a production image = %+v", production[0])
	}
	if production[1].Name != "type-b" || !reflect.DeepEqual(production[1].Languages, []string{"clojure", "groovy", "java", "javascript", "scala", "typescript"}) {
		t.Fatalf("type-b production image = %+v", production[1])
	}
	if production[2].Name != "type-c" || !reflect.DeepEqual(production[2].Languages, []string{"ada", "asm", "d", "fortran", "go", "nasm", "nim", "pascal", "rust", "zig"}) {
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
	if production[11].Name != "type-l" || !reflect.DeepEqual(production[11].Languages, []string{"python"}) {
		t.Fatalf("type-l production image = %+v", production[11])
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
		"ci-aheui",
		"ci-asm",
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
		"ci-nasm",
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
		"aheui":      {"Hello, World!", "Main.aheui"},
		"ada":        {"gnatmake", "Broken.adb"},
		"asm":        {"Main.s", "Broken.s", "gcc -nostdlib -static -no-pie"},
		"clojure":    {"PushbackReader", "Main.clj"},
		"dart":       {"dart compile exe", "Broken.dart"},
		"erlang":     {"Broken.erl", "erlc"},
		"nim":        {"nim c", "Broken.nim"},
		"pascal":     {"fpc", "Broken.pas"},
		"racket":     {"raco make", "Broken.rkt"},
		"zig":        {"Broken.zig", "zig build-exe"},
		"r":          {"Broken.R", "parse(file=commandArgs(TRUE)[1])"},
		"fortran":    {"Broken.f90", "gfortran"},
		"d":          {"Broken.d", "ldc2"},
		"groovy":     {"Broken.groovy", "groovyc"},
		"prolog":     {"Broken.pl", "swipl"},
		"lisp":       {"Broken.lisp", "sbcl"},
		"nasm":       {"Main.asm", "Broken.asm", "nasm -felf64"},
		"coq":        {"Broken.v", "coqc"},
		"python":     {"import qiskit", "import robot_judge", "from jungol_robot import Direction, Position"},
		"typescript": {"declare const require: any;", "const fs = require('fs');", "tsc Main.ts --module commonjs --target es2019 --outDir dist"},
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

func TestRepositoryCatalogIncludesAheuiRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	spec, ok := catalog.Languages["aheui"]
	if !ok {
		t.Fatalf("aheui language missing from catalog")
	}
	if !slices.Contains(spec.Install.Apt, "python3") || !slices.Contains(spec.Install.Apt, "python3-pip") {
		t.Fatalf("aheui apt packages = %v, want python3 and python3-pip", spec.Install.Apt)
	}
	if !slices.Contains(spec.Install.Pip, "aheui==1.2.5") {
		t.Fatalf("aheui pip packages = %v, want aheui==1.2.5", spec.Install.Pip)
	}
}

func TestRepositoryCatalogUsesTrixieAndUpdatedICUForDebianProfiles(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	for _, profileName := range []string{"type-a", "type-b", "type-c", "type-d", "type-e", "type-f", "type-i", "type-j", "type-k", "type-l"} {
		profile, ok := catalog.Profiles[profileName]
		if !ok {
			t.Fatalf("profile %q missing from catalog", profileName)
		}
		if profile.BaseImage != "debian:trixie-slim" {
			t.Fatalf("profile %q base image = %q, want debian:trixie-slim", profileName, profile.BaseImage)
		}
	}

	for _, language := range []string{"csharp", "fsharp"} {
		spec, ok := catalog.Languages[language]
		if !ok {
			t.Fatalf("language %q missing from catalog", language)
		}
		if !slices.Contains(spec.Install.Apt, "libicu76") {
			t.Fatalf("%s apt packages = %v, want libicu76", language, spec.Install.Apt)
		}
	}
}

func TestRepositoryCatalogIncludesAssemblyToolchains(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	asmSpec, ok := catalog.Languages["asm"]
	if !ok {
		t.Fatalf("asm language missing from catalog")
	}
	if !slices.Contains(asmSpec.Install.Apt, "gcc") {
		t.Fatalf("asm apt packages = %v, want gcc", asmSpec.Install.Apt)
	}

	nasmSpec, ok := catalog.Languages["nasm"]
	if !ok {
		t.Fatalf("nasm language missing from catalog")
	}
	if !slices.Contains(nasmSpec.Install.Apt, "gcc") || !slices.Contains(nasmSpec.Install.Apt, "nasm") {
		t.Fatalf("nasm apt packages = %v, want gcc and nasm", nasmSpec.Install.Apt)
	}
}

func TestRepositoryCatalogPinsOfficialNode24Toolchain(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	for _, language := range []string{"javascript", "typescript"} {
		spec, ok := catalog.Languages[language]
		if !ok {
			t.Fatalf("%s language missing from catalog", language)
		}
		if !reflect.DeepEqual(spec.Install.Apt, []string{"curl", "xz-utils"}) {
			t.Fatalf("%s apt packages = %v, want curl and xz-utils", language, spec.Install.Apt)
		}
		installScript := strings.Join(spec.Install.Script, "\n")
		for _, marker := range []string{
			"export NODE_VERSION=24.15.0",
			"https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz",
			`ln -sfn "/opt/node-v${NODE_VERSION}-linux-x64/bin/node" /usr/local/bin/node`,
			`ln -sfn "/opt/node-v${NODE_VERSION}-linux-x64/bin/npm" /usr/local/bin/npm`,
		} {
			if !strings.Contains(installScript, marker) {
				t.Fatalf("%s install script must contain %q, got %q", language, marker, installScript)
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
		"numpy==2.4.4",
		"pandas==3.0.2",
		"seaborn==0.13.2",
		"matplotlib==3.10.8",
		"pillow==12.2.0",
		"six==1.17.0",
		"qiskit==2.4.0",
		"pyparsing==3.3.2",
		"pylatexenc==2.10",
		"jax[cpu]==0.10.0",
	} {
		if !slices.Contains(python.Install.Pip, pkg) {
			t.Fatalf("python runtime must include %q, got %v", pkg, python.Install.Pip)
		}
	}
	scriptBody := strings.Join(python.Install.Script, "\n")
	for _, marker := range []string{
		"download.pytorch.org/whl/cpu",
		"torch==2.11.0+cpu",
		"torchvision==0.26.0+cpu",
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

	if !slices.Contains(catalog.Languages["plain"].Install.Apt, "libc6-dev") {
		t.Fatalf("plain apt packages = %v, want libc6-dev for hosted C/C++ compilation", catalog.Languages["plain"].Install.Apt)
	}
	if !slices.Contains(catalog.Languages["nim"].Install.Apt, "libc6-dev") {
		t.Fatalf("nim apt packages = %v, want libc6-dev for Nim C backend headers", catalog.Languages["nim"].Install.Apt)
	}

	ci, err := catalog.CILanguageImages()
	if err != nil {
		t.Fatalf("CILanguageImages returned error: %v", err)
	}

	for _, spec := range ci {
		if spec.Name != "ci-kotlin" {
			continue
		}
		if !slices.Contains(spec.AptPackages, "default-jre-headless") {
			t.Fatalf("ci-kotlin apt packages = %v, want default-jre-headless for run_konan", spec.AptPackages)
		}
		for _, marker := range []string{
			"nim-lang.org/choosenim/init.sh",
			"choosenim 2.2.8",
			"/root/.nimble/bin/nim",
		} {
			if !strings.Contains(strings.Join(catalog.Languages["nim"].Install.Script, "\n"), marker) {
				t.Fatalf("nim install script must contain %q", marker)
			}
		}
		return
	}

	t.Fatalf("ci-kotlin image not found")
}
