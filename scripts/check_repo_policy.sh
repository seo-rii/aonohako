#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "${SCRIPT_DIR}/.." && pwd)

cd "${REPO_ROOT}"

have_rg=false
if command -v rg >/dev/null 2>&1; then
  have_rg=true
fi

search_fixed() {
  local pattern=$1
  if "${have_rg}"; then
    rg -F -n --hidden --glob '!.git' --glob '!README.md' --glob '!docs/**' --glob '!scripts/check_repo_policy.sh' "${pattern}" .
  else
    grep -R -F -n \
      --exclude-dir=.git \
      --exclude-dir=docs \
      --exclude=README.md \
      --exclude=check_repo_policy.sh \
      -- "${pattern}" .
  fi
}

declare -a patterns=(
  'gcloud'
  'GOOGLE_APPLICATION_CREDENTIALS'
  'BEGIN PRIVATE KEY'
  'private_key_id'
  'client_email'
)

for pattern in "${patterns[@]}"; do
  if search_fixed "${pattern}"; then
    echo "repository policy violation: found forbidden pattern '${pattern}'" >&2
    exit 1
  fi
done

"${BASH}" scripts/check_dockerfile_bases.sh \
  --allow-context aonohako-python-packages \
  Dockerfile docker/runtime.Dockerfile

echo "repository policy check passed"
