#!/usr/bin/env bash
set -euo pipefail

export OCELOT_CONFIG="${OCELOT_CONFIG:-/usr/local/share/aonohako/ocelot/configure.ocelot}"
export CUDA_VISIBLE_DEVICES=""
export LD_LIBRARY_PATH="/usr/lib:/opt/gpuocelot/lib:${LD_LIBRARY_PATH:-}"

exec "$1"
