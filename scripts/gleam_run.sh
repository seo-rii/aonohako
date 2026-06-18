#!/usr/bin/env bash
set -euo pipefail

project="${1:?usage: aonohako-gleam-run <project-dir>}"

export HOME="${AONOHAKO_GLEAM_HOME:-/usr/local/lib/aonohako/gleam-home}"
export EMU="${EMU:-beam}"
export ROOTDIR="${ROOTDIR:-/usr/lib/erlang}"
export PROGNAME="${PROGNAME:-erl}"
if [ -z "${BINDIR:-}" ]; then
	erlexec="$(find "$ROOTDIR" -path '*/erts-*/bin/erlexec' -type f 2>/dev/null | sort -V | tail -n 1 || true)"
	if [ -n "$erlexec" ]; then
		export BINDIR="${erlexec%/*}"
	fi
fi
export ERL_AFLAGS="${ERL_AFLAGS:-+MIscs 128 +S 1:1 +A 1 +MMscs 0}"

cd "${project}"
exec gleam run -m main
