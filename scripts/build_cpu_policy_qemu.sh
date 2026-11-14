#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo 'usage: build_cpu_policy_qemu.sh OUTPUT_DIRECTORY' >&2
  exit 2
fi

# The distribution's static qemu-user build omits the plugin loader. Build the
# small user-mode target explicitly. Capstone is required for x86 instruction
# disassembly in QEMU 10; without it the policy must reject every instruction.
qemu_version=10.0.15
qemu_sha256=b6653d2f71a9e36fd4feb53eb9b782471dc22886b709cc86848b12cdea0a557c
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
patch_path="$script_dir/../runtime/qemu-${qemu_version}-guest-bounds.patch"
output_dir=$(realpath -m -- "$1")
build_jobs=${AONOHAKO_QEMU_BUILD_JOBS:-2}
if [[ ! "$build_jobs" =~ ^[1-9][0-9]*$ ]]; then
  echo 'AONOHAKO_QEMU_BUILD_JOBS must be a positive integer' >&2
  exit 2
fi
build_root=$(mktemp -d "${TMPDIR:-/tmp}/aonohako-qemu.XXXXXXXX")
trap 'rm -rf -- "$build_root"' EXIT
archive="$build_root/qemu.tar.xz"
if [[ -n "${AONOHAKO_QEMU_SOURCE_ARCHIVE:-}" ]]; then
  cp -- "$AONOHAKO_QEMU_SOURCE_ARCHIVE" "$archive"
else
  curl --fail --location --retry 3 --silent --show-error \
    "https://download.qemu.org/qemu-${qemu_version}.tar.xz" -o "$archive"
fi
printf '%s  %s\n' "$qemu_sha256" "$archive" | sha256sum --check --status
tar -xJf "$archive" -C "$build_root"
patch --batch --forward --fuzz=0 -d "$build_root/qemu-${qemu_version}" -p1 < "$patch_path"
mkdir "$build_root/build"
(
  cd "$build_root/build"
  "../qemu-${qemu_version}/configure" \
    --without-default-features \
    --target-list=x86_64-linux-user \
    --enable-linux-user --enable-plugins --enable-capstone \
    --disable-system --disable-tools --disable-docs --disable-guest-agent \
    --disable-debug-info --disable-download
  ninja -j "$build_jobs" qemu-x86_64
)
install -d -m 0755 "$output_dir"
install -m 0755 "$build_root/build/qemu-x86_64" "$output_dir/qemu-x86_64"
strip --strip-unneeded "$output_dir/qemu-x86_64"
install -m 0644 "$build_root/qemu-${qemu_version}/COPYING" "$output_dir/QEMU-COPYING"
install -m 0644 "$patch_path" "$output_dir/qemu-${qemu_version}-guest-bounds.patch"
patch_sha256=$(sha256sum "$patch_path")
patch_sha256=${patch_sha256%% *}
printf 'QEMU %s\nsource_url=https://download.qemu.org/qemu-%s.tar.xz\nsource_sha256=%s\nguest_bounds_patch_sha256=%s\n' \
  "$qemu_version" "$qemu_version" "$qemu_sha256" "$patch_sha256" > "$output_dir/qemu-version.txt"
"$output_dir/qemu-x86_64" --version
qemu_help=$("$output_dir/qemu-x86_64" -help)
if [[ "$qemu_help" != *'-plugin'* ]]; then
  echo 'the built QEMU does not expose its plugin loader' >&2
  exit 1
fi
