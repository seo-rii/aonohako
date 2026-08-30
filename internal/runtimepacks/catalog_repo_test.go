package runtimepacks

import (
	"os"
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
	if len(production) != 25 {
		t.Fatalf("expected 25 production images, got %d", len(production))
	}

	if production[0].Name != "type-a" || !reflect.DeepEqual(production[0].Languages, []string{"aheui", "algol68", "apecode", "apl", "awk", "bc", "befunge", "bf", "bqn", "chez-scheme", "chicken-scheme", "elixir", "erlang", "fennel", "forth", "gforth", "gleam", "golfscript", "guile", "haskell", "idris2", "j", "janet", "lisp", "lolcode", "lua", "malbolge", "mercury", "ocaml", "perl", "php", "picolisp", "plain", "prolog", "pypy", "r", "racket", "raku", "ruby", "scheme", "sed", "smalltalk", "sml", "sqlite", "tcl", "uiua", "wasm", "whitespace"}) {
		t.Fatalf("type-a production image = %+v", production[0])
	}
	if production[1].Name != "type-b" || !reflect.DeepEqual(production[1].Languages, []string{"assemblyscript", "clojure", "coffeescript", "deno", "elm", "graphql", "groovy", "haxe", "java", "javascript", "purescript", "rescript", "scala", "typescript"}) {
		t.Fatalf("type-b production image = %+v", production[1])
	}
	if production[2].Name != "type-c" || !reflect.DeepEqual(production[2].Languages, []string{"ada", "asm", "c3", "classic-basic", "cobol", "crystal", "cython", "d", "delphi", "fortran", "freebasic", "gnucobol", "go", "hare", "koka", "mojo", "moonbit", "nasm", "nim", "objective-c", "objective-cpp", "objectpascal", "odin", "pascal", "qbasic", "rust", "vala", "vlang", "zerolang", "zig"}) {
		t.Fatalf("type-c production image = %+v", production[2])
	}
	if production[3].Name != "type-d" || !reflect.DeepEqual(production[3].Languages, []string{"kotlin", "kotlin-jvm"}) {
		t.Fatalf("type-d production image = %+v", production[3])
	}
	if production[4].Name != "type-e" || !reflect.DeepEqual(production[4].Languages, []string{"csharp", "fsharp", "powershell", "vbnet"}) {
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
	if production[8].Name != "type-i" || !reflect.DeepEqual(production[8].Languages, []string{"c", "cpp", "java", "plain", "pypy", "python"}) {
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
	if production[19].Name != "type-t" || !reflect.DeepEqual(production[19].Languages, []string{"acl2", "alloy", "fstar"}) {
		t.Fatalf("type-t production image = %+v", production[19])
	}
	if production[20].Name != "type-u" || !reflect.DeepEqual(production[20].Languages, []string{"kframework"}) {
		t.Fatalf("type-u production image = %+v", production[20])
	}
	if production[21].Name != "type-v" || !reflect.DeepEqual(production[21].Languages, []string{"chapel"}) {
		t.Fatalf("type-v production image = %+v", production[21])
	}
	if production[22].Name != "type-w" || !reflect.DeepEqual(production[22].Languages, []string{"pony"}) {
		t.Fatalf("type-w production image = %+v", production[22])
	}
	if production[23].Name != "type-x" || !reflect.DeepEqual(production[23].Languages, []string{"bash", "fish", "posix-sh", "zsh"}) {
		t.Fatalf("type-x production image = %+v", production[23])
	}
	if production[24].Name != "type-y" || !reflect.DeepEqual(production[24].Languages, []string{"factor"}) {
		t.Fatalf("type-y production image = %+v", production[24])
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
		"ci-acl2",
		"ci-ada",
		"ci-agda",
		"ci-aheui",
		"ci-algol68",
		"ci-alloy",
		"ci-apecode",
		"ci-apl",
		"ci-asm",
		"ci-assemblyscript",
		"ci-awk",
		"ci-bash",
		"ci-bc",
		"ci-befunge",
		"ci-bf",
		"ci-bqn",
		"ci-c",
		"ci-c3",
		"ci-carbon",
		"ci-chapel",
		"ci-chez-scheme",
		"ci-chicken-scheme",
		"ci-classic-basic",
		"ci-clojure",
		"ci-cobol",
		"ci-coffeescript",
		"ci-coq",
		"ci-cpp",
		"ci-crystal",
		"ci-csharp",
		"ci-cuda-ocelot",
		"ci-cython",
		"ci-d",
		"ci-dafny",
		"ci-dart",
		"ci-delphi",
		"ci-deno",
		"ci-duckdb",
		"ci-elixir",
		"ci-elm",
		"ci-erlang",
		"ci-factor",
		"ci-fennel",
		"ci-fish",
		"ci-forth",
		"ci-fortran",
		"ci-freebasic",
		"ci-fsharp",
		"ci-fstar",
		"ci-gdl",
		"ci-gforth",
		"ci-gleam",
		"ci-gnucobol",
		"ci-go",
		"ci-golfscript",
		"ci-graphql",
		"ci-groovy",
		"ci-guile",
		"ci-hare",
		"ci-haskell",
		"ci-haxe",
		"ci-idris2",
		"ci-isabelle",
		"ci-j",
		"ci-janet",
		"ci-java",
		"ci-javascript",
		"ci-julia",
		"ci-kframework",
		"ci-koka",
		"ci-kotlin",
		"ci-kotlin-jvm",
		"ci-lean4",
		"ci-lisp",
		"ci-lolcode",
		"ci-lua",
		"ci-malbolge",
		"ci-mercury",
		"ci-mojo",
		"ci-moonbit",
		"ci-nasm",
		"ci-nim",
		"ci-objective-c",
		"ci-objective-cpp",
		"ci-objectpascal",
		"ci-ocaml",
		"ci-octave",
		"ci-odin",
		"ci-pascal",
		"ci-perl",
		"ci-php",
		"ci-picolisp",
		"ci-plain",
		"ci-pony",
		"ci-posix-sh",
		"ci-powershell",
		"ci-prolog",
		"ci-purescript",
		"ci-pypy",
		"ci-python",
		"ci-qbasic",
		"ci-r",
		"ci-racket",
		"ci-raku",
		"ci-rescript",
		"ci-rocq",
		"ci-ruby",
		"ci-rust",
		"ci-scala",
		"ci-scheme",
		"ci-sed",
		"ci-smalltalk",
		"ci-sml",
		"ci-sqlite",
		"ci-swift",
		"ci-systemverilog",
		"ci-tcl",
		"ci-tla",
		"ci-typescript",
		"ci-uhmlang",
		"ci-uiua",
		"ci-vala",
		"ci-vb6",
		"ci-vbnet",
		"ci-verilog",
		"ci-vhdl",
		"ci-vlang",
		"ci-wasm",
		"ci-whitespace",
		"ci-why3",
		"ci-zerolang",
		"ci-zig",
		"ci-zsh",
	}) {
		t.Fatalf("ci image names = %v", names)
	}
}

func TestRepositoryCatalogRefreshesMercuryRepoBeforeInstall(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	production, err := catalog.ProductionImages()
	if err != nil {
		t.Fatalf("ProductionImages returned error: %v", err)
	}
	var typeA *ImageSpec
	for i := range production {
		if production[i].Name == "type-a" {
			typeA = &production[i]
			break
		}
	}
	if typeA == nil {
		t.Fatalf("type-a production image not found")
	}

	script := strings.Join(typeA.InstallScript, "\n")
	repoIndex := strings.Index(script, "dl.mercurylang.org/deb/ trixie main")
	if repoIndex == -1 {
		t.Fatalf("type-a install script is missing Mercury repo refresh markers:\n%s", script)
	}
	updateRelIndex := strings.Index(script[repoIndex:], "apt-get update -o APT::Get::List-Cleanup=false")
	installRelIndex := strings.Index(script[repoIndex:], "mercury-recommended")
	if updateRelIndex == -1 || installRelIndex == -1 {
		t.Fatalf("type-a install script is missing Mercury repo refresh markers:\n%s", script)
	}
	updateIndex := repoIndex + updateRelIndex
	installIndex := repoIndex + installRelIndex
	if !(repoIndex < updateIndex && updateIndex < installIndex) {
		t.Fatalf("Mercury repo must be refreshed after source addition and before install: repo=%d update=%d install=%d", repoIndex, updateIndex, installIndex)
	}
}

func TestRepositoryCatalogStrengthensNewLanguageSmokeCoverage(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	ciImages, err := catalog.CILanguageImages()
	if err != nil {
		t.Fatalf("CILanguageImages returned error: %v", err)
	}
	ciByLanguage := make(map[string]ImageSpec, len(ciImages))
	for _, image := range ciImages {
		ciByLanguage[strings.TrimPrefix(image.Name, "ci-")] = image
	}

	tests := map[string][]string{
		"aheui":         {"Hello, World!", "Main.aheui"},
		"algol68":       {"A68G_VERSION=3.13.3", "A68G_SHA256=78dc53f4a712a9c8ee159b1eb7045fe4ea060c4eb2a49efb9634f83c2cb13995", "A68G_SHA512=90b64911ee3b4011799425cf846fea4a26570182ec1ed9f65e6f0630e02b0623ed408fb6d074dcd2795b6412e209a39f5580d32acd079a3f3d419bf2440512a6", "algol68g-${A68G_VERSION}.tar.gz/sha512/${A68G_SHA512}/algol68g-${A68G_VERSION}.tar.gz", "--enable-core", "AONOHAKO_SAFE_RUNTIME", "aonohako_disabled_system", "aonohako_disabled_fork", "aonohako_disabled_execve", "read_rc_options", "read_env_options", "process execution is disabled", "nm -D --undefined-only", "system|fork|execve|dlopen|dlsym", "--no-compile -O0 --check --file Main.a68 --no-pragmats", "--no-compile -O0 --run --file Main.a68 --no-pragmats", "A68G_OPTIONS='-O1 --compile --debug'", "Process.a68", "ProcessFork.a68", "ProcessExec.a68", "Monitor.a68", "Broken.a68"},
		"acl2":          {"acl2", "aonohako-acl2-check Main.lisp", "plus-zero-right", "Broken.lisp"},
		"apecode":       {"APECODE_COMMIT=c7ae98d3dfc1713ecc800422a4c815628776e1e2", "python3 -m pip install --break-system-packages --no-cache-dir /tmp/apecode.tar.gz", "apecc --check Main.ape", "apecc -o Main Main.ape", "./Main", "state main", "3 1 2"},
		"ada":           {"gnatmake", "Broken.adb"},
		"agda":          {"agda Main.agda", "data Unit : Set"},
		"alloy":         {"ALLOY_VERSION=6.2.0", "org.alloytools.alloy.dist.jar", "aonohako-alloy-check Main.als", "check ok for 3", "Broken.als"},
		"apl":           {"kanapl@0.0.0", "node --disable-wasm-trap-handler --max-old-space-size=64 --max-semi-space-size=1 --stack-size=2048 /usr/local/bin/apl --script -f Main.apl"},
		"asm":           {"Main.s", "Broken.s", "gcc -nostdlib -static -no-pie"},
		"awk":           {"gawk --sandbox", "Main.awk"},
		"bash":          {"5.2.37-2+b9", "bash --noprofile --norc -n SyntaxOnly.sh", "test ! -e /tmp/aonohako-shell-syntax-leak", "Broken.sh"},
		"bc":            {"bc -q Main.bc", "1 + 1"},
		"befunge":       {"python3 /usr/local/lib/aonohako/befunge.py Main.bef", `>"ko",,91+,@`},
		"bqn":           {"CBQN_COMMIT=d56147be877693eaed351745782c258bd7424de7", "bqn Main.bqn"},
		"c3":            {"C3_VERSION=0.7.11", "c3c compile Main.c3"},
		"carbon":        {"CARBON_VERSION=0.0.0-0.nightly.2026.05.02", "16719a509201acd2a7d82c260fba073c14ce7eb53c44c6ae7dda1fa083b6fa2a", "aonohako-carbon-warmup.carbon", "carbon build-runtimes --output-directory=/opt/carbon/lib/carbon/aonohako-runtimes", "aonohako-runtimes/libcxx/lib/libc++.a", "chmod -R a+rX /opt/carbon/lib/carbon/aonohako-runtimes", "carbon compile --optimize=speed --no-debug-info --output-last-input-only --output=Main.o Main.carbon", "carbon --prebuilt-runtimes=/opt/carbon/lib/carbon/aonohako-runtimes link --output=Main Main.o", "./Main | grep '^Hello World!$'", "Broken.carbon"},
		"chapel":        {"CHAPEL_VERSION=2.9.0", "CHAPEL_DEB_SHA256=11f93de9e725a7c74608b4afcc7c8fc8bec380f27b09883cd6f69d6fbe66e13d", "sha256sum -c -", "set -euo pipefail", "chpl-language-server/src/chpl-shim.py", "chpl-venv", "fixDistDocs.perl", "fixInternalDocs.sh", "third-party", "-type l", "-perm /0001", "CHPL_COMM=none CHPL_TASKS=qthreads CHPL_TARGET_CPU=none chpl --local --fast", "here.maxTaskPar", "CHPL_RT_NUM_THREADS_PER_LOCALE=1 ./Main -nl 1", "./Main -nl 2", "test ! -e Broken"},
		"chez-scheme":   {"chezscheme=10.0.0+dfsg-5", "chez_scheme_check.scm", "/usr/bin/chezscheme --quiet --script", "aonohako-chez-compile-leak", "test ! -e aonohako-chez-compile-leak", "Broken.scm"},
		"classic-basic": {"fbc -lang qb -x Main Main.bas", "PRINT \"ok\""},
		"clojure":       {"PushbackReader", "java -Xmx128m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxDirectMemorySize=16m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m -Dfile.encoding=UTF-8 -DONLINE_JUDGE=1 -cp /usr/share/java/clojure-1.12.jar clojure.main Main.clj"},
		"cobol":         {"gnucobol", "cobc -x -free -O2 -o Main Main.cob"},
		"coffeescript":  {"coffeescript", "ln -sfn /usr/bin/coffee /usr/local/bin/coffee", "node --disable-wasm-trap-handler --max-old-space-size=64 --max-semi-space-size=1 --stack-size=2048 /usr/local/bin/coffee Main.coffee"},
		"crystal":       {"crystal build Main.cr", "Broken.cr"},
		"cuda-ocelot":   {"GPUOCELOT_COMMIT=b16039dc940dc6bc4ea0a98380495769ff35ed99", "git clone --filter=blob:none --sparse", "git sparse-checkout set --no-cone /ocelot /.gitmodules", "libfl-dev", "libzstd-dev", "aonohako-cuda-ocelot-build Main.cu Main"},
		"cython":        {"cython3 --embed -3 -o Main.c Main.pyx", "python3-config --includes --ldflags --embed"},
		"dafny":         {"DAFNY_VERSION=4.11.0", "curl --retry 6", "wget --tries=6", "dafny verify --cores 1 Main.dfy"},
		"dart":          {"dart compile exe", "Broken.dart"},
		"delphi":        {"fpc -Mdelphi -O2 -Xs -oMain Main.dpr", "AssignFile", "Broken.dpr"},
		"deno":          {"DENO_VERSION=2.7.14", "deno check --v8-flags=--max-old-space-size=512 Main.ts", "deno run --no-prompt --v8-flags=--max-old-space-size=128 Main.ts"},
		"duckdb":        {"DUCKDB_VERSION=1.5.2", "aonohako-duckdb-run Main.sql"},
		"elm":           {"elm-compiler", `"elm/json": "1.1.3"`, "HOME=/usr/local/lib/aonohako/elm-home", "elm make Main.elm --output=aonohako-elm-compiled.js", "port stdin : (String -> msg) -> Sub msg"},
		"erlang":        {"Broken.erl", "erlc"},
		"factor":        {"FACTOR_VERSION=0.101", "FACTOR_SHA256=9f971e935414c0d46d9090632464d66994ee797bacc91cc8b739db3b0857a25a", "sha256sum -c -", "libstdc++6", "factor_check.factor", "/opt/factor/factor -no-user-init -no-signals -q", "aonohako-factor-parser-leak", "Broken.factor"},
		"fennel":        {"FENNEL_VERSION=1.6.1", "FENNEL_SHA256=3abde50a0e25270cbb8f9d183a0a42221875b3390ba4bf11ef8697eaa53b2787", "sha256sum -c -", "aonohako-fennel-compile Main.fnl Main.lua", "luac5.4 -p --", "compiler-diagnostic", "InvalidLua.fnl", "mathmod.fnl", "nativeleak.so", "loaders:blocked", "exact:blocked", "dotted:blocked", "test ! -e /tmp/fennel-native-leak", "fennel-signal-ready", "signal_status", "test ! -e Signal.lua", "Broken.fnl"},
		"forth":         {"gforth Main.fs -e bye", ".\" ok\" cr"},
		"freebasic":     {"FREEBASIC_VERSION=1.10.1", "libtinfo5", "fbc -x Main Main.bas"},
		"gdl":           {"aonohako-gdl-run", "Main.pro"},
		"gforth":        {"gforth Main.fs -e bye", ".\" ok\" cr"},
		"gleam":         {"GLEAM_VERSION=1.16.0", "aonohako-gleam-run ."},
		"gnucobol":      {"gnucobol", "cobc -x -free -O2 -o Main Main.cob"},
		"golfscript":    {"golfscript_sandboxed.rb", "Main.gs"},
		"graphql":       {"graphql-core==3.2.6", "aonohako-graphql-run Main.graphql"},
		"guile":         {"guile-3.0=3.0.10+really3.0.10-4", "guile-3.0-libs=3.0.10+really3.0.10-4", "guile_check.scm", "GUILE_AUTO_COMPILE=0", "--no-auto-compile --no-debug -q -s", "aonohako-guile-compile-leak", "test ! -e aonohako-guile-compile-leak", "Broken.scm"},
		"fstar":         {"FSTAR_VERSION=2026.06.28", "fstar-v${FSTAR_VERSION}-Linux-x86_64.tar.gz", "fstar.exe Main.fst", "Lemma (1 + 1 == 2)"},
		"hare":          {"hare build -o Main Main.ha", "fmt::println"},
		"idris2":        {"IDRIS2_VERSION=0.8.0", "make bootstrap SCHEME=chezscheme PREFIX=/usr/local", "idris2 --cg chez -o Main Main.idr", "test -d build/exec/Main_app"},
		"haxe":          {"haxe -main Main -neko Main.n", "neko Main.n"},
		"isabelle":      {"ISABELLE_VERSION=Isabelle2025-2", "www.cl.cam.ac.uk/research/hvg/Isabelle/dist", "sha256sum -c -", "isabelle process_theories -o naproche_server=false -D ."},
		"j":             {"J_VERSION=963", "J_SOURCE_COMMIT=a9728936d7bd7743024db26a8fcb72e5f2fcadac", "ff5b91383265d27e2b9edb4c934ba1b642784417196ba6c2b09a2661c2a173d1", "aonohako-j Main.ijs", "echo 'ok'"},
		"janet":         {"JANET_VERSION=1.41.2", "janet Main.janet"},
		"javascript":    {"node --disable-wasm-trap-handler --max-old-space-size=64 --max-semi-space-size=1 --stack-size=2048 Main.js"},
		"groovy":        {"Broken.groovy", "groovyc", "java -Xmx128m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxDirectMemorySize=16m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m -Dfile.encoding=UTF-8 -DONLINE_JUDGE=1 -cp \"${groovy_cp}\" Main"},
		"java":          {"javac --release 11 -encoding UTF-8 Main.java", "jar cfm Main.jar MANIFEST.MF Main.class", "java -XX:ReservedCodeCacheSize=64m -XX:-UseCompressedClassPointers -Xmx128m -Xss1m -XX:MaxDirectMemorySize=16m -XX:MaxMetaspaceSize=64m -Dfile.encoding=UTF-8 -XX:+UseSerialGC -DONLINE_JUDGE=1 -jar Main.jar"},
		"kframework":    {"KFRAMEWORK_VERSION=7.1.337", "kframework_${KFRAMEWORK_VERSION}_amd64_ubuntu_noble.deb", "aonohako-kframework-check Main.k", "imports INT"},
		"koka":          {"KOKA_VERSION=3.2.3", "KOKA_SHA256=e82a4b497f1f8791ee171d06c45293ba16432e485d645ddd9688bafa6ccde5a5", "sha256sum -c -", "--no-autoinstall", "--cc=/usr/bin/gcc-16", "--ccopts=-march=x86-64 -mtune=generic", "--cclinkopts=-march=x86-64 -mtune=generic", "x86 ISA needed: x86-64-baseline", "evil/libevil.so", "test ! -e Broken"},
		"kotlin-jvm":    {"KOTLIN_JVM_VERSION=2.3.21", "default-jdk-headless", "kotlinc -jvm-target 1.8 Main.kt Helper.java -include-runtime -d Main.jar", "javac --release 8 -cp Main.jar Helper.java", "jar uf Main.jar Helper.class", "java -Xms64m -Xmx128m -Xss1m -XX:+UseSerialGC -XX:MaxDirectMemorySize=16m -XX:MaxMetaspaceSize=64m -XX:CompressedClassSpaceSize=64m -XX:ReservedCodeCacheSize=32m -DONLINE_JUDGE=1 -jar Main.jar"},
		"lean4":         {"LEAN_VERSION=4.29.1", "curl --retry 6", "wget --tries=6", "lean Main.lean"},
		"lolcode":       {"LCI_VERSION=0.11.2", "cb1065936d3a7463928dcddfc345a8d7d8602678394efc0e54981f9dd98c27d2", "lci Main.lol", `VISIBLE "ok"`},
		"malbolge":      {"python3 /usr/local/lib/aonohako/malbolge.py Main.mal", "Hello World!"},
		"mercury":       {"dl.mercurylang.org/deb/ trixie main", "mercury-recommended", "mmc --make --grade hlc.gc main"},
		"mojo":          {"mojo==1.0.0", "mojo build Main.mojo"},
		"moonbit":       {"MOONBIT_VERSION=0.10.9+6e6c44045", "MOONBIT_ARCHIVE_SHA256=0e81deb35eca29e892415cf954ea42b48a43bcf277ad36a3ae1e97d2d1dfe732", "MOONBIT_CORE_SHA256=d92b84ea0bc11ec9a9fe57d313416a6694a5826f4307e8e65d5075079ee913ca", "sha256sum -c -", "bundle --warn-list -a --all --target native", "moon build --target-dir .aonohako-moonbit-build --target native --release --strip --frozen --jobs 1", "Broken"},
		"nim":           {"nim c", "Broken.nim"},
		"objective-c":   {"clang -x objective-c -O2 -pipe Main.m -o Main -L/usr/lib/gcc/x86_64-linux-gnu/16 -lobjc", "libobjc-16-dev"},
		"objective-cpp": {"clang++ -x objective-c++ -O2 -pipe Main.mm -o Main -L/usr/lib/gcc/x86_64-linux-gnu/16 -lobjc", "libobjc-16-dev"},
		"objectpascal":  {"fpc -Mobjfpc -O2 -Xs -oMain Main.pas", "TOk = class", "Broken.pas"},
		"octave":        {"octave-cli --quiet", "Main.m"},
		"odin":          {"ODIN_VERSION=dev-2026-04", "odin build . -out:Main"},
		"pascal":        {"fpc", "Broken.pas"},
		"picolisp":      {"picolisp", "pil -version -bye", "Main.l", "printf '1 2\\n'", "pil Main.l -bye", "grep '^3$'", "Broken.l"},
		"pony":          {"PONY_VERSION=0.69.1", "PONY_ARCHIVE_ROOT=0.69.1-38f9f11", "PONY_SHA256=8e1955ed1a63444ae13666031d5d3909cacfb475ca96643e878f36cf4edff9ab", "sha256sum -c -", "--cpu=generic", "--ponymaxthreads=1", "objdump -d Main", "test ! -e /opt/pony/bin/pony-doc", "test ! -e Broken"},
		"posix-sh":      {"0.5.12-12", "/bin/dash -n SyntaxOnly.sh", "test ! -e /tmp/aonohako-posix-shell-syntax-leak", "Broken.sh"},
		"zsh":           {"5.9-8+b23", "/usr/bin/zsh -d -f -n SyntaxOnly.zsh", `test ! -e "${startup_leak}"`, "test ! -e /tmp/aonohako-zsh-syntax-leak", "printf '1 2\\n' | /usr/bin/zsh -d -f Main.zsh", "Broken.zsh"},
		"fish":          {"4.0.2-1", "/usr/bin/fish --no-config --private --no-execute SyntaxOnly.fish", `test ! -e "$startup_leak"`, "test ! -e /tmp/aonohako-fish-syntax-leak", "printf '1 2\\n' | /usr/bin/fish --no-config --private Main.fish", "Broken.fish"},
		"powershell":    {"POWERSHELL_VERSION=7.6.5", "POWERSHELL_SHA256=b34ab3b19acac1d3d4d0d3cfdb02acf62f457b0b6a962ff008132033f7566844", "sha256sum -c -", "Parser]::ParseFile", "ForEach-Object -Parallel", "test ! -e /opt/microsoft/powershell/7/createdump", "Broken.ps1"},
		"qbasic":        {"fbc -lang qb -x Main Main.bas", "PRINT \"ok\""},
		"raku":          {"raku -c Main.raku", "raku Main.raku"},
		"racket":        {"raco make", "Broken.rkt"},
		"rocq":          {"rocq c Main.v", "coqc -q Main.v"},
		"scheme":        {"chibi-scheme Main.scm", "(scheme base)"},
		"sed":           {"sed -f Main.sed", "s/^/ok/"},
		"sml":           {"MLTON_VERSION=20241230", "mlton -output Main Main.sml"},
		"smalltalk":     {"GST_VERSION=3.2.5", "mirrors.kernel.org/gnu/smalltalk", "sed -i 's/const char \\*inbuf;/char *inbuf;/'", "CC=/usr/bin/gcc-14 ./configure", "make -j1", "gst -q Main.st"},
		"systemverilog": {"iverilog -g2012", "Main.sv"},
		"tcl":           {"tclsh Main.tcl", "puts \"ok\""},
		"tla":           {"TLA_VERSION=1.7.4", "install -d -m 0755 /usr/local/lib/aonohako", "curl --retry 6", "wget --tries=6", "aonohako-tla-run Main.tla"},
		"uiua":          {"UIUA_VERSION=0.18.1", "UIUA_SHA256=83ce782e1c843937fee1aae1dc7db0480bed425a88fe0c18109df7a0d1970470", "sha256sum -c -", "uiua run Main.ua --no-format"},
		"vala":          {"valac --define=ONLINE_JUDGE -o Main Main.vala", "Broken.vala"},
		"vb6":           {"aonohako-vb6-run Main.bas", "Sub Main()"},
		"vbnet":         {"App.vbproj", "dotnet publish App.vbproj"},
		"verilog":       {"iverilog -g2012", "Main.v"},
		"vhdl":          {"ghdl -a --std=08", "main_tb"},
		"vlang":         {"V_VERSION=0.5.2", "86caf9e70c3342d48ef19eb4f6c47b709f18c90ae86255520d5c29df6b482e23", "v_linux.zip", "sha256sum -c -", "v -o Main Main.v"},
		"why3":          {"aonohako-why3-prove Main.mlw", "goal G: true"},
		"zig":           {"Broken.zig", "zig build-exe"},
		"zerolang":      {"ZERO_VERSION=0.3.4", "ZERO_LINUX_X64_SHA256=84a3c79d482260ee15660a49fc6b904afc927a230d05a4263039dd4dd1360e87", "zero-linux-x64", "sha256sum -c -", "ZERO_CACHE_DIR=/tmp/zerolang-cache", "zero import --out Main.graph Main.0", "zero check Main.graph", "zero build --release release-fast --out Main Main.graph", "Broken.0"},
		"r":             {"Broken.R", "parse(file=commandArgs(TRUE)[1])"},
		"rescript":      {"rescript@12.3.0", "rescript build", "node --disable-wasm-trap-handler --max-old-space-size=64 --max-semi-space-size=1 --stack-size=2048 lib/js/Main.js"},
		"fortran":       {"Broken.f90", "gfortran"},
		"d":             {"Broken.d", "ldc2"},
		"prolog":        {"Broken.pl", "swipl"},
		"scala":         {"scalac Main.scala", "java -Xmx128m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxDirectMemorySize=16m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m -Dfile.encoding=UTF-8 -DONLINE_JUDGE=1 -cp \"${scala_cp}\" Main"},
		"lisp":          {"Broken.lisp", "sbcl"},
		"nasm":          {"Main.asm", "Broken.asm", "nasm -felf64"},
		"coq":           {"Broken.v", "coqc"},
		"cpp": {
			`std::cout << "ok\n";`,
			"g++ -O2 -pipe Main.cpp -o Main",
			`#include "testlib.h"`,
			"registerTestlibCmd",
			"g++ -std=c++17 -O2 -pipe Checker.cpp -o Checker",
			"./Checker input.txt output.txt answer.txt",
		},
		"python":     {"import qiskit", `"/usr/local/lib/aonohako/python" in os.environ.get("PYTHONPATH", "")`},
		"purescript": {"purescript@0.15.16", "spago@1.0.4", "registry: 77.4.1", "spago build", `require("./output/Main/index.js").main();`},
		"typescript": {"declare const require: any;", "const fs = require('fs');", "tsc Main.ts --module commonjs --target es2019 --outDir dist", "node --disable-wasm-trap-handler --max-old-space-size=64 --max-semi-space-size=1 --stack-size=2048 dist/Main.js"},
		"wasm":       {"-W max-memory-size=33554432", "-W max-wasm-stack=1048576", "-W trap-on-grow-failure=y"},
	}
	tests["chicken-scheme"] = []string{"chicken-bin=5.3.0-2", "libchicken-dev=5.3.0-2", "csc -O3 -d0 -no-trace -static -o Main Main.scm", "readelf -d Main", "aonohako-chicken-compile-leak", "test ! -e aonohako-chicken-compile-leak", "Broken.scm", "test ! -e Broken"}
	tests["assemblyscript"] = []string{"ASSEMBLYSCRIPT_VERSION=0.28.20", "ASSEMBLYSCRIPT_SHA256=26696c1bbb716bd85a0fbd38c7efbda186c83bc648ba4f78369d432081b4b1cb", "ASSEMBLYSCRIPT_WASI_SHIM_SHA256=e8b4410255e6f86cb96c2ce1d191fa9a6b45d274f0f506c4132e8075634ea005", "BINARYEN_SHA256=ed375d90d259924799147788d66ed70ce6e65454b69df577e09f2c4d17c00f6c", "LONG_SHA256=68033e466773df7d52c9e59341bb729d83716cd920c56460395724456d646b26", "npm install --global --offline --ignore-scripts", "aonohako-assemblyscript-compile", "wasm-validate Main.wasm", "wasmtime run", "Broken.ts"}

	for language, patterns := range tests {
		spec, ok := ciByLanguage[language]
		if !ok {
			t.Fatalf("missing CI image for language %q", language)
		}
		body := strings.Join(append(append(append(append([]string{}, spec.AptPackages...), spec.PipPackages...), spec.NPMPackages...), append(spec.InstallScript, spec.SmokeCommand...)...), "\n")
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

func TestRepositoryCatalogIncludesMalbolgeRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	spec, ok := catalog.Languages["malbolge"]
	if !ok {
		t.Fatal("malbolge language missing from catalog")
	}
	if !slices.Contains(spec.Install.Apt, "python3") {
		t.Fatalf("malbolge apt packages = %v, want python3", spec.Install.Apt)
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{"Main.mal", "/usr/local/lib/aonohako/malbolge.py", "Hello World!"} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("malbolge smoke must contain %q, got %q", marker, smoke)
		}
	}
}

func TestRepositoryCatalogUsesOfficialMojoRelease(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	mojoScript := strings.Join(catalog.Languages["mojo"].Install.Script, "\n")
	if !strings.Contains(mojoScript, "pip install --no-cache-dir mojo==1.0.0") {
		t.Fatalf("mojo install script must use official PyPI 1.0.0 release:\n%s", mojoScript)
	}
	if strings.Contains(mojoScript, "modular.gateway.scarf.sh") || strings.Contains(mojoScript, "--extra-index-url") {
		t.Fatalf("mojo install script must not use the obsolete extra index:\n%s", mojoScript)
	}
}

func TestRepositoryCatalogVerifiesUiuaArchive(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	uiuaScript := strings.Join(catalog.Languages["uiua"].Install.Script, "\n")
	for _, marker := range []string{
		"UIUA_VERSION=0.18.1",
		"UIUA_SHA256=83ce782e1c843937fee1aae1dc7db0480bed425a88fe0c18109df7a0d1970470",
		"sha256sum -c -",
	} {
		if !strings.Contains(uiuaScript, marker) {
			t.Fatalf("uiua install script must contain %q:\n%s", marker, uiuaScript)
		}
	}
}

func TestRepositoryCatalogPinsMoonBitToolchainAndOfflineBuild(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	spec, ok := catalog.Languages["moonbit"]
	if !ok {
		t.Fatal("moonbit language missing from catalog")
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"MOONBIT_VERSION=0.10.9+6e6c44045",
		"MOONBIT_ARCHIVE_SHA256=0e81deb35eca29e892415cf954ea42b48a43bcf277ad36a3ae1e97d2d1dfe732",
		"MOONBIT_CORE_SHA256=d92b84ea0bc11ec9a9fe57d313416a6694a5826f4307e8e65d5075079ee913ca",
		"sha256sum -c -",
		"bundle --warn-list -a --all --target native",
	} {
		if !strings.Contains(install, marker) {
			t.Fatalf("MoonBit install must contain %q:\n%s", marker, install)
		}
	}
	if strings.Contains(install, "/latest/") || strings.Contains(install, "install/unix.sh") {
		t.Fatalf("MoonBit install must not use a drifting installer:\n%s", install)
	}
	if !slices.Contains(spec.Install.SandboxTools, "ar") || !strings.Contains(install, "ln -sfn /usr/bin/ar /usr/local/bin/ar") {
		t.Fatalf("MoonBit must expose its pinned native archiver path: tools=%v\n%s", spec.Install.SandboxTools, install)
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{"--target native", "--frozen", "--jobs 1"} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("MoonBit smoke must contain %q: %s", marker, smoke)
		}
	}
}

func TestRepositoryCatalogPinsAndHardensFennelAOT(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	spec, ok := catalog.Languages["fennel"]
	if !ok {
		t.Fatal("fennel language missing from catalog")
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"FENNEL_VERSION=1.6.1",
		"FENNEL_SHA256=3abde50a0e25270cbb8f9d183a0a42221875b3390ba4bf11ef8697eaa53b2787",
		"sha256sum -c -",
	} {
		if !strings.Contains(install, marker) {
			t.Fatalf("Fennel install must contain %q:\n%s", marker, install)
		}
	}
	if !slices.Contains(spec.Install.SandboxTools, "fennel") || !slices.Contains(spec.Install.SandboxTools, "lua") || !slices.Contains(spec.Install.SandboxTools, "luac5.4") {
		t.Fatalf("Fennel compiler/runtime tools = %v", spec.Install.SandboxTools)
	}
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "fennel_compile.sh"))
	if err != nil {
		t.Fatal(err)
	}
	helperBody := string(helper)
	for _, marker := range []string{
		"--no-fennelrc /usr/local/lib/aonohako/fennel_writer.fnl",
		"on_signal()",
		"trap - 0 1 2 3 15",
		"exit $((128 + signal_number))",
		"trap cleanup 0",
		"trap 'on_signal 15' 15",
		"mktemp -d \"${target_dir}/.aonohako-fennel.XXXXXX\"",
		">\"${compiler_stdout_path}\"",
		"fennel compiler emitted unexpected stdout",
		"luac5.4 -p -- \"${compiled_path}\"",
		"luac5.4 -p -- \"${guarded_path}\"",
		"mv -f -- \"${guarded_path}\" \"${target_path}\"",
		"os.execute = nil",
		"io.popen = nil",
		"package.loaded.debug = nil",
		"for name in pairs(package.preload) do package.preload[name] = nil end",
		"package.loadlib = nil",
		"package.cpath = ''",
		"package.searchers[4] = nil",
		"package.searchers[3] = nil",
		"package.loaders[4] = nil",
		"package.loaders[3] = nil",
		"debug = nil",
	} {
		if !strings.Contains(helperBody, marker) {
			t.Fatalf("Fennel helper must contain %q:\n%s", marker, helperBody)
		}
	}
	if strings.Contains(helperBody, "--compile") || strings.Contains(helperBody, `> "${target_path}"`) {
		t.Fatalf("Fennel helper must not derive the final artifact from CLI stdout:\n%s", helperBody)
	}
	trapIndex := strings.Index(helperBody, "trap cleanup 0")
	tempDirIndex := strings.Index(helperBody, "temp_dir=$(mktemp -d")
	if trapIndex < 0 || tempDirIndex < 0 || trapIndex >= tempDirIndex {
		t.Fatalf("Fennel helper must install cleanup before creating its temp directory:\n%s", helperBody)
	}
	writer, err := os.ReadFile(filepath.Join("..", "..", "scripts", "fennel_writer.fnl"))
	if err != nil {
		t.Fatal(err)
	}
	writerBody := string(writer)
	for _, marker := range []string{"(require :fennel)", "fennel.compile-string", ":requireAsInclude true", ":useMetadata false", ":extra-compiler-env {:print compiler-print}", "(io.stderr:write", "  nil)"} {
		if !strings.Contains(writerBody, marker) {
			t.Fatalf("Fennel writer must contain %q:\n%s", marker, writerBody)
		}
	}
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"scripts/fennel_compile.sh /usr/local/bin/aonohako-fennel-compile",
		"scripts/fennel_writer.fnl /usr/local/lib/aonohako/fennel_writer.fnl",
	} {
		if !strings.Contains(string(dockerfile), marker) {
			t.Fatalf("runtime Dockerfile must contain %q", marker)
		}
	}
}

func TestAssemblyScriptCompilerBypassesAscProcessRespawn(t *testing.T) {
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "assemblyscript_compile.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(helper)
	for _, marker := range []string{
		"exec /usr/local/bin/node",
		"--enable-source-maps",
		"/usr/local/lib/node_modules/assemblyscript/bin/asc.js",
		"cd /usr/local/lib/node_modules/@assemblyscript/wasi-shim",
		"--outFile \"${target_path}\"",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("AssemblyScript compiler helper must contain %q:\n%s", marker, body)
		}
	}
	if strings.Contains(body, "exec /usr/local/bin/asc") {
		t.Fatalf("AssemblyScript compiler helper must bypass asc's process-spawning launcher:\n%s", body)
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "scripts/assemblyscript_compile.sh /usr/local/bin/aonohako-assemblyscript-compile") {
		t.Fatal("runtime Dockerfile must install the trusted AssemblyScript compiler helper")
	}
}

func TestFactorParserHelperNeverRunsSubmittedTopLevelForms(t *testing.T) {
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "factor_check.factor"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(helper)
	for _, marker := range []string{"command-line get last", "parse-file", "drop"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("Factor parser helper must contain %q: %s", marker, body)
		}
	}
	for _, forbidden := range []string{"run-file", "load", "eval"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Factor parser helper must not contain %q: %s", forbidden, body)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "scripts/factor_check.factor /usr/local/lib/aonohako/factor_check.factor") {
		t.Fatal("runtime Dockerfile must install the trusted Factor parser helper")
	}
}

func TestChezSchemeReaderHelperNeverRunsSubmittedTopLevelForms(t *testing.T) {
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "chez_scheme_check.scm"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(helper)
	for _, marker := range []string{"call-with-input-file", "read port", "eof-object?"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("Chez Scheme reader helper must contain %q: %s", marker, body)
		}
	}
	for _, forbidden := range []string{"load", "eval", "system"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Chez Scheme reader helper must not contain %q: %s", forbidden, body)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "scripts/chez_scheme_check.scm /usr/local/lib/aonohako/chez_scheme_check.scm") {
		t.Fatal("runtime Dockerfile must install the trusted Chez Scheme reader helper")
	}
}

func TestGuileReaderHelperNeverRunsSubmittedTopLevelForms(t *testing.T) {
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "guile_check.scm"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(helper)
	for _, marker := range []string{"call-with-input-file", "read port", "eof-object?"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("Guile reader helper must contain %q: %s", marker, body)
		}
	}
	for _, forbidden := range []string{"load", "eval", "system"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Guile reader helper must not contain %q: %s", forbidden, body)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "scripts/guile_check.scm /usr/local/lib/aonohako/guile_check.scm") {
		t.Fatal("runtime Dockerfile must install the trusted Guile reader helper")
	}
}

func TestChezSchemeInstallIsSharedWithIdris2(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	shared, ok := catalog.SharedInstalls["chezscheme10"]
	if !ok {
		t.Fatal("chezscheme10 shared install missing from catalog")
	}
	if !slices.Contains(shared.Apt, "chezscheme=10.0.0+dfsg-5") {
		t.Fatalf("chezscheme10 apt packages = %v", shared.Apt)
	}
	for _, language := range []string{"chez-scheme", "idris2"} {
		if !slices.Contains(catalog.Languages[language].Install.Shared, "chezscheme10") {
			t.Fatalf("%s must reuse the pinned chezscheme10 install", language)
		}
		if slices.Contains(catalog.Languages[language].Install.Apt, "chezscheme") {
			t.Fatalf("%s duplicates the shared Chez Scheme package", language)
		}
	}
}

func TestChickenSchemeCatalogPinsStaticChickenRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := catalog.Languages["chicken-scheme"]
	if !ok {
		t.Fatal("chicken-scheme language missing from catalog")
	}
	for _, pkg := range []string{"chicken-bin=5.3.0-2", "libchicken-dev=5.3.0-2"} {
		if !slices.Contains(spec.Install.Apt, pkg) {
			t.Fatalf("Chicken Scheme apt packages = %v, missing %s", spec.Install.Apt, pkg)
		}
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{"-static -o Main", "readelf -d Main > Main.readelf", `Shared library: \[libchicken`, `test "${grep_status}" -eq 1`, "test ! -e aonohako-chicken-compile-leak", "Broken.scm"} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("Chicken Scheme smoke must contain %q", marker)
		}
	}
	if strings.Contains(smoke, "grep -q '(NEEDED)'") {
		t.Fatal("Chicken Scheme -static must not be treated as fully static system linkage")
	}
}

func TestRepositoryCatalogPinsAndBoundsChapelRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	spec, ok := catalog.Languages["chapel"]
	if !ok {
		t.Fatal("chapel language missing from catalog")
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"CHAPEL_VERSION=2.9.0",
		"CHAPEL_DEB_SHA256=11f93de9e725a7c74608b4afcc7c8fc8bec380f27b09883cd6f69d6fbe66e13d",
		"chapel-${CHAPEL_VERSION}-1.debian13.amd64.deb",
		"sha256sum -c -",
		"chmod 0750",
		"mason",
		"/usr/share/chapel/2.9/tools/c2chapel/c2chapel.py",
		"/usr/share/chapel/2.9/tools/chpl-language-server/src/chpl-shim.py",
		"/usr/share/chapel/2.9/tools/chplcheck/chplcheck",
		"find /usr/lib/chapel/2.9/third-party/chpl-venv -type f -perm /0111 -exec chmod 0750 {} +",
		`test "$(find /usr/lib/chapel/2.9/third-party/chpl-venv -type f -perm /0111 | wc -l)" -eq 37`,
		"/usr/share/chapel/2.9/modules/dists/fixDistDocs.perl",
		"/usr/share/chapel/2.9/modules/internal/fixInternalDocs.sh",
		"test -d /usr/lib/chapel/2.9/third-party",
		"find /usr/lib/chapel/2.9/third-party -path '*/bin/*' -type f -perm /0111 -exec chmod 0750 {} +",
	} {
		if !strings.Contains(install, marker) {
			t.Fatalf("Chapel install must contain %q:\n%s", marker, install)
		}
	}
	if !slices.Contains(spec.Install.SandboxTools, "chpl") {
		t.Fatalf("Chapel sandbox tools = %v", spec.Install.SandboxTools)
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{"set -euo pipefail", "chpl-language-server/src/chpl-shim.py", "chpl-venv", "fixDistDocs.perl", "fixInternalDocs.sh", "third-party", "-type l", "-perm /0001", "CHPL_COMM=none", "CHPL_TASKS=qthreads", "CHPL_TARGET_CPU=none", "CHPL_RT_NUM_THREADS_PER_LOCALE=1", "--local", "here.maxTaskPar", "./Main -nl 1", "./Main -nl 2", "test -x Main", "test ! -e Broken"} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("Chapel smoke must contain %q:\n%s", marker, smoke)
		}
	}
}

func TestRepositoryCatalogPinsAndHardensAlgol68Runtime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	spec, ok := catalog.Languages["algol68"]
	if !ok {
		t.Fatal("algol68 language missing from catalog")
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"A68G_VERSION=3.13.3",
		"A68G_SHA256=78dc53f4a712a9c8ee159b1eb7045fe4ea060c4eb2a49efb9634f83c2cb13995",
		"A68G_SHA512=90b64911ee3b4011799425cf846fea4a26570182ec1ed9f65e6f0630e02b0623ed408fb6d074dcd2795b6412e209a39f5580d32acd079a3f3d419bf2440512a6",
		"algol68g-${A68G_VERSION}.tar.gz/sha512/${A68G_SHA512}/algol68g-${A68G_VERSION}.tar.gz",
		"sha256sum -c -",
		"CPPFLAGS=-DAONOHAKO_SAFE_RUNTIME=1 ./configure --enable-core",
		"aonohako_disabled_system",
		"aonohako_disabled_fork",
		"aonohako_disabled_execve",
		"read_rc_options ();",
		"read_env_options ();",
		"process execution is disabled",
		"system|fork|execve|dlopen|dlsym",
	} {
		if !strings.Contains(install, marker) {
			t.Fatalf("Algol 68 install must contain %q:\n%s", marker, install)
		}
	}
	if !slices.Contains(spec.Install.SandboxTools, "a68g") {
		t.Fatalf("Algol 68 sandbox tools = %v", spec.Install.SandboxTools)
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{
		"set -euo pipefail",
		"A68G_OPTIONS='-O1 --compile --debug'",
		"printf '%s\\n' '-O1 --compile --debug' > .a68grc",
		"-name 'Main.c' -o -name 'Main.so' -o -name 'Main.o' -o -name 'Main.sh' -o -name 'Main.lst'",
		"a68g --quiet --no-compile -O0 --check --file Main.a68 --no-pragmats",
		"a68g --quiet --no-compile -O0 --run --file Main.a68 --no-pragmats",
		"PR COMPILE PR",
		"system (\"touch /tmp/a68g-system-leak\")",
		"test ! -e /tmp/a68g-system-leak",
		"tag \"fork\" has not been declared properly",
		"tag \"exec\" has not been declared properly",
		"DO touch /tmp/a68g-monitor-leak",
		"process execution is disabled",
		"test ! -e /tmp/a68g-monitor-leak",
		"Broken.a68",
	} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("Algol 68 smoke must contain %q:\n%s", marker, smoke)
		}
	}
}

func TestRepositoryCatalogPinsAndHardensKokaRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	spec, ok := catalog.Languages["koka"]
	if !ok {
		t.Fatal("koka language missing from catalog")
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"KOKA_VERSION=3.2.3",
		"KOKA_SHA256=e82a4b497f1f8791ee171d06c45293ba16432e485d645ddd9688bafa6ccde5a5",
		"sha256sum -c -",
		"chown -R root:root /opt/koka",
		"find /opt/koka -type f -exec chmod 0644",
		"chmod 0755 /opt/koka/bin/koka",
	} {
		if !strings.Contains(install, marker) {
			t.Fatalf("Koka install must contain %q:\n%s", marker, install)
		}
	}
	if !slices.Contains(spec.Install.SandboxTools, "koka") {
		t.Fatalf("Koka sandbox tools = %v", spec.Install.SandboxTools)
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{
		"set -euo pipefail",
		"test \"$(find /opt/koka -type f -perm /0111 | wc -l)\" -eq 1",
		"! command -v conan",
		"! command -v vcpkg",
		"--no-autoinstall",
		"--cc=/usr/bin/gcc-16",
		"--ccopts=-march=x86-64 -mtune=generic",
		"--cclinkopts=-march=x86-64 -mtune=generic",
		"--builddir=.aonohako-koka-build",
		"--output=Main main.kk",
		"test \"$(stat -c %a Main)\" = 644",
		"x86 ISA needed: x86-64-baseline",
		"(RPATH|RUNPATH)",
		"evil/libevil.so",
		"test ! -e Broken",
	} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("Koka smoke must contain %q:\n%s", marker, smoke)
		}
	}
}

func TestRepositoryCatalogPinsAndHardensPonyRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	spec, ok := catalog.Languages["pony"]
	if !ok {
		t.Fatal("pony language missing from catalog")
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"PONY_VERSION=0.69.1",
		"PONY_ARCHIVE_ROOT=0.69.1-38f9f11",
		"PONY_SHA256=8e1955ed1a63444ae13666031d5d3909cacfb475ca96643e878f36cf4edff9ab",
		"sha256sum -c -",
		"libponyrt-pic.a",
		"crtbeginS.o",
		"crtendS.o",
		"chmod 0755 /opt/pony/bin/ponyc",
		"chmod 0750",
	} {
		if !strings.Contains(install, marker) {
			t.Fatalf("Pony install must contain %q:\n%s", marker, install)
		}
	}
	if !slices.Contains(spec.Install.SandboxTools, "ponyc") {
		t.Fatalf("Pony sandbox tools = %v", spec.Install.SandboxTools)
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{
		"set -euo pipefail",
		"test \"$(find /opt/pony -type f -perm /0111 | wc -l)\" -eq 1",
		"test ! -e /opt/pony/bin/pony-doc",
		"-perm /0001",
		"--cpu=generic",
		"--ponymaxthreads=1",
		"%(ymm|zmm)",
		"[[:space:]]v[a-z0-9]+[[:space:]]",
		"GNU_STACK",
		"GNU_RELRO",
		"test ! -e Broken",
	} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("Pony smoke must contain %q:\n%s", marker, smoke)
		}
	}
}

func TestRepositoryCatalogHardensDedicatedShellRuntimeVariants(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	shared, ok := catalog.SharedInstalls["shell_runtime"]
	if !ok {
		t.Fatal("shell_runtime shared install missing from catalog")
	}
	for _, pkg := range []string{"bash=5.2.37-2+b9", "dash=0.5.12-12", "zsh=5.9-8+b23", "zsh-common=5.9-8", "fish=4.0.2-1", "fish-common=4.0.2-1", "diffutils", "findutils", "grep", "mawk", "sed"} {
		if !slices.Contains(shared.Apt, pkg) {
			t.Fatalf("shell runtime apt packages = %v, want %q", shared.Apt, pkg)
		}
	}
	for _, tool := range []string{"bash", "dash", "zsh", "fish"} {
		if !slices.Contains(shared.SandboxTools, tool) {
			t.Fatalf("shell runtime sandbox tools = %v, want %q", shared.SandboxTools, tool)
		}
	}
	for language, source := range map[string]string{"bash": "SyntaxOnly.sh", "posix-sh": "SyntaxOnly.sh", "zsh": "SyntaxOnly.zsh", "fish": "SyntaxOnly.fish"} {
		spec, ok := catalog.Languages[language]
		if !ok || !slices.Contains(spec.Install.Shared, "shell_runtime") {
			t.Fatalf("%s runtime spec = %+v", language, spec)
		}
		smoke := strings.Join(spec.Smoke.Command, "\n")
		for _, marker := range []string{source, "test ! -e", "Broken."} {
			if !strings.Contains(smoke, marker) {
				t.Fatalf("%s smoke must contain %q:\n%s", language, marker, smoke)
			}
		}
	}
	typeX := catalog.Profiles["type-x"]
	if !reflect.DeepEqual(typeX.Languages, []string{"bash", "fish", "posix-sh", "zsh"}) || len(typeX.Install.Script) != 0 {
		t.Fatalf("type-x profile = %+v", typeX)
	}

	hardener, err := os.ReadFile(filepath.Join("..", "..", "scripts", "harden_shell_runtime.sh"))
	if err != nil {
		t.Fatal(err)
	}
	hardenerBody := string(hardener)
	for _, marker := range []string{
		"set -euo pipefail",
		"/usr/local/go/pkg/tool",
		"chmod 0750",
		"chmod -R go-rwx",
		"-perm /6000",
		"chmod ug-s",
		`for root in /usr/lib /usr/local/lib`,
		`library_inventory="$(mktemp)"`,
		`find "${root}" -xdev -type f -perm /0001 -print0 > "${library_inventory}"`,
		`magic="$(od -An -tx1 -N4 -- "${path}" | tr -d '[:space:]')"`,
		`"${magic}" != "7f454c46"`,
		`chmod go-x "${path}"`,
		"ld-linux*.so*",
		"shell_runtime_allowlist.txt",
	} {
		if !strings.Contains(hardenerBody, marker) {
			t.Fatalf("shell hardener must contain %q:\n%s", marker, hardenerBody)
		}
	}

	allowlist, err := os.ReadFile(filepath.Join("..", "..", "scripts", "shell_runtime_allowlist.txt"))
	if err != nil {
		t.Fatal(err)
	}
	allowed := string(allowlist)
	for _, path := range []string{"/usr/local/bin/aonohako\n", "/usr/local/bin/aonohako-selftest\n", "/usr/bin/bash\n", "/usr/bin/dash\n", "/usr/bin/zsh\n", "/usr/bin/fish\n", "/usr/bin/grep\n", "/usr/bin/cp\n", "/usr/bin/sleep\n"} {
		if !strings.Contains(allowed, path) {
			t.Fatalf("shell allowlist must contain %q:\n%s", path, allowed)
		}
	}
	for _, path := range []string{"/usr/local/go/bin/go", "/usr/bin/apt", "/usr/bin/dpkg", "/usr/bin/curl", "/usr/bin/setpriv", "/usr/bin/unshare"} {
		if strings.Contains(allowed, path) {
			t.Fatalf("shell allowlist unexpectedly exposes %q", path)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dockerBody := string(dockerfile)
	for _, marker := range []string{"scripts/harden_shell_runtime.sh", `"${IMAGE_NAME}" == "type-x"`, `"${IMAGE_NAME}" == "ci-bash"`, `"${IMAGE_NAME}" == "ci-posix-sh"`, `"${IMAGE_NAME}" == "ci-zsh"`, `"${IMAGE_NAME}" == "ci-fish"`, `",fish,"`, "/var/empty/.config/fish"} {
		if !strings.Contains(dockerBody, marker) {
			t.Fatalf("runtime Dockerfile must contain %q", marker)
		}
	}
}

func TestRepositoryCatalogPinsAndHardensPowerShellInDotnetProfile(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	spec, ok := catalog.Languages["powershell"]
	if !ok {
		t.Fatal("powershell language missing from catalog")
	}
	if !slices.Contains(spec.Install.Apt, "libicu76") || !slices.Contains(spec.Install.SandboxTools, "pwsh") {
		t.Fatalf("PowerShell install = %+v", spec.Install)
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"POWERSHELL_VERSION=7.6.5",
		"POWERSHELL_SHA256=b34ab3b19acac1d3d4d0d3cfdb02acf62f457b0b6a962ff008132033f7566844",
		"sha256sum -c -",
		"rm -f /opt/microsoft/powershell/7/createdump",
		"chown -R root:root /opt/microsoft/powershell/7",
		"find /opt/microsoft/powershell/7 -type f -exec chmod 0644",
		"chmod 0755 /opt/microsoft/powershell/7/pwsh",
	} {
		if !strings.Contains(install, marker) {
			t.Fatalf("PowerShell install must contain %q:\n%s", marker, install)
		}
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{
		"set -euo pipefail",
		"HOME=/var/empty",
		"XDG_CONFIG_HOME=/var/empty/.config",
		"XDG_DATA_HOME=/var/empty/.local/share",
		"PSModulePath=/opt/microsoft/powershell/7/Modules",
		"POWERSHELL_TELEMETRY_OPTOUT=1",
		"POWERSHELL_UPDATECHECK=Off",
		"Parser]::ParseFile",
		"ForEach-Object -Parallel",
		"test ! -e /tmp/aonohako-powershell-syntax-leak",
		"test ! -e /opt/microsoft/powershell/7/createdump",
		"stat -c '%u:%g:%a' /etc/passwd",
		"test ! -s /etc/passwd",
		"stat -c '%u:%g:%a' /etc/group",
		"/var/empty/.local/share/powershell/Modules:/usr/local/share/powershell/Modules:/opt/microsoft/powershell/7/Modules",
		"find /opt/microsoft/powershell/7 -type f -perm /0022",
		"Broken.ps1",
	} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("PowerShell smoke must contain %q:\n%s", marker, smoke)
		}
	}
	if profile := catalog.Profiles["type-e"]; !reflect.DeepEqual(profile.Languages, []string{"csharp", "fsharp", "powershell", "vbnet"}) {
		t.Fatalf("type-e languages = %v", profile.Languages)
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

func TestRepositoryCatalogPinsOfficialVReleaseArtifact(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	spec, ok := catalog.Languages["vlang"]
	if !ok {
		t.Fatalf("vlang language missing from catalog")
	}
	body := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{
		"export V_VERSION=0.5.2",
		"export V_LINUX_SHA256=86caf9e70c3342d48ef19eb4f6c47b709f18c90ae86255520d5c29df6b482e23",
		"releases/download/${V_VERSION}/v_linux.zip",
		"sha256sum -c -",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("V install script must contain %q", marker)
		}
	}
	for _, forbidden := range []string{"git clone", "V_COMMIT"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("V install script must not contain drifting bootstrap input %q", forbidden)
		}
	}
	if !slices.Contains(spec.Install.Apt, "curl") || !slices.Contains(spec.Install.Apt, "unzip") {
		t.Fatalf("V apt packages = %v, want curl and unzip", spec.Install.Apt)
	}
}

func TestRepositoryCatalogUsesTrixieAndUpdatedICUForDebianProfiles(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	const pinnedTrixie = "debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2"
	for _, profileName := range []string{"type-a", "type-b", "type-c", "type-d", "type-e", "type-f", "type-i", "type-j", "type-k", "type-l", "type-m", "type-n", "type-p", "type-q", "type-r", "type-s", "type-t"} {
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
		install := strings.Join(spec.Install.Script, "\n")
		if !strings.Contains(install, "dotnet-install.sh --version 8.0.423") {
			t.Fatalf("%s install script must pin .NET SDK 8.0.423, got %q", language, install)
		}
		if strings.Contains(install, "dotnet-install.sh --channel") {
			t.Fatalf("%s install script must not use a drifting .NET channel, got %q", language, install)
		}
		smoke := strings.Join(spec.Smoke.Command, "\n")
		for _, marker := range []string{"DOTNET_PROCESSOR_COUNT=1", "DOTNET_GCHeapHardLimit=8000000", "DOTNET_EnableDiagnostics=0", "COMPlus_EnableDiagnostics=0"} {
			if !strings.Contains(smoke, marker) {
				t.Fatalf("%s smoke command must contain %q, got %q", language, marker, smoke)
			}
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

	nodeInstall, ok := catalog.SharedInstalls["node24"]
	if !ok {
		t.Fatalf("node24 shared install missing from catalog")
	}
	if !reflect.DeepEqual(nodeInstall.Apt, []string{"curl", "xz-utils"}) {
		t.Fatalf("node24 apt packages = %v, want curl and xz-utils", nodeInstall.Apt)
	}
	installScript := strings.Join(nodeInstall.Script, "\n")
	for _, marker := range []string{
		"export NODE_VERSION=24.15.0",
		"export NODE_SHA256=472655581fb851559730c48763e0c9d3bc25975c59d518003fc0849d3e4ba0f6",
		"https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.xz",
		"sha256sum -c -",
		`ln -sfn "/opt/node-v${NODE_VERSION}-linux-x64/bin/node" /usr/local/bin/node`,
		`ln -sfn "/opt/node-v${NODE_VERSION}-linux-x64/bin/npm" /usr/local/bin/npm`,
	} {
		if !strings.Contains(installScript, marker) {
			t.Fatalf("node24 shared install must contain %q, got %q", marker, installScript)
		}
	}

	for _, language := range []string{"apl", "assemblyscript", "javascript", "purescript", "rescript", "typescript"} {
		spec, ok := catalog.Languages[language]
		if !ok {
			t.Fatalf("%s language missing from catalog", language)
		}
		if !slices.Contains(spec.Install.Shared, "node24") {
			t.Fatalf("%s shared installs = %v, want node24", language, spec.Install.Shared)
		}
	}
}

func TestRepositoryCatalogExpandsSharedInstallMarkersOncePerProductionImage(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	production, err := catalog.ProductionImages()
	if err != nil {
		t.Fatalf("ProductionImages returned error: %v", err)
	}
	images := make(map[string]ImageSpec, len(production))
	for _, image := range production {
		images[image.Name] = image
	}

	tests := []struct {
		profile string
		marker  string
	}{
		{profile: "type-a", marker: "export NODE_VERSION=24.15.0"},
		{profile: "type-a", marker: "aonohako-gforth-sid.list"},
		{profile: "type-b", marker: "export NODE_VERSION=24.15.0"},
		{profile: "type-c", marker: "libobjc-16-dev"},
		{profile: "type-c", marker: "export FREEBASIC_VERSION=1.10.1"},
	}
	for _, tc := range tests {
		image, ok := images[tc.profile]
		if !ok {
			t.Fatalf("production image %q missing", tc.profile)
		}
		if count := strings.Count(strings.Join(image.InstallScript, "\n"), tc.marker); count != 1 {
			t.Fatalf("%s marker %q count = %d, want 1", tc.profile, tc.marker, count)
		}
	}
}

func TestRepositoryCatalogPinsGCC16AcrossProfiles(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	for _, profileName := range sortedKeys(catalog.Profiles) {
		if profileName == "type-j" || profileName == "type-o" || profileName == "type-q" || profileName == "type-r" || profileName == "type-s" || profileName == "type-t" || profileName == "type-u" || profileName == "type-v" || profileName == "type-x" || profileName == "type-y" {
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
		"pillow==12.3.0",
		"six==1.17.0",
		"qiskit==2.4.0",
		"pyparsing==3.3.2",
		"pylatexenc==2.10",
		"jax[cpu]==0.10.0",
		"setuptools==80.10.2",
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
	wasmScriptBody := strings.Join(catalog.SharedInstalls["wasmtime44"].Script, "\n")
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
	if !strings.Contains(wasmScriptBody, "WASMTIME_SHA256=52eba06fe9f4364aa6164a4a3eafb2ca692ba9a756cbe8137b5574871f8cbfc8") || !strings.Contains(wasmScriptBody, "sha256sum -c -") {
		t.Fatalf("shared Wasmtime install must verify the pinned release archive")
	}
	for _, language := range []string{"assemblyscript", "wasm"} {
		if !slices.Contains(catalog.Languages[language].Install.Shared, "wasmtime44") {
			t.Fatalf("%s must reuse the shared Wasmtime install", language)
		}
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
			"chown 0:0 /usr/local/lib/aonohako/konan/cache/.lock",
			"chmod 0400 /usr/local/lib/aonohako/konan/cache/.lock",
		} {
			if !strings.Contains(body, marker) {
				t.Fatalf("ci-kotlin must prewarm readonly Kotlin/Native dependencies with %q, got %q", marker, body)
			}
		}
		return
	}

	t.Fatalf("ci-kotlin image not found")
}
