#!/usr/bin/env bash
set -euo pipefail

project_dir="${1:?usage: aonohako-vhdl-run <project-dir> [top]}"
top="${2:-main_tb}"

cd "$project_dir"

mapfile -t sources < <(find . -type f \( -name '*.vhd' -o -name '*.vhdl' \) -print | sort)
if [[ "${#sources[@]}" -eq 0 ]]; then
  echo "no vhdl sources" >&2
  exit 1
fi

ghdl -a --std=08 "${sources[@]}"
ghdl -e --std=08 "$top"
exec ghdl -r --std=08 "$top" --assert-level=error --stop-time=1ms
