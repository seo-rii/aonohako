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

echo "repository policy check passed"
