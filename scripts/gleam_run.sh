#!/usr/bin/env bash
set -euo pipefail

project="${1:?usage: aonohako-gleam-run <project-dir>}"

export HOME="${AONOHAKO_GLEAM_HOME:-/usr/local/lib/aonohako/gleam-home}"
export ERL_AFLAGS="${ERL_AFLAGS:-+S 1:1 +SDio 1 +SDcpu 1}"

cd "${project}"
exec gleam run
