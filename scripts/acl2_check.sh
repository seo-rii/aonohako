#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: aonohako-acl2-check <Main.lisp>" >&2
  exit 2
fi

src="$1"
driver="$(mktemp)"
stdout_file="$(mktemp)"
stderr_file="$(mktemp)"
cleanup() {
  rm -f "${driver}" "${stdout_file}" "${stderr_file}"
}
trap cleanup EXIT

cat >"${driver}" <<ACL2
(ld "${src}" :ld-error-action :return)
(good-bye)
ACL2

status=0
ACL2_CUSTOMIZATION=NONE acl2 <"${driver}" >"${stdout_file}" 2>"${stderr_file}" || status="$?"
cat "${stdout_file}"
cat "${stderr_file}" >&2
if [[ "${status}" -ne 0 ]]; then
  exit "${status}"
fi

if grep -Eq '(\*\*\*\*\*\*\*\* FAILED \*\*\*\*\*\*\*\*|ACL2 Error|HARD ACL2 ERROR)' "${stdout_file}" "${stderr_file}"; then
  exit 1
fi
