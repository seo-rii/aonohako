#!/usr/bin/env bash
set -euo pipefail

IMAGE_REF="${1:-}"

if [ -z "${IMAGE_REF}" ]; then
    echo "usage: $0 <image-ref>" >&2
    exit 1
fi

REPO_DIGEST="$(docker image inspect "${IMAGE_REF}" --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}' 2>/dev/null || true)"

echo "## Runtime Toolchain Versions"
echo
echo "- Image: \`${IMAGE_REF}\`"
if [ -n "${AONOHAKO_IMAGE_ID:-}" ]; then
    echo "- Image ID: \`${AONOHAKO_IMAGE_ID}\`"
fi
if [ -n "${AONOHAKO_LANGUAGES:-}" ]; then
    echo "- Languages: \`${AONOHAKO_LANGUAGES}\`"
fi
if [ -n "${REPO_DIGEST}" ]; then
    echo "- Repo digest: \`${REPO_DIGEST}\`"
fi
echo

DOCKER_RUN_ARGS=(run --rm -i)
if [ -n "${AONOHAKO_LANGUAGES:-}" ]; then
    DOCKER_RUN_ARGS+=(--env "AONOHAKO_LANGUAGES=${AONOHAKO_LANGUAGES}")
fi
DOCKER_RUN_ARGS+=(--entrypoint bash "${IMAGE_REF}")

docker "${DOCKER_RUN_ARGS[@]}" <<'EOF'
set -euo pipefail

declare -A enabled_languages=()
declare -A reported_tools=()

if [ -n "${AONOHAKO_LANGUAGES:-}" ]; then
    while IFS= read -r raw_language; do
        language="$(printf "%s" "${raw_language}" | tr -d '[:space:]')"
        if [ -n "${language}" ]; then
            enabled_languages["${language}"]=1
        fi
    done < <(printf "%s\n" "${AONOHAKO_LANGUAGES}" | tr ',' '\n')
fi

has_language() {
    if [ "${#enabled_languages[@]}" -eq 0 ]; then
        return 0
    fi
    [ -n "${enabled_languages[$1]:-}" ]
}

report() {
    local name="$1"
    shift
    local output

    if ! command -v "$1" >/dev/null 2>&1; then
        output="<not installed>"
    elif output="$("$@" </dev/null 2>&1)"; then
        :
    else
        output="<command failed>"
    fi

    output="$(printf "%s" "${output}" | tr -d '\r' | sed -n '/./{s/|/\\|/g;p;q;}')"
    if [ -z "${output}" ]; then
        output="<no version output>"
    fi
    printf '| %s | `%s` |\n' "${name}" "${output}"
}

report_python_pkg() {
    local name="$1"
    local dist="$2"
    local output

    if ! command -v python3 >/dev/null 2>&1; then
        output="<not installed>"
    elif output="$(DIST_NAME="${dist}" python3 - <<'PY' 2>&1
import importlib.metadata
import os

print(importlib.metadata.version(os.environ["DIST_NAME"]))
PY
)"; then
        :
    else
        output="<package probe failed>"
    fi

    output="$(printf "%s" "${output}" | sed -n '1p' | tr -d '\r' | sed 's/|/\\|/g')"
    if [ -z "${output}" ]; then
        output="<no version output>"
    fi
    printf '| %s | `%s` |\n' "${name}" "${output}"
}

report_once() {
    local name="$1"
    shift

    if [ -n "${reported_tools[${name}]:-}" ]; then
        return 0
    fi
    reported_tools["${name}"]=1
    report "${name}" "$@"
}

report_python_pkg_once() {
    local name="$1"
    shift

    if [ -n "${reported_tools[${name}]:-}" ]; then
        return 0
    fi
    reported_tools["${name}"]=1
    report_python_pkg "${name}" "$@"
}

report_compile_option() {
    local language="$1"
    local options="$2"
    local escaped_language
    local escaped_options

    if ! has_language "${language}"; then
        return 0
    fi

    escaped_language="$(printf "%s" "${language}" | sed 's/`/\\`/g;s/|/\\|/g')"
    escaped_options="$(printf "%s" "${options}" | sed 's/`/\\`/g;s/|/\\|/g')"
    printf '| `%s` | `%s` |\n' "${escaped_language}" "${escaped_options}"
}

echo "| Tool | Version |"
echo "| --- | --- |"
if has_language "aheui"; then
    report_once "Python" python3 --version
    report_python_pkg_once "Aheui" "aheui"
fi

if has_language "python"; then
    report_once "Python" python3 --version
    report_python_pkg_once "NumPy" "numpy"
    report_python_pkg_once "Pandas" "pandas"
    report_python_pkg_once "Seaborn" "seaborn"
    report_python_pkg_once "Matplotlib" "matplotlib"
    report_python_pkg_once "Pillow" "Pillow"
    report_python_pkg_once "Six" "six"
    report_python_pkg_once "Qiskit" "qiskit"
    report_python_pkg_once "PyParsing" "pyparsing"
    report_python_pkg_once "PyLaTeXEnc" "pylatexenc"
    report_python_pkg_once "Torch" "torch"
    report_python_pkg_once "TorchVision" "torchvision"
    report_python_pkg_once "JAX" "jax"
    report_python_pkg_once "JAXLIB" "jaxlib"
fi

if has_language "apecode"; then
    report_python_pkg_once "APECode" "apecode"
fi

if has_language "whitespace"; then
    report_once "Whitespace interpreter" sh -c 'test -f /usr/local/lib/aonohako/whitespace.py && printf bundled'
fi

if has_language "bf"; then
    report_once "Brainfuck interpreter" sh -c 'test -f /usr/local/lib/aonohako/brainfuck.py && printf bundled'
fi

if has_language "befunge"; then
    report_once "Befunge interpreter" sh -c 'test -f /usr/local/lib/aonohako/befunge.py && printf bundled'
fi

if has_language "malbolge"; then
    report_once "Malbolge interpreter" sh -c 'test -f /usr/local/lib/aonohako/malbolge.py && printf "bundled (1998 reference)"'
fi

if has_language "pypy"; then
    report_once "PyPy" pypy3 --version
fi

if has_language "picolisp"; then
    report_once "PicoLisp" pil -version -bye
fi

if has_language "assemblyscript" || has_language "javascript" || has_language "typescript" || has_language "coffeescript" || has_language "elm" || has_language "rescript" || has_language "purescript"; then
    report_once "Node.js" node --version
    report_once "npm" npm --version
fi

if has_language "assemblyscript"; then
    report_once "AssemblyScript" asc --version
fi

if has_language "factor"; then
    report_once "Factor" /opt/factor/factor -version
fi

if has_language "typescript"; then
    report_once "TypeScript" tsc --version
fi

if has_language "deno"; then
    report_once "Deno" deno --version
fi

if has_language "elm"; then
    report_once "Elm" elm --version
fi

if has_language "coffeescript"; then
    report_once "CoffeeScript" coffee --version
fi

if has_language "rescript"; then
    report_once "ReScript" rescript --version
fi

if has_language "purescript"; then
    report_once "PureScript" purs --version
    report_once "Spago" spago --version
fi

if has_language "haxe"; then
    report_once "Haxe" haxe --version
    report_once "Neko" neko -version
fi

if has_language "graphql"; then
    report_once "Python" python3 --version
    report_python_pkg_once "GraphQL Core" "graphql-core"
fi

if has_language "java" || has_language "groovy" || has_language "scala" || has_language "clojure" || has_language "kotlin-jvm"; then
    report_once "Java compiler" javac -version
fi

if has_language "java" || has_language "groovy" || has_language "scala" || has_language "clojure" || has_language "kotlin-jvm" || has_language "tla"; then
    report_once "Java runtime" java -version
fi

if has_language "groovy"; then
    report_once "Groovy" groovy --version
fi

if has_language "scala"; then
    report_once "Scala" scala -version
fi

if has_language "plain" || has_language "c" || has_language "cpp" || has_language "asm" || has_language "nasm" || has_language "objective-c" || has_language "objective-cpp"; then
    report_once "GCC" gcc -dumpfullversion -dumpversion
    report_once "G++" g++ -dumpfullversion -dumpversion
fi

if has_language "objective-c"; then
    report_once "Clang" clang --version
fi

if has_language "objective-cpp"; then
    report_once "Clang++" clang++ --version
fi

if has_language "asm"; then
    report_once "GNU as" as --version
fi

if has_language "nasm"; then
    report_once "NASM" nasm -v
fi

if has_language "go"; then
    report_once "Go" go version
fi

if has_language "rust"; then
    report_once "Rust" rustc --version
fi

if has_language "zig"; then
    report_once "Zig" zig version
fi

if has_language "d"; then
    report_once "LDC" ldc2 --version
fi

if has_language "fortran"; then
    report_once "GFortran" gfortran --version
fi

if has_language "haskell"; then
    report_once "GHC" ghc --version
fi

if has_language "vlang"; then
    report_once "V" v version
fi

if has_language "odin"; then
    report_once "Odin" odin version
fi

if has_language "c3"; then
    report_once "C3" c3c --version
fi

if has_language "hare"; then
    report_once "Hare" hare version
fi

if has_language "mojo"; then
    report_once "Mojo" mojo --version
fi

if has_language "moonbit"; then
    report_once "MoonBit" env MOON_HOME=/opt/moonbit moon version --all
fi

if has_language "fennel"; then
    report_once "Fennel" fennel --version
fi

if has_language "chapel"; then
    report_once "Chapel" chpl --version
fi

if has_language "algol68"; then
    report_once "Algol 68 Genie" a68g --version
fi

if has_language "koka"; then
    report_once "Koka" koka --version
fi

if has_language "pony"; then
    report_once "Pony" ponyc --version
fi

if has_language "bash"; then
    report_once "Bash package" dpkg-query -W '-f=${Version}' bash
fi

if has_language "posix-sh"; then
    report_once "dash package" dpkg-query -W '-f=${Version}' dash
fi

if has_language "powershell"; then
    report_once "PowerShell" pwsh -NoLogo -NoProfile -NonInteractive -Command '$PSVersionTable.PSVersion.ToString()'
fi

if has_language "zerolang"; then
    report_once "Zerolang" zero --version
fi

if has_language "swift"; then
    report_once "Swift" swift --version
fi

if has_language "kotlin"; then
    report_once "Kotlin/Native" kotlinc-native -version
fi

if has_language "kotlin-jvm"; then
    report_once "Kotlin/JVM" kotlinc -version
fi

if has_language "pascal" || has_language "delphi" || has_language "objectpascal"; then
    report_once "Free Pascal" fpc -iV
fi

if has_language "nim"; then
    report_once "Nim" nim --version
fi

if has_language "clojure"; then
    report_once "Clojure" clojure -e "(println (clojure-version))"
fi

if has_language "racket"; then
    report_once "Racket" racket --version
fi

if has_language "scheme"; then
    report_once "Chibi Scheme" chibi-scheme -V
fi

if has_language "chez-scheme"; then
    report_once "Chez Scheme" /usr/bin/chezscheme --version
fi

if has_language "awk"; then
    report_once "GNU awk" gawk --version
fi

if has_language "tcl"; then
    if [ -z "${reported_tools[Tcl]:-}" ]; then
        reported_tools["Tcl"]=1
        if command -v tclsh >/dev/null 2>&1; then
            output="$(printf 'puts [info patchlevel]\n' | tclsh 2>&1 || printf '<command failed>')"
        else
            output="<not installed>"
        fi
        output="$(printf "%s" "${output}" | tr -d '\r' | sed -n '/./{s/|/\\|/g;p;q;}')"
        if [ -z "${output}" ]; then
            output="<no version output>"
        fi
        printf '| Tcl | `%s` |\n' "${output}"
    fi
fi

if has_language "gleam"; then
    report_once "Gleam" gleam --version
fi

if has_language "gdl"; then
    report_once "GNU Data Language" gdl --version
fi

if has_language "octave"; then
    report_once "GNU Octave" octave-cli --version
fi

if has_language "duckdb"; then
    report_once "DuckDB" duckdb --version
fi

if has_language "vhdl"; then
    report_once "GHDL" ghdl --version
fi

if has_language "verilog" || has_language "systemverilog"; then
    report_once "Icarus Verilog" iverilog -V
    report_once "VVP" vvp -V
fi

if has_language "crystal"; then
    report_once "Crystal" crystal --version
fi

if has_language "vala"; then
    report_once "Vala" valac --version
fi

if has_language "cuda-ocelot"; then
    report_once "NVIDIA CUDA compiler" nvcc --version
fi

if has_language "carbon"; then
    report_once "Carbon" carbon --version
fi

if has_language "ada"; then
    report_once "Ada" gnatmake -v
fi

if has_language "cobol" || has_language "gnucobol"; then
    report_once "GnuCOBOL" cobc --version
fi

if has_language "cython"; then
    report_once "Python" python3 --version
    report_once "Cython" cython3 --version
fi

if has_language "dart"; then
    report_once "Dart" dart --version
fi

if has_language "julia"; then
    report_once "Julia" julia --version
fi

if has_language "r"; then
    report_once "R" Rscript --version
fi

if has_language "raku"; then
    report_once "Raku" raku --version
fi

if has_language "erlang"; then
    report_once "Erlang" erl -noshell -eval "io:format(\"~s~n\", [erlang:system_info(otp_release)]), halt()."
fi

if has_language "mercury"; then
    report_once "Mercury" mmc --version
fi

if has_language "idris2"; then
    report_once "Idris2" idris2 --version
fi

if has_language "prolog"; then
    report_once "Prolog" swipl --version
fi

if has_language "ocaml"; then
    report_once "OCaml" ocamlopt -version
fi

if has_language "lisp"; then
    report_once "SBCL" sbcl --version
fi

if has_language "sml"; then
    report_once "MLton" mlton
fi

if has_language "elixir"; then
    report_once "Elixir" elixir -e "IO.puts(System.version())"
fi

if has_language "ruby"; then
    report_once "Ruby" ruby -e "print RUBY_VERSION, \"\n\""
fi

if has_language "vb6" || has_language "golfscript"; then
    report_once "Ruby" ruby -e "print RUBY_VERSION, \"\n\""
fi

if has_language "freebasic" || has_language "classic-basic" || has_language "qbasic"; then
    report_once "FreeBASIC" fbc -version
fi

if has_language "smalltalk"; then
    report_once "GNU Smalltalk" gst --version
fi

if has_language "php"; then
    report_once "PHP" php --version
fi

if has_language "lua"; then
    report_once "Lua" lua5.4 -v
fi

if has_language "perl"; then
    report_once "Perl" perl -e "printf \"v%vd\\n\", \$^V"
fi

if has_language "sqlite"; then
    report_once "SQLite" sqlite3 --version
fi

if has_language "sed"; then
    report_once "GNU sed" sed --version
fi

if has_language "bc"; then
    report_once "bc" bc --version
fi

if has_language "j"; then
    report_once "J" bash -lc "printf '%s\n' \"echo 9!:14 ''\" 'exit 0' >/tmp/aonohako-j-version.ijs && aonohako-j /tmp/aonohako-j-version.ijs"
fi

if has_language "lolcode"; then
    report_once "lci" lci -v
fi

if has_language "forth" || has_language "gforth"; then
    report_once "Gforth" gforth --version
fi

if has_language "csharp" || has_language "fsharp" || has_language "vbnet"; then
    report_once ".NET" dotnet --version
fi

if has_language "coq" || has_language "rocq"; then
    if command -v rocq >/dev/null 2>&1; then
        report_once "Rocq" rocq --version
    fi
    report_once "Coq" coqc --version
fi

if has_language "lean4"; then
    report_once "Lean" lean --version
fi

if has_language "agda"; then
    report_once "Agda" agda --version
fi

if has_language "dafny"; then
    report_once "Dafny" env DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1 DOTNET_PROCESSOR_COUNT=1 COMPlus_ThreadPool_ForceMinWorkerThreads=1 dafny --version
fi

if has_language "tla"; then
    report_once "TLA+ TLC" bash -c 'java -cp /usr/local/lib/aonohako/tla2tools.jar tlc2.TLC -version 2>&1 | grep -m1 "^TLC2 Version "'
fi

if has_language "why3"; then
    report_once "Why3" why3 --version
    report_once "Z3" z3 --version
fi

if has_language "isabelle"; then
    report_once "Isabelle" isabelle version
fi

if has_language "fstar"; then
    report_once "F*" fstar.exe --version
fi

if has_language "alloy"; then
    report_once "Alloy" java -jar /usr/local/lib/aonohako/alloy.jar version
fi

if has_language "acl2"; then
    report_once "ACL2" sh -c 'printf "%s\n" "(good-bye)" | ACL2_CUSTOMIZATION=NONE acl2 | sed -n "1,12p"'
fi

if has_language "kframework"; then
    report_once "K Framework" kompile --version
fi

if has_language "bqn"; then
    report_once "CBQN" sh -c 'command -v bqn >/dev/null && printf installed'
fi

if has_language "apl"; then
    report_once "APL" apl --version
fi

if has_language "uiua"; then
    report_once "Uiua" uiua --version
fi

if has_language "janet"; then
    report_once "Janet" janet -v
fi

if has_language "assemblyscript" || has_language "wasm"; then
    report_once "Wasmtime" wasmtime --version
fi

if has_language "uhmlang"; then
    report_once "Umjunsik Lang" sh -c 'command -v umjunsik-lang-go >/dev/null && printf installed'
fi

echo
echo "## Runtime Compile Options"
echo
echo "| Language | Compile options |"
echo "| --- | --- |"
report_compile_option "aheui" "pass-through .aheui artifacts"
report_compile_option "apecode" "apecc -o <target>"
report_compile_option "plain" "C: gcc -O2 -Wall -lm --static -DONLINE_JUDGE=1 -std=c11; C++: g++ -O2 -Wall -lm --static -pipe -DONLINE_JUDGE=1 -std=c++17; text: pass-through"
report_compile_option "c" "gcc -O2 -Wall -lm --static -DONLINE_JUDGE=1 -std=<selected>"
report_compile_option "cpp" "g++ -O2 -Wall -lm --static -pipe -DONLINE_JUDGE=1 -std=<selected>"
report_compile_option "python" "python3 -I -S -m compileall -b ."
report_compile_option "pypy" "pypy3 -I -S -m compileall -b ."
report_compile_option "java" "javac --release 11 -encoding UTF-8"
report_compile_option "groovy" "groovyc -d <workdir>"
report_compile_option "scala" "scalac -d <workdir>"
report_compile_option "clojure" "clojure reader parse check"
report_compile_option "chez-scheme" "/usr/bin/chezscheme --quiet --script /usr/local/lib/aonohako/chez_scheme_check.scm <source.scm>; run with /usr/bin/chezscheme --quiet --script <source.scm>"
report_compile_option "javascript" "node --check"
report_compile_option "assemblyscript" "aonohako-assemblyscript-compile <source.ts> <target.wasm>; wasm-validate <target.wasm>; run with bounded Wasmtime and no filesystem preopens"
report_compile_option "factor" "/opt/factor/factor -no-user-init -no-signals -q /usr/local/lib/aonohako/factor_check.factor <source.factor>; run with bounded stacks and immutable vocabulary roots"
report_compile_option "typescript" "tsc --module commonjs --target es2019 --sourceMap --outDir dist"
report_compile_option "coffeescript" "coffee --compile --bare --output <workdir>"
report_compile_option "rescript" "rescript build"
report_compile_option "purescript" "spago build"
report_compile_option "deno" "deno check --v8-flags=--max-old-space-size=<compile cap>"
report_compile_option "elm" "elm make <source> --output <target>"
report_compile_option "idris2" "idris2 --cg chez -o <target>"
report_compile_option "haxe" "haxe -D ONLINE_JUDGE -main Main -neko <target>.n"
report_compile_option "graphql" "pass-through .graphql artifacts"
report_compile_option "haskell" "ghc -O2 -o <target>"
report_compile_option "asm" "gcc -nostdlib -static -no-pie"
report_compile_option "nasm" "nasm -felf64 -dONLINE_JUDGE=1 plus gcc -nostdlib -static -no-pie"
report_compile_option "objective-c" "clang -x objective-c -O2 -pipe -DONLINE_JUDGE=1 -lobjc"
report_compile_option "objective-cpp" "clang++ -x objective-c++ -O2 -pipe -DONLINE_JUDGE=1 -lobjc"
report_compile_option "go" "go build -tags=online_judge,ONLINE_JUDGE -o <target>"
report_compile_option "rust" "rustc --edition 2018 -O --cfg ONLINE_JUDGE -o <target>"
report_compile_option "zig" "zig build-exe -O ReleaseSafe -femit-bin=<target>"
report_compile_option "pascal" "fpc -O2 -Xs -dONLINE_JUDGE"
report_compile_option "delphi" "fpc -Mdelphi -O2 -Xs -dONLINE_JUDGE"
report_compile_option "objectpascal" "fpc -Mobjfpc -O2 -Xs -dONLINE_JUDGE"
report_compile_option "nim" "nim c -d:release -d:ONLINE_JUDGE --opt:speed"
report_compile_option "ada" "gnatmake -O2"
report_compile_option "cobol" "cobc -x -free -O2"
report_compile_option "gnucobol" "cobc -x -free -O2"
report_compile_option "cython" "cython3 --embed -3 plus gcc -O2 -pipe -DONLINE_JUDGE=1"
report_compile_option "d" "ldc2 -O3 -release --d-version=ONLINE_JUDGE"
report_compile_option "fortran" "gfortran -O2 -pipe"
report_compile_option "crystal" "crystal build --release --no-debug --define ONLINE_JUDGE"
report_compile_option "vala" "valac --define=ONLINE_JUDGE -o <target>"
report_compile_option "vlang" "v -d ONLINE_JUDGE -o <target>"
report_compile_option "odin" "odin build . -define:ONLINE_JUDGE=true -out:<target>"
report_compile_option "c3" "c3c compile -D ONLINE_JUDGE -O2"
report_compile_option "hare" "hare build -o <target>"
report_compile_option "mojo" "mojo build -o <target>"
report_compile_option "moonbit" "moon build --target native --release --strip --frozen --jobs 1"
report_compile_option "fennel" "aonohako-fennel-compile <source> <target>.lua (embedded compile-string API, requireAsInclude, luac5.4 validation, atomic publish)"
report_compile_option "chapel" "CHPL_COMM=none CHPL_TASKS=qthreads CHPL_TARGET_CPU=none chpl --local --fast -o <target> <source>; run with CHPL_RT_NUM_THREADS_PER_LOCALE=1 <target> -nl 1"
report_compile_option "algol68" "a68g --quiet --no-compile -O0 --check --file <source> --no-pragmats; run with --run instead of --check"
report_compile_option "koka" 'koka --compile -O2 --no-debug -j1 -v0 --console=raw --no-autoinstall --cc=/usr/bin/gcc-16 "--ccopts=-march=x86-64 -mtune=generic" "--cclinkopts=-march=x86-64 -mtune=generic" --builddir=.aonohako-koka-build --output=Main main.kk (after normalizing the source filename to main.kk)'
report_compile_option "pony" "ponyc --cpu=generic --output=. --bin-name=Main .; run with ./Main --ponymaxthreads=1"
report_compile_option "bash" "bash --noprofile --norc -n Main.sh; run with /bin/bash --noprofile --norc Main.sh"
report_compile_option "posix-sh" "/bin/dash -n Main.sh; run with /bin/dash Main.sh"
report_compile_option "powershell" "pwsh -NoLogo -NoProfile -NonInteractive -Command '& { param(\$path) ... [System.Management.Automation.Language.Parser]::ParseFile(\$path,...) ... }' Main.ps1; run with pwsh -NoLogo -NoProfile -NonInteractive -File Main.ps1"
report_compile_option "zerolang" "zero import --out <graph> <source.0>; zero build --release release-fast --out <target> <graph>"
report_compile_option "kotlin" "kotlinc-native -J-Xms64m -J-Xmx<compiler cap> -J-Xss1m -J-XX:+UseSerialGC -J-XX:ReservedCodeCacheSize=32m -J-XX:MaxMetaspaceSize=192m -J-XX:CompressedClassSpaceSize=64m -opt -o <target>"
report_compile_option "kotlin-jvm" "kotlinc -J-Xms64m -J-Xmx<compiler cap> -J-Xss1m -J-XX:+UseSerialGC -jvm-target 1.8 -include-runtime -d <target>.jar; optional javac --release 8 plus jar uf"
report_compile_option "swift" "swiftc -O -D ONLINE_JUDGE -module-cache-path <workdir>/.cache/swift-module-cache"
report_compile_option "cuda-ocelot" "aonohako-cuda-ocelot-build"
report_compile_option "carbon" "carbon compile --optimize=speed --no-debug-info --output-last-input-only --output=<target>.o <source>; carbon --prebuilt-runtimes=/opt/carbon/lib/carbon/aonohako-runtimes link --output=<target> <target>.o <prebuilt Core objects>"
report_compile_option "gleam" "gleam build"
report_compile_option "vbnet" "dotnet publish --configuration Release -p:UseAppHost=false -p:DefineConstants=ONLINE_JUDGE"
report_compile_option "csharp" "dotnet publish --configuration Release -p:UseAppHost=false -p:DefineConstants=ONLINE_JUDGE"
report_compile_option "fsharp" "dotnet publish --configuration Release -p:UseAppHost=false -p:DefineConstants=ONLINE_JUDGE"
report_compile_option "vb6" "pass-through VB6 source artifacts"
report_compile_option "freebasic" "fbc -d ONLINE_JUDGE -x <target>"
report_compile_option "classic-basic" "fbc -lang qb -d ONLINE_JUDGE -x <target>"
report_compile_option "qbasic" "fbc -lang qb -d ONLINE_JUDGE -x <target>"
report_compile_option "smalltalk" "pass-through .st artifacts"
report_compile_option "lisp" "sbcl --noinform --non-interactive --eval compile-file"
report_compile_option "picolisp" "pass-through .l artifacts"
report_compile_option "golfscript" "pass-through .gs artifacts"
report_compile_option "duckdb" "pass-through .sql artifacts"
report_compile_option "bqn" "pass-through .bqn artifacts"
report_compile_option "apl" "pass-through .apl artifacts"
report_compile_option "j" "pass-through .ijs artifacts"
report_compile_option "uiua" "pass-through .ua artifacts"
report_compile_option "janet" "pass-through .janet artifacts"
report_compile_option "sed" "pass-through .sed artifacts"
report_compile_option "bc" "pass-through .bc artifacts"
report_compile_option "befunge" "pass-through .bef/.bf93 artifacts"
report_compile_option "malbolge" "validate reference opcodes, then pass-through .mal/.mb artifacts"
report_compile_option "lolcode" "pass-through .lol artifacts"
report_compile_option "forth" "pass-through .fs artifacts"
report_compile_option "gforth" "pass-through .fs/.fth/.4th artifacts"
report_compile_option "whitespace" "pass-through .ws artifacts"
report_compile_option "bf" "pass-through .bf artifacts"
report_compile_option "wasm" "wat2wasm or pass-through .wasm artifacts"
report_compile_option "uhmlang" "pass-through .uhm artifacts"
report_compile_option "racket" "raco make"
report_compile_option "scheme" "pass-through .scm artifacts"
report_compile_option "awk" "gawk --sandbox --lint -f"
report_compile_option "tcl" "pass-through .tcl artifacts"
report_compile_option "gdl" "pass-through .pro artifacts"
report_compile_option "octave" "pass-through .m artifacts"
report_compile_option "vhdl" "ghdl -a --std=08 plus ghdl -e --std=08"
report_compile_option "dart" "dart compile exe -D ONLINE_JUDGE=true -o <target>"
report_compile_option "verilog" "iverilog -g2012 -DONLINE_JUDGE=1 -o <target>.vvp"
report_compile_option "systemverilog" "iverilog -g2012 -DONLINE_JUDGE=1 -o <target>.vvp"
report_compile_option "r" "/usr/lib/R/bin/exec/R --vanilla --slave -e parse(file=commandArgs(TRUE)[1]) --args <file>"
report_compile_option "raku" "raku -c"
report_compile_option "erlang" "erlc"
report_compile_option "mercury" "mmc --make --grade hlc.gc <module>"
report_compile_option "prolog" "swipl syntax check"
report_compile_option "ocaml" "ocamlopt"
report_compile_option "sml" "mlton -output <target>"
report_compile_option "elixir" "elixirc"
report_compile_option "ruby" "ruby -c"
report_compile_option "php" "php -l"
report_compile_option "lua" "luac5.4 -p"
report_compile_option "perl" "perl -c"
report_compile_option "sqlite" "pass-through .sql artifacts"
report_compile_option "julia" "pass-through .jl artifacts"
report_compile_option "coq" "rocq c or coqc -q"
report_compile_option "rocq" "rocq c or coqc -q"
report_compile_option "lean4" "lean"
report_compile_option "agda" "agda"
report_compile_option "dafny" "dafny verify --cores 1"
report_compile_option "tla" "pass-through .tla/.cfg artifacts"
report_compile_option "why3" "aonohako-why3-prove"
report_compile_option "isabelle" "isabelle process_theories -o naproche_server=false -D ."
report_compile_option "fstar" "fstar.exe"
report_compile_option "alloy" "aonohako-alloy-check"
report_compile_option "acl2" "aonohako-acl2-check"
report_compile_option "kframework" "aonohako-kframework-check"
EOF
