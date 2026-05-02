#!/usr/bin/env bash
set -euo pipefail

src="${1:?usage: aonohako-tla-run <spec.tla>}"
cfg="${src%.tla}.cfg"
args=("-workers" "1" "-deadlock")
if [[ -f "${cfg}" ]]; then
  args+=("-config" "${cfg}")
fi
args+=("${src}")

out="$(mktemp "${TMPDIR:-/tmp}/aonohako-tla.XXXXXX")"
cleanup() {
  rm -f "$out"
}
trap cleanup EXIT

if java -Xms32m -Xmx256m -Xss1m -XX:+UseSerialGC -XX:CompressedClassSpaceSize=64m -XX:ReservedCodeCacheSize=32m -cp /usr/local/lib/aonohako/tla2tools.jar tlc2.TLC "${args[@]}" >"$out"; then
  exit 0
fi
status=$?
head -c 65536 "$out" >&2 || true
exit "$status"
