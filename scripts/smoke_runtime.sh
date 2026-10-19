#!/usr/bin/env bash
set -euo pipefail

suite=image-permissions
case ",${AONOHAKO_LANGUAGES:-}," in
  *,python,*)
    suite=permissions
    ;;
esac

for _ in 1 2 3; do
  aonohako-selftest "${suite}"
done

work_root="${AONOHAKO_SMOKE_WORK_ROOT:-/work}"
mkdir -p "${work_root}"
# Match the Cloud Run entrypoint: helpers may traverse this root, not list it.
chmod 0711 "${work_root}"
export AONOHAKO_EXECUTION_MODE=local-root
export AONOHAKO_WORK_ROOT="${work_root}"
aonohako-selftest compile-execute
aonohako-selftest two-step
aonohako-selftest language-security
aonohako-selftest runtime-memory

if [[ -z "${AONOHAKO_SMOKE_COMMAND:-}" ]]; then
  exit 0
fi

smoke_parts=()
rest=${AONOHAKO_SMOKE_COMMAND}
while [[ "${rest}" == *$'\t'* ]]; do
  smoke_parts+=("${rest%%$'\t'*}")
  rest=${rest#*$'\t'}
done
smoke_parts+=("${rest}")
exec "${smoke_parts[@]}"
