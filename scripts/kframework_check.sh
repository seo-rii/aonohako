#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "usage: aonohako-kframework-check <Main.k>" >&2
  exit 2
fi

src="$1"
base="$(basename "${src}")"
stem="${base%.*}"
export JAVA_TOOL_OPTIONS="${JAVA_TOOL_OPTIONS:--Djava.awt.headless=true -Xms64m -Xmx2048m -Xss1m -XX:+UseSerialGC}"

kompile "${src}" --backend llvm
rm -rf "${stem}-kompiled" "${src%.*}-kompiled"
