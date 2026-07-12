#!/usr/bin/env bash
set -euo pipefail

src="${1:?usage: aonohako-gdl-run <source.pro> [entrypoint]}"
entry="${2:-main}"

if [[ "${src}" == "${PWD}/"* ]]; then
  src="${src#"${PWD}/"}"
elif [[ "${src}" == /* ]]; then
  echo "invalid GDL source path" >&2
  exit 2
fi

if [[ ! "${src}" =~ ^[A-Za-z0-9_./-]+$ ]]; then
  echo "invalid GDL source path" >&2
  exit 2
fi

if [[ ! "${entry}" =~ ^[A-Za-z_][A-Za-z0-9_\$]*$ ]]; then
  echo "invalid GDL entrypoint: ${entry}" >&2
  exit 2
fi

printf '.compile %s\n%s\nexit\n' "${src}" "${entry}" | gdl -quiet
