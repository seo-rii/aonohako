#!/usr/bin/env bash
set -euo pipefail

src="${1:?usage: aonohako-carbon-run <source.carbon>}"

exec carbon compile --phase=check "${src}" >/dev/null
