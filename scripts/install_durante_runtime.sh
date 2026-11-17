#!/bin/sh
set -eu

if [ "$#" -ne 4 ]; then
  echo 'usage: install_durante_runtime.sh SDK CLIENT VENDORED_SOURCE OUTPUT_DIRECTORY' >&2
  exit 2
fi
sdk=$1
client=$2
vendor=$3
output=$4

# Package the same prepared native runtime for policy-only and measured runs.
# Headers, debug symbols, build tools and unrelated SDK executables stay out.
install -d -m 0755 "$output/dynamorio/bin64" "$output/lib"
install -m 0755 "$sdk/bin64/drrun" "$output/dynamorio/bin64/drrun"
for arch in lib64 lib32; do
  for profile in release debug; do
    install -d -m 0755 "$output/dynamorio/$arch/$profile"
    # drrun execs the core itself during injection; it is also an ELF loader.
    install -m 0755 "$sdk/$arch/$profile/libdynamorio.so" "$output/dynamorio/$arch/$profile/libdynamorio.so"
  done
done
for profile in release debug; do
  install -m 0644 "$sdk/lib64/$profile/libdrpreload.so" "$output/dynamorio/lib64/$profile/libdrpreload.so"
  install -d -m 0755 "$output/dynamorio/ext/lib64/$profile"
  install -m 0644 "$sdk/ext/lib64/$profile/libdrmgr.so" "$output/dynamorio/ext/lib64/$profile/libdrmgr.so"
done
install -m 0644 "$sdk/.durante-runtime.json" "$output/dynamorio/.durante-runtime.json"
install -m 0644 "$client" "$output/lib/libdurante.so"
install -m 0644 "$sdk/License.txt" "$output/DynamoRIO-LICENSE.txt"
install -m 0644 "$vendor/LICENSE" "$vendor/UPSTREAM.json" "$output/"
"$output/dynamorio/bin64/drrun" -version
