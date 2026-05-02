#!/usr/bin/env bash
set -euo pipefail

src="${1:-Main.cu}"
out="${2:-Main}"
nvcc_bin="${NVCC:-/usr/local/cuda/bin/nvcc}"
ocelot_lib_dir="${GPUOCELOT_LIB_DIR:-/usr/lib}"
ocelot_lib="${GPUOCELOT_LIB:-gpuocelot}"

exec "${nvcc_bin}" \
  -std=c++17 \
  -O2 \
  -arch=sm_35 \
  -Xptxas -v \
  "${src}" \
  -o "${out}" \
  -L"${ocelot_lib_dir}" \
  -l"${ocelot_lib}" \
  -Xlinker -rpath \
  -Xlinker "${ocelot_lib_dir}"
