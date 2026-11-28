#!/usr/bin/env bash
set -euo pipefail
shopt -s nullglob

phase=$1
gid=$2
python_isolation=$3
pypy_isolation=$4

restrict_trees() {
  local path
  for path in "$@"; do
    [[ -e "${path}" ]] || continue
    if [[ "${phase}" == "initial" ]]; then
      chown -R "0:${gid}" "${path}"
    else
      # Late tool permission changes still need to be reconciled. Avoid copying
      # already-correct files from the toolchain layer just to rewrite metadata.
      find "${path}" \( ! -uid 0 -o ! -gid "${gid}" \) -exec chown -h "0:${gid}" {} +
    fi
    find "${path}" -type d ! -perm 0750 -exec chmod 0750 {} +
    find "${path}" -type f -perm /0111 ! -perm 0750 -exec chmod 0750 {} +
    find "${path}" -type f ! -perm /0111 ! -perm 0640 -exec chmod 0640 {} +
  done
}

if [[ "${python_isolation}" == "true" ]]; then
  restrict_trees /usr/lib/python*/dist-packages /usr/local/lib/python*/dist-packages \
    /usr/lib/python*/site-packages /usr/local/lib/python*/site-packages /usr/share/python-wheels
fi
if [[ "${pypy_isolation}" == "true" ]]; then
  restrict_trees /usr/lib/pypy*/dist-packages /usr/local/lib/pypy*/dist-packages \
    /usr/lib/pypy*/site-packages /usr/local/lib/pypy*/site-packages
fi
