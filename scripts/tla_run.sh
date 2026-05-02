#!/usr/bin/env bash
set -euo pipefail

src="${1:?usage: aonohako-tla-run <spec.tla>}"
cfg="${src%.tla}.cfg"
args=("-workers" "1" "-deadlock")
if [[ -f "${cfg}" ]]; then
  args+=("-config" "${cfg}")
fi
args+=("${src}")

exec java -cp /usr/local/lib/aonohako/tla2tools.jar tlc2.TLC "${args[@]}" >/dev/null
