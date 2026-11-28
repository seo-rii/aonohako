#!/bin/bash
set -euo pipefail
umask 022
root=$(cd -- "$(dirname -- "$0")" && pwd)
case "${1:-}" in primary) arms=(v12 v13) ;; repeat) arms=(v13 v12) ;; *) exit 2 ;; esac
test "$#" = 1
test ! -e "$root/output"
mkdir "$root/output"
for arm in "${arms[@]}"; do
    driver=$(mktemp -d "/var/tmp/aonohako-transfer-${arm}-XXXXXXXX")
    chmod 0755 "$driver"
    printf '%s\n' "$arm" > "$driver/arm"
    ln -s "$root/guest.py" "$driver/guest.py"
    if [[ $arm == v12 ]]; then img="$root/pilot/runtime.squashfs"; else img="$root/v13.squashfs"; fi
    ln -s "$img" "$driver/runtime.squashfs"
    ln -s "$root/pilot/guest.sh" "$driver/bootstrap.sh"
    # Bootstrap mounts this arm at the production path in a CPU-1 systemd scope.
    rc=0
    bash "$driver/bootstrap.sh" "$1" "$root/plan.json" || rc=$?
    rm -rf -- "$driver"
    (( rc == 0 )) || exit "$rc"
done
