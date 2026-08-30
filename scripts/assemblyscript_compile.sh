#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 || "$1" != /* || "$2" != /* ]]; then
    echo "usage: $0 <absolute-source.ts> <absolute-target.wasm>" >&2
    exit 2
fi

source_path="$1"
target_path="$2"

cd /usr/local/lib/node_modules/@assemblyscript/wasi-shim
exec /usr/local/bin/node \
    --enable-source-maps \
    /usr/local/lib/node_modules/assemblyscript/bin/asc.js \
    "${source_path}" \
    --config ./asconfig.json \
    --runtime incremental \
    --optimize \
    --noColors \
    --outFile "${target_path}"
