#!/usr/bin/env bash
set -euo pipefail

src="${1:?usage: aonohako-duckdb-run <query.sql>}"

if grep -Eiq '(^|[^[:alpha:]_])(install|load|copy)[[:space:]]' "${src}"; then
  echo "duckdb restricted statement rejected" >&2
  exit 1
fi
if grep -Eiq 'read_(csv|json|parquet)|pragma[[:space:]]+enable_|pragma[[:space:]]+disable_' "${src}"; then
  echo "duckdb restricted function rejected" >&2
  exit 1
fi

{
  printf '%s\n' '.mode list'
  printf '%s\n' '.headers off'
  cat "${src}"
} | duckdb -batch ':memory:'
