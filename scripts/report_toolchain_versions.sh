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
if [ -n "${REPO_DIGEST}" ]; then
    echo "- Repo digest: \`${REPO_DIGEST}\`"
fi
echo

docker run --rm -i --entrypoint bash "${IMAGE_REF}" <<'EOF'
set -euo pipefail

report() {
    local name="$1"
    shift
    local output

    if output="$("$@" 2>&1)"; then
        :
    else
        output="<command failed>"
    fi

    output="$(printf "%s" "${output}" | sed -n '1p' | tr -d '\r' | sed 's/|/\\|/g')"
    printf '| %s | `%s` |\n' "${name}" "${output}"
}

report_python_pkg() {
    local name="$1"
    local dist="$2"
    local output

    if ! command -v python3 >/dev/null 2>&1; then
        output="<command failed>"
    elif output="$(DIST_NAME="${dist}" python3 - <<'PY' 2>&1
import importlib.metadata
import os

print(importlib.metadata.version(os.environ["DIST_NAME"]))
PY
)"; then
        :
    else
        output="<command failed>"
    fi

    output="$(printf "%s" "${output}" | sed -n '1p' | tr -d '\r' | sed 's/|/\\|/g')"
    printf '| %s | `%s` |\n' "${name}" "${output}"
}

echo "| Tool | Version |"
echo "| --- | --- |"
report "Python" python3 --version
report "PyPy" pypy3 --version
report "Node.js" node --version
report "npm" npm --version
report "TypeScript" tsc --version
report "Java compiler" javac -version
report "Java runtime" java -version
report "Groovy" groovy --version
report "Scala" scala -version
report "GCC" gcc --version
report "G++" g++ --version
report "Go" go version
report "Rust" rustc --version
report "Swift" swift --version
report "Kotlin/Native" kotlinc-native -version
report "Free Pascal" fpc -iV
report "Nim" nim --version
report "Clojure" clojure -e "(println (clojure-version))"
report "Racket" racket --version
report "Ada" gnatmake -v
report "Dart" dart --version
report "Julia" julia --version
report "R" Rscript --version
report "Erlang" erl -noshell -eval "io:format(\"~s~n\", [erlang:system_info(otp_release)]), halt()."
report "Prolog" swipl --version
report "OCaml" ocamlopt -version
report "Elixir" elixir -e "IO.puts(System.version())"
report "Ruby" ruby -e "print RUBY_VERSION, \"\n\""
report "PHP" php --version
report "Lua" lua5.4 -v
report "Perl" perl -e "print sprintf(q(v%vd\n), \$^V)"
report "SQLite" sqlite3 --version
report ".NET" dotnet --version
report "Coq" coqc --version
report "Wasmtime" wasmtime --version

report_python_pkg "Aheui" "aheui"
report_python_pkg "NumPy" "numpy"
report_python_pkg "Pandas" "pandas"
report_python_pkg "Seaborn" "seaborn"
report_python_pkg "Matplotlib" "matplotlib"
report_python_pkg "Pillow" "Pillow"
report_python_pkg "Six" "six"
report_python_pkg "Qiskit" "qiskit"
report_python_pkg "PyParsing" "pyparsing"
report_python_pkg "PyLaTeXEnc" "pylatexenc"
report_python_pkg "Torch" "torch"
report_python_pkg "TorchVision" "torchvision"
report_python_pkg "JAX" "jax"
report_python_pkg "JAXLIB" "jaxlib"
EOF
