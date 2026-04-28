#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)

cd "${REPO_ROOT}"

declare -a patterns=(
  'gcloud'
  'GOOGLE_APPLICATION_CREDENTIALS'
  'BEGIN PRIVATE KEY'
  'private_key_id'
  'client_email'
)

for pattern in "${patterns[@]}"; do
  if rg -F -n --hidden --glob '!.git' --glob '!README.md' --glob '!docs/**' --glob '!scripts/check_repo_policy.sh' "${pattern}" .; then
    echo "repository policy violation: found forbidden pattern '${pattern}'" >&2
    exit 1
  fi
done

require_pinned_arg() {
  local file=$1
  local arg=$2
  if ! rg -q "^ARG ${arg}=[^[:space:]]+@sha256:[0-9a-f]{64}$" "${file}"; then
    echo "repository policy violation: ${file} must define digest-pinned ARG ${arg}" >&2
    exit 1
  fi
}

require_pinned_arg Dockerfile GO_IMAGE
require_pinned_arg Dockerfile RUNTIME_BASE
require_pinned_arg Dockerfile DOTNET_SDK_IMAGE
require_pinned_arg Dockerfile PYTHON_IMAGE
require_pinned_arg docker/runtime.Dockerfile GO_IMAGE
require_pinned_arg docker/runtime.Dockerfile RUNTIME_BASE

if rg -n '^FROM( --platform=\$BUILDPLATFORM)? [^{$][^[:space:]@]*:[^[:space:]@]*( AS|$)' Dockerfile docker/runtime.Dockerfile; then
  echo "repository policy violation: Dockerfile external FROM images must be digest-pinned or routed through a pinned ARG" >&2
  exit 1
fi

echo "repository policy check passed"
