#!/usr/bin/env bash
set -euo pipefail

for required in unshare mount chroot; do
  if ! command -v "${required}" >/dev/null 2>&1; then
    echo "missing sandbox dependency: ${required}" >&2
    exit 1
  fi
done

if [[ -z "${AONOHAKO_SMOKE_COMMAND:-}" ]]; then
  echo "AONOHAKO_SMOKE_COMMAND is empty" >&2
  exit 1
fi

IFS=$'\t' read -r -a smoke_parts <<< "${AONOHAKO_SMOKE_COMMAND}"
exec "${smoke_parts[@]}"
