#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${AONOHAKO_SMOKE_COMMAND:-}" ]]; then
  echo "AONOHAKO_SMOKE_COMMAND is empty" >&2
  exit 1
fi

aonohako-selftest permissions

IFS=$'\t' read -r -a smoke_parts <<< "${AONOHAKO_SMOKE_COMMAND}"
exec "${smoke_parts[@]}"
