#!/usr/bin/env bash
set -euo pipefail

assets="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
go="${AONOHAKO_GO:-/usr/local/go/bin/go}"
target="${1:-/usr/local/bin/umjunsik-lang-go}"
case "$target" in
  /*) ;;
  *) target="$PWD/$target" ;;
esac
revision="$(cat "$assets/REVISION")"
[[ "$revision" =~ ^[0-9a-f]{40}$ ]]
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT

git init --quiet "$work/source"
git -C "$work/source" remote add origin https://github.com/rycont/umjunsik-lang.git
git -C "$work/source" fetch --quiet --depth=1 origin "$revision"
git -C "$work/source" checkout --quiet --detach FETCH_HEAD
test "$(git -C "$work/source" rev-parse HEAD)" = "$revision"
git -C "$work/source" apply --check "$assets/judge.patch"
git -C "$work/source" apply "$assets/judge.patch"

# No dependency or toolchain downloads are needed by this stdlib-only runtime.
# Keep build caches out of the resulting runtime image as well.
unset GOROOT
export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS=
export GOPROXY=off GOSUMDB=off CGO_ENABLED=0
export GOCACHE="$work/go-cache" GOMODCACHE="$work/go-modcache"
(
  cd "$work/source/umjunsik-lang-go"
  "$go" build -trimpath -o "$work/umjunsik-lang-go" .
)
# Test the actual binary before installing it. Explicit-file mode also keeps
# this regression suite independent of aonohako's module and dependencies.
GO111MODULE=off UHMLANG_BINARY="$work/umjunsik-lang-go" \
  "$go" test -count=1 -timeout=60s -v "$assets/regression_test.go"
install -m 0755 "$work/umjunsik-lang-go" "$target"
