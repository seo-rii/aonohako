#!/bin/bash
set -euo pipefail
root=$(cd -- "$(dirname -- "$0")" && pwd)
test "$#" = 1
case "$1" in primary|repeat) ;; *) exit 2 ;; esac
test ! -e "$root/output"
# Use the registered pilot's read-only SquashFS and CPU1 systemd scope.
driver=$(mktemp -d /var/tmp/aonohako-refit-XXXXXXXX)
trap 'rm -rf -- "$driver"' EXIT
chmod 0755 "$driver"
ln -s "$root/guest.py" "$driver/guest.py"
ln -s "$root/pilot/runtime.squashfs" "$driver/runtime.squashfs"
ln -s "$root/pilot/guest.sh" "$driver/bootstrap.sh"
bash "$driver/bootstrap.sh" "$1" "$root/freeze.json"
