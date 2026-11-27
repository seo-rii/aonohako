#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo 'usage: build_durante_client.sh VERIFIED_VENDOR SDK BUILD_DIRECTORY' >&2
  exit 2
fi
vendor=$1
sdk=$2
build=$3
# This entry point always compiles the client whose identity it records.
# Historical v7 installations have no source marker.
commit=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["commit"])' "$vendor/UPSTREAM.json")
case "$commit" in
  41743068ef9070b6e04195bf7822c5ee513660ff) mark_source=false ;;
  614761c18d4735f00e93667c74c099cfdb2800c7|27954247e094e682a7cf47504ff13682f970469a|191858a02666c1406b871e4974b9072b2c91bb34|74d9188f9fd86c75f079277e6e5278b379a0a1d7|b43685c76b94a365886031359b032899b009cd1b) mark_source=true ;;
  *) echo 'unexpected Durante source commit' >&2; exit 1 ;;
esac
cmake -S "$vendor/client" -B "$build" -DCMAKE_BUILD_TYPE=Release -DDynamoRIO_DIR="$sdk/cmake"
cmake --build "$build" --clean-first --target durante -j2
if [ "$mark_source" = true ]; then
  printf %s "$commit" > "$build/durante.source"
  objcopy --remove-section .durante.source --add-section ".durante.source=$build/durante.source" \
    --set-section-flags .durante.source=readonly "$build/libdurante.so"
fi
