#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: aonohako-why3-prove <Main.mlw>" >&2
  exit 2
fi

src=$1
work_dir=${TMPDIR:-/tmp}
vc_dir=$(mktemp -d "${work_dir%/}/aonohako-why3-vc.XXXXXX")
trap 'rm -rf "$vc_dir"' EXIT

export HOME="${WHY3_HOME:-/usr/local/lib/aonohako/why3-home}"

why3 prove -P z3 -o "$vc_dir" "$src" >/dev/null

found=0
while IFS= read -r -d '' vc; do
  found=1
  if ! output=$(z3 "$vc" 2>&1); then
    printf '%s\n' "$output" >&2
    exit 1
  fi
  if ! printf '%s\n' "$output" | grep -qx 'unsat'; then
    echo "why3 proof failed for ${src}: ${vc}" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done < <(find "$vc_dir" -type f -name '*.smt2' -print0)

if [[ $found -eq 0 ]]; then
  echo "why3 generated no z3 verification conditions for ${src}" >&2
  exit 1
fi
