#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${AONOHAKO_SMOKE_COMMAND:-}" ]]; then
  echo "AONOHAKO_SMOKE_COMMAND is empty" >&2
  exit 1
fi

suite=image-permissions
case ",${AONOHAKO_LANGUAGES:-}," in
  *,python,*)
    suite=permissions
    ;;
esac

for _ in 1 2 3; do
  aonohako-selftest "${suite}"
done

smoke_parts=()
rest=${AONOHAKO_SMOKE_COMMAND}
while [[ "${rest}" == *$'\t'* ]]; do
  smoke_parts+=("${rest%%$'\t'*}")
  rest=${rest#*$'\t'}
done
smoke_parts+=("${rest}")
exec "${smoke_parts[@]}"
