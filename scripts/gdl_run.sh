#!/usr/bin/env bash
set -euo pipefail

src="${1:?usage: aonohako-gdl-run <source.pro> [entrypoint]}"
entry="${2:-main}"

printf '.compile %s\n%s\nexit\n' "${src}" "${entry}" | gdl -quiet
