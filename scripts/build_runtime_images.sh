#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)

cd "${REPO_ROOT}"
if [[ -n "${AONOHAKO_RUNTIME_BUILDER:-}" ]]; then
  exec "${AONOHAKO_RUNTIME_BUILDER}" "$@"
fi
exec go run ./cmd/runtime-builder "$@"
