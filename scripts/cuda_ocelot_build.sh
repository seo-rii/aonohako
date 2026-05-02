#!/usr/bin/env bash
set -euo pipefail

src="${1:-Main.cu}"
out="${2:-Main}"

exec nvcc \
  -std=c++17 \
  -O2 \
  -arch=sm_35 \
  -Xptxas -v \
  "${src}" \
  -o "${out}" \
  -L/opt/gpuocelot/lib \
  -locelot \
  -Wl,-rpath,/opt/gpuocelot/lib
