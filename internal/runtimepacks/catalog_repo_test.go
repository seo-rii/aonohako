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
	if len(production) != 19 {
		t.Fatalf("expected 19 production images, got %d", len(production))
	}

	if production[0].Name != "type-a" || !reflect.DeepEqual(production[0].Languages, []string{"aheui", "apl", "awk", "bf", "bqn", "elixir", "erlang", "gleam", "golfscript", "haskell", "janet", "lisp", "lua", "ocaml", "perl", "php", "plain", "prolog", "pypy", "r", "racket", "ruby", "scheme", "smalltalk", "sqlite", "uiua", "wasm", "whitespace"}) {
		t.Fatalf("type-a production image = %+v", production[0])
	}
	if production[1].Name != "type-b" || !reflect.DeepEqual(production[1].Languages, []string{"clojure", "deno", "graphql", "groovy", "java", "javascript", "scala", "typescript"}) {
		t.Fatalf("type-b production image = %+v", production[1])
	}
	if production[2].Name != "type-c" || !reflect.DeepEqual(production[2].Languages, []string{"ada", "asm", "c3", "crystal", "d", "fortran", "go", "hare", "mojo", "nasm", "nim", "odin", "pascal", "rust", "vlang", "zig"}) {
		t.Fatalf("type-c production image = %+v", production[2])
	}
	if production[3].Name != "type-d" || !reflect.DeepEqual(production[3].Languages, []string{"kotlin", "kotlin-jvm"}) {
		t.Fatalf("type-d production image = %+v", production[3])
	}
	if production[4].Name != "type-e" || !reflect.DeepEqual(production[4].Languages, []string{"csharp", "fsharp", "vbnet"}) {
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
	if production[9].Name != "type-j" || !reflect.DeepEqual(production[9].Languages, []string{"agda", "coq", "rocq", "tla", "why3"}) {
		t.Fatalf("type-j production image = %+v", production[9])
	}
	if production[10].Name != "type-k" || !reflect.DeepEqual(production[10].Languages, []string{"dart"}) {
		t.Fatalf("type-k production image = %+v", production[10])
	}
	if production[11].Name != "type-l" || !reflect.DeepEqual(production[11].Languages, []string{"python"}) {
		t.Fatalf("type-l production image = %+v", production[11])
	}
	if production[12].Name != "type-m" || !reflect.DeepEqual(production[12].Languages, []string{"duckdb", "gdl", "octave"}) {
		t.Fatalf("type-m production image = %+v", production[12])
	}
	if production[13].Name != "type-n" || !reflect.DeepEqual(production[13].Languages, []string{"systemverilog", "verilog", "vhdl"}) {
		t.Fatalf("type-n production image = %+v", production[13])
	}
	if production[14].Name != "type-o" || !reflect.DeepEqual(production[14].Languages, []string{"cuda-ocelot"}) {
		t.Fatalf("type-o production image = %+v", production[14])
	}
	if production[15].Name != "type-p" || !reflect.DeepEqual(production[15].Languages, []string{"carbon", "vb6"}) {
		t.Fatalf("type-p production image = %+v", production[15])
	}
	if production[16].Name != "type-q" || !reflect.DeepEqual(production[16].Languages, []string{"dafny"}) {
		t.Fatalf("type-q production image = %+v", production[16])
	}
	if production[17].Name != "type-r" || !reflect.DeepEqual(production[17].Languages, []string{"isabelle"}) {
		t.Fatalf("type-r production image = %+v", production[17])
	}
	if production[18].Name != "type-s" || !reflect.DeepEqual(production[18].Languages, []string{"lean4"}) {
		t.Fatalf("type-s production image = %+v", production[18])
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
		"ci-agda",
		"ci-aheui",
		"ci-apl",
		"ci-asm",
		"ci-awk",
		"ci-bf",
		"ci-bqn",
		"ci-c3",
		"ci-carbon",
		"ci-clojure",
		"ci-coq",
		"ci-crystal",
		"ci-csharp",
		"ci-cuda-ocelot",
		"ci-d",
		"ci-dafny",
		"ci-dart",
		"ci-deno",
		"ci-duckdb",
		"ci-elixir",
		"ci-erlang",
		"ci-fortran",
		"ci-fsharp",
		"ci-gdl",
		"ci-gleam",
		"ci-go",
		"ci-golfscript",
		"ci-graphql",
		"ci-groovy",
		"ci-hare",
		"ci-haskell",
		"ci-isabelle",
		"ci-janet",
		"ci-java",
		"ci-javascript",
		"ci-julia",
		"ci-kotlin",
		"ci-kotlin-jvm",
		"ci-lean4",
		"ci-lisp",
		"ci-lua",
		"ci-mojo",
		"ci-nasm",
		"ci-nim",
		"ci-ocaml",
		"ci-octave",
		"ci-odin",
		"ci-pascal",
		"ci-perl",
		"ci-php",
		"ci-plain",
		"ci-prolog",
		"ci-pypy",
		"ci-python",
		"ci-r",
		"ci-racket",
		"ci-rocq",
		"ci-ruby",
		"ci-rust",
		"ci-scala",
		"ci-scheme",
		"ci-smalltalk",
		"ci-sqlite",
		"ci-swift",
		"ci-systemverilog",
		"ci-tla",
		"ci-typescript",
		"ci-uhmlang",
		"ci-uiua",
		"ci-vb6",
		"ci-vbnet",
		"ci-verilog",
		"ci-vhdl",
		"ci-vlang",
		"ci-wasm",
		"ci-whitespace",
		"ci-why3",
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
		"aheui":         {"Hello, World!", "Main.aheui"},
		"ada":           {"gnatmake", "Broken.adb"},
		"agda":          {"agda Main.agda", "data Unit : Set"},
		"apl":           {"kanapl@0.0.0", "apl --script -f Main.apl"},
		"asm":           {"Main.s", "Broken.s", "gcc -nostdlib -static -no-pie"},
		"awk":           {"gawk --sandbox", "Main.awk"},
		"bqn":           {"CBQN_COMMIT=d56147be877693eaed351745782c258bd7424de7", "bqn Main.bqn"},
		"c3":            {"C3_VERSION=0.7.11", "c3c compile Main.c3"},
		"carbon":        {"CARBON_VERSION=0.0.0-0.nightly.2026.05.02", "carbon compile --phase=check Main.carbon"},
		"clojure":       {"PushbackReader", "Main.clj"},
		"crystal":       {"crystal build Main.cr", "Broken.cr"},
		"cuda-ocelot":   {"GPUOCELOT_COMMIT=b16039dc940dc6bc4ea0a98380495769ff35ed99", "libfl-dev", "libzstd-dev", "aonohako-cuda-ocelot-build Main.cu Main"},
		"dafny":         {"DAFNY_VERSION=4.11.0", "curl --retry 6", "wget --tries=6", "dafny verify Main.dfy"},
		"dart":          {"dart compile exe", "Broken.dart"},
		"deno":          {"DENO_VERSION=2.7.14", "deno check Main.ts", "deno run --no-prompt Main.ts"},
		"duckdb":        {"DUCKDB_VERSION=1.5.2", "aonohako-duckdb-run Main.sql"},
		"erlang":        {"Broken.erl", "erlc"},
		"gdl":           {"aonohako-gdl-run", "Main.pro"},
		"gleam":         {"GLEAM_VERSION=1.16.0", "aonohako-gleam-run ."},
		"golfscript":    {"golfscript_sandboxed.rb", "Main.gs"},
		"graphql":       {"graphql-core==3.2.6", "aonohako-graphql-run Main.graphql"},
		"hare":          {"hare build -o Main Main.ha", "fmt::println"},
		"isabelle":      {"ISABELLE_VERSION=Isabelle2025-2", "curl --retry 6", "wget --tries=6", "isabelle build -D ."},
		"janet":         {"JANET_VERSION=1.41.2", "janet Main.janet"},
		"kotlin-jvm":    {"KOTLIN_JVM_VERSION=2.3.21", "kotlinc Main.kt -include-runtime -d Main.jar"},
		"lean4":         {"LEAN_VERSION=4.29.1", "curl --retry 6", "wget --tries=6", "lean Main.lean"},
		"mojo":          {"mojo==0.26.2.0", "mojo build Main.mojo"},
		"nim":           {"nim c", "Broken.nim"},
		"octave":        {"octave-cli --quiet", "Main.m"},
		"odin":          {"ODIN_VERSION=dev-2026-04", "odin build . -out:Main"},
		"pascal":        {"fpc", "Broken.pas"},
		"racket":        {"raco make", "Broken.rkt"},
		"rocq":          {"rocq c Main.v", "coqc -q Main.v"},
		"scheme":        {"chibi-scheme Main.scm", "(scheme base)"},
		"smalltalk":     {"GST_VERSION=3.2.5", "sed -i 's/const char \\*inbuf;/char *inbuf;/'", "CC=/usr/bin/gcc-14 ./configure", "make -j1", "gst -q Main.st"},
		"systemverilog": {"iverilog -g2012", "Main.sv"},
		"tla":           {"TLA_VERSION=1.7.4", "install -d -m 0755 /usr/local/lib/aonohako", "curl --retry 6", "wget --tries=6", "aonohako-tla-run Main.tla"},
		"uiua":          {"UIUA_VERSION=0.18.1", "uiua run Main.ua --no-format"},
		"vb6":           {"aonohako-vb6-run Main.bas", "Sub Main()"},
		"vbnet":         {"App.vbproj", "dotnet publish App.vbproj"},
		"verilog":       {"iverilog -g2012", "Main.v"},
		"vhdl":          {"ghdl -a --std=08", "main_tb"},
		"vlang":         {"V_COMMIT=e632a84cd573bb05f3f72a0ae0cb9bbcaae404da", "v -o Main Main.v"},
		"why3":          {"aonohako-why3-prove Main.mlw", "goal G: true"},
		"zig":           {"Broken.zig", "zig build-exe"},
		"r":             {"Broken.R", "parse(file=commandArgs(TRUE)[1])"},
		"fortran":       {"Broken.f90", "gfortran"},
		"d":             {"Broken.d", "ldc2"},
		"groovy":        {"Broken.groovy", "groovyc"},
		"prolog":        {"Broken.pl", "swipl"},
		"lisp":          {"Broken.lisp", "sbcl"},
		"nasm":          {"Main.asm", "Broken.asm", "nasm -felf64"},
		"coq":           {"Broken.v", "coqc"},
		"python":        {"import qiskit", "import robot_judge", "from jungol_robot import Direction, Position"},
		"typescript":    {"declare const require: any;", "const fs = require('fs');", "tsc Main.ts --module commonjs --target es2019 --outDir dist"},
		"wasm":          {"-W max-memory-size=33554432", "-W max-wasm-stack=1048576", "-W trap-on-grow-failure=y"},
	}

	for language, patterns := range tests {
		spec, ok := catalog.Languages[language]
		if !ok {
			t.Fatalf("missing language %q in catalog", language)
		}
		body := strings.Join(append(append(append(append([]string{}, spec.Install.Apt...), spec.Install.Pip...), spec.Install.NPM...), append(spec.Install.Script, spec.Smoke.Command...)...), "\n")
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

func TestRepositoryCatalogPinsRustToolchain(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	spec, ok := catalog.Languages["rust"]
	if !ok {
		t.Fatalf("rust language missing from catalog")
	}
	body := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"export RUST_VERSION=1.95.0",
		`--default-toolchain "$RUST_VERSION"`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("rust install script must contain %q", marker)
		}
	}
	if strings.Contains(body, "--default-toolchain stable") {
		t.Fatalf("rust install script must pin the requested toolchain instead of stable")
	}
}

func TestRepositoryCatalogUsesTrixieAndUpdatedICUForDebianProfiles(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	const pinnedTrixie = "debian:trixie-slim@sha256:cedb1ef40439206b673ee8b33a46a03a0c9fa90bf3732f54704f99cb061d2c5a"
	for _, profileName := range []string{"type-a", "type-b", "type-c", "type-d", "type-e", "type-f", "type-i", "type-j", "type-k", "type-l", "type-m", "type-n", "type-p", "type-q", "type-r", "type-s"} {
		profile, ok := catalog.Profiles[profileName]
		if !ok {
			t.Fatalf("profile %q missing from catalog", profileName)
		}
		if profile.BaseImage != pinnedTrixie {
			t.Fatalf("profile %q base image = %q, want %s", profileName, profile.BaseImage, pinnedTrixie)
		}
	}
	if profile := catalog.Profiles["type-g"]; profile.BaseImage != "julia:1.11.5-bookworm@sha256:be7093a80d030bb8ec7cdc093aabb3428da995b466dbf6c0b472380107472316" {
		t.Fatalf("type-g base image = %q, want digest-pinned julia image", profile.BaseImage)
	}
	if profile := catalog.Profiles["type-h"]; profile.BaseImage != "swift:6.2.1-bookworm@sha256:73f569f5536fe3c9ad5109eb4622c5560af7424d55304955190e5fbccc047b86" {
		t.Fatalf("type-h base image = %q, want digest-pinned swift image", profile.BaseImage)
	}
	if profile := catalog.Profiles["type-o"]; profile.BaseImage != "nvidia/cuda:11.8.0-devel-ubuntu22.04@sha256:94fd755736cb58979173d491504f0b573247b1745250249415b07fefc738e41f" {
		t.Fatalf("type-o base image = %q, want digest-pinned CUDA 11.8 image", profile.BaseImage)
	}
	for profileName, profile := range catalog.Profiles {
		if !strings.Contains(profile.BaseImage, "@sha256:") {
			t.Fatalf("profile %q base image %q must be digest pinned", profileName, profile.BaseImage)
		}
	}

	for _, language := range []string{"csharp", "fsharp", "vbnet"} {
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

func TestRepositoryCatalogPinsGCC16AcrossProfiles(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	for _, profileName := range sortedKeys(catalog.Profiles) {
		if profileName == "type-j" || profileName == "type-o" || profileName == "type-q" || profileName == "type-r" || profileName == "type-s" {
			continue
		}
		profile := catalog.Profiles[profileName]
		installScript := strings.Join(profile.Install.Script, "\n")
		for _, marker := range []string{
			"deb http://deb.debian.org/debian sid main",
			"apt-get install -y --no-install-recommends -t sid gcc-16 g++-16",
			"ln -sfn /usr/bin/gcc-16 /usr/local/bin/gcc",
			"ln -sfn /usr/bin/g++-16 /usr/local/bin/g++",
			"ln -sfn /usr/bin/gcc-16 /usr/local/bin/cc",
			"ln -sfn /usr/bin/g++-16 /usr/local/bin/c++",
		} {
			if !strings.Contains(installScript, marker) {
				t.Fatalf("profile %q install script must contain %q, got %q", profileName, marker, installScript)
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
	nimScriptBody := strings.Join(catalog.Languages["nim"].Install.Script, "\n")
	for _, marker := range []string{
		"nim-lang.org/choosenim/init.sh",
		"choosenim 2.2.8",
		"install -d /usr/local/lib/nim",
		"cp -a /root/.choosenim/toolchains/nim-2.2.8/lib /usr/local/lib/nim/",
		"install -m 0755 /root/.choosenim/toolchains/nim-2.2.8/bin/nim /usr/local/bin/nim",
		"install -m 0755 /root/.choosenim/toolchains/nim-2.2.8/bin/nimble /usr/local/bin/nimble",
	} {
		if !strings.Contains(nimScriptBody, marker) {
			t.Fatalf("nim install script must contain %q", marker)
		}
	}
	if strings.Contains(nimScriptBody, "ln -sfn /root/.nimble/bin/") {
		t.Fatalf("nim install script must not leave /usr/local/bin symlinked into /root/.nimble/bin")
	}
	wasmScriptBody := strings.Join(catalog.Languages["wasm"].Install.Script, "\n")
	if !strings.Contains(wasmScriptBody, "WASMTIME_VERSION=44.0.0") {
		t.Fatalf("wasm install script must pin wasmtime version")
	}
	if !strings.Contains(wasmScriptBody, "github.com/bytecodealliance/wasmtime/releases/download/v${WASMTIME_VERSION}") {
		t.Fatalf("wasm install script must download a pinned release artifact")
	}
	if !strings.Contains(wasmScriptBody, "install -m 0755 /tmp/wasmtime/wasmtime /usr/local/bin/wasmtime") {
		t.Fatalf("wasm install script must materialize wasmtime under /usr/local/bin")
	}
	if strings.Contains(wasmScriptBody, "ln -sfn /root/.wasmtime/bin/wasmtime") {
		t.Fatalf("wasm install script must not leave /usr/local/bin symlinked into /root/.wasmtime/bin")
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
		body := strings.Join(spec.InstallScript, "\n") + "\n" + strings.Join(spec.SmokeCommand, "\n")
		for _, marker := range []string{
			"KONAN_DATA_DIR=/usr/local/lib/aonohako/konan",
			"kotlinc-native -J-Xms64m -J-Xmx1024m -J-Xss1m",
			"chmod -R a+rX /usr/local/lib/aonohako/konan",
			"chown 65532:65532 /usr/local/lib/aonohako/konan/cache/.lock",
			"chmod 0600 /usr/local/lib/aonohako/konan/cache/.lock",
		} {
			if !strings.Contains(body, marker) {
				t.Fatalf("ci-kotlin must prewarm readonly Kotlin/Native dependencies with %q, got %q", marker, body)
			}
		}
		return
	}

	t.Fatalf("ci-kotlin image not found")
}
