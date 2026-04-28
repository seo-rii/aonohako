#!/usr/bin/env bash
set -euo pipefail

tool="${1:-}"
version="${2:-}"
dest_dir="${3:-/usr/local/bin}"

if [[ "${tool}" != "syft" && "${tool}" != "grype" ]]; then
  echo "usage: install_anchore_tool.sh syft|grype vX.Y.Z [dest-dir]" >&2
  exit 2
fi
if [[ -z "${version}" ]]; then
  echo "usage: install_anchore_tool.sh syft|grype vX.Y.Z [dest-dir]" >&2
  exit 2
fi

version="${version#v}"
archive="${tool}_${version}_linux_amd64.tar.gz"
checksums="${tool}_${version}_checksums.txt"
base_url="https://github.com/anchore/${tool}/releases/download/v${version}"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

for attempt in 1 2 3 4 5; do
  rm -f "${tmp_dir}/${archive}" "${tmp_dir}/${checksums}" "${tmp_dir}/${archive}.sha256" "${tmp_dir}/${tool}"
  if curl -fsSL --retry 3 --retry-delay 2 --retry-connrefused --retry-all-errors "${base_url}/${archive}" -o "${tmp_dir}/${archive}" &&
    curl -fsSL --retry 3 --retry-delay 2 --retry-connrefused --retry-all-errors "${base_url}/${checksums}" -o "${tmp_dir}/${checksums}" &&
    grep -E "[ *]${archive}$" "${tmp_dir}/${checksums}" > "${tmp_dir}/${archive}.sha256" &&
    (cd "${tmp_dir}" && sha256sum --check --status "${archive}.sha256") &&
    tar -xzf "${tmp_dir}/${archive}" -C "${tmp_dir}" "${tool}"; then
    install -d -m 0755 "${dest_dir}"
    install -m 0755 "${tmp_dir}/${tool}" "${dest_dir}/${tool}"
    exit 0
  fi
  echo "install ${tool} v${version} failed on attempt ${attempt}/5" >&2
  sleep $((attempt * 3))
done

echo "failed to install ${tool} v${version}" >&2
exit 1
