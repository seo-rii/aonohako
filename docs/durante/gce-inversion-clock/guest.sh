#!/bin/bash
set -euo pipefail
root=$(cd -- "$(dirname -- "$0")" && pwd)
phase=${1:?primary or repeat}
case "$phase" in primary|repeat) ;; *) exit 2;; esac
test "$#" = 1
test ! -e "$root/output"
if [[ "$phase" = repeat ]]; then test -s "$root/primary-thresholds.json"; fi
driver=$(mktemp -d "/var/tmp/aonohako-inversions-XXXXXXXX")
trap 'rm -rf -- "$driver"' EXIT
chmod 0755 "$driver"
ln -s "$root/guest.py" "$driver/guest.py"
ln -s "$root/runtime.squashfs" "$driver/runtime.squashfs"
ln -s "$root/pilot/guest.sh" "$driver/bootstrap.sh"
bash "$driver/bootstrap.sh" "$phase" "$root/plan.json"
