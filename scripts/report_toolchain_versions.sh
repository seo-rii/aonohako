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
if [ -n "${AONOHAKO_LANGUAGES:-}" ]; then
    echo "- Languages: \`${AONOHAKO_LANGUAGES}\`"
fi
if [ -n "${REPO_DIGEST}" ]; then
    echo "- Repo digest: \`${REPO_DIGEST}\`"
fi
echo

docker run --rm -i --entrypoint bash "${IMAGE_REF}" <<'EOF'
set -euo pipefail

declare -A enabled_languages=()
declare -A reported_tools=()

if [ -n "${AONOHAKO_LANGUAGES:-}" ]; then
    while IFS= read -r raw_language; do
        language="$(printf "%s" "${raw_language}" | tr -d '[:space:]')"
        if [ -n "${language}" ]; then
            enabled_languages["${language}"]=1
        fi
    done < <(printf "%s" "${AONOHAKO_LANGUAGES}" | tr ',' '\n')
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
        return 0
    fi

    if output="$("$@" 2>&1)"; then
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
        return 0
    elif output="$(DIST_NAME="${dist}" python3 - <<'PY' 2>&1
import importlib.metadata
import os

print(importlib.metadata.version(os.environ["DIST_NAME"]))
PY
)"; then
        :
    else
        return 0
    fi

    output="$(printf "%s" "${output}" | sed -n '1p' | tr -d '\r' | sed 's/|/\\|/g')"
    printf '| %s | `%s` |\n' "${name}" "${output}"
}

report_python_module() {
    local name="$1"
    local module="$2"
    local output

    if ! command -v python3 >/dev/null 2>&1; then
        return 0
    elif output="$(MODULE_NAME="${module}" python3 - <<'PY' 2>&1
import importlib.util
import os
import sys

spec = importlib.util.find_spec(os.environ["MODULE_NAME"])
if spec is None:
    sys.exit(1)
print("vendored")
PY
)"; then
        :
    else
        return 0
    fi

    output="$(printf "%s" "${output}" | sed -n '1p' | tr -d '\r' | sed 's/|/\\|/g')"
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

report_python_module_once() {
    local name="$1"
    shift

    if [ -n "${reported_tools[${name}]:-}" ]; then
        return 0
    fi
    reported_tools["${name}"]=1
    report_python_module "${name}" "$@"
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
    report_python_module_once "JungolRobot" "jungol_robot"
    report_python_module_once "robot_judge" "robot_judge"
fi

if has_language "pypy"; then
    report_once "PyPy" pypy3 --version
fi

if has_language "javascript" || has_language "typescript"; then
    report_once "Node.js" node --version
    report_once "npm" npm --version
fi

if has_language "typescript"; then
    report_once "TypeScript" tsc --version
fi

if has_language "java" || has_language "groovy" || has_language "scala" || has_language "clojure"; then
    report_once "Java compiler" javac -version
    report_once "Java runtime" java -version
fi

if has_language "groovy"; then
    report_once "Groovy" groovy --version
fi

if has_language "scala"; then
    report_once "Scala" scala -version
fi

if has_language "plain" || has_language "asm" || has_language "nasm" || has_language "cuda-lite"; then
    report_once "GCC" gcc -dumpfullversion -dumpversion
    report_once "G++" g++ -dumpfullversion -dumpversion
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

if has_language "swift"; then
    report_once "Swift" swift --version
fi

if has_language "kotlin"; then
    report_once "Kotlin/Native" kotlinc-native -version
fi

if has_language "pascal"; then
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

if has_language "awk"; then
    report_once "GNU awk" gawk --version
fi

if has_language "gdl"; then
    report_once "GNU Data Language" gdl --version
fi

if has_language "octave"; then
    report_once "GNU Octave" octave-cli --version
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

if has_language "ada"; then
    report_once "Ada" gnatmake -v
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

if has_language "erlang"; then
    report_once "Erlang" erl -noshell -eval "io:format(\"~s~n\", [erlang:system_info(otp_release)]), halt()."
fi

if has_language "prolog"; then
    report_once "Prolog" swipl --version
fi

if has_language "ocaml"; then
    report_once "OCaml" ocamlopt -version
fi

if has_language "elixir"; then
    report_once "Elixir" elixir -e "IO.puts(System.version())"
fi

if has_language "ruby"; then
    report_once "Ruby" ruby -e "print RUBY_VERSION, \"\n\""
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

if has_language "csharp" || has_language "fsharp" || has_language "vbnet"; then
    report_once ".NET" dotnet --version
fi

if has_language "coq" || has_language "rocq"; then
    if command -v rocq >/dev/null 2>&1; then
        report_once "Rocq" rocq --version
    fi
    report_once "Coq" coqc --version
fi

if has_language "wasm"; then
    report_once "Wasmtime" wasmtime --version
fi
EOF
