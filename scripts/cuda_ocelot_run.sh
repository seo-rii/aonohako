#!/usr/bin/env bash
set -euo pipefail

export OCELOT_CONFIG="${OCELOT_CONFIG:-/usr/local/share/aonohako/ocelot/configure.ocelot}"
export CUDA_VISIBLE_DEVICES=""

exec "$1"
