#!/bin/bash
# Run from the authenticated payload on a disposable GCE guest.
set -euo pipefail
umask 022
cd "$(dirname "$0")"
phase=${1:?calibrate, primary or repeat}
case "$phase" in
    calibrate) test "$#" = 1 ;;
    primary|repeat) test "$#" = 2; test -f "$2" ;;
    *) exit 2 ;;
esac
test "$(id -u)" = 0
test ! -e output
test ! -e /opt/durante
stage=$(mktemp -d /var/tmp/aonohako-judge-pilot-XXXXXXXX)
chmod 0755 "$stage"
trace=/sys/kernel/tracing
instance="$trace/instances/$(basename "$stage")"
cleanup() {
    if mountpoint -q "$stage/trace"; then umount "$stage/trace"; fi
    if test -d "$instance"; then
        echo 0 > "$instance/tracing_on"
        echo 0 > "$instance/events/sched/sched_process_exec/enable"
        rmdir "$instance"
    fi
    if mountpoint -q "$stage/work"; then umount "$stage/work"; fi
    if mountpoint -q /opt/durante; then umount /opt/durante; fi
    rmdir /opt/durante
    rm -rf -- "$stage"
}
install -d -m 0755 /opt/durante
trap cleanup EXIT
mount -t squashfs -o ro,loop,nodev,nosuid "$PWD/runtime.squashfs" /opt/durante
mkdir "$stage/work" "$stage/trace"
mount -t tmpfs -o rw,exec,nosuid,nodev,mode=0755,size=256m,nr_inodes=1048576 tmpfs "$stage/work"
if ! mountpoint -q "$trace"; then mount -t tracefs tracefs "$trace"; fi
mkdir "$instance"
echo 0 > "$instance/tracing_on"
echo 4096 > "$instance/buffer_size_kb"
echo 1 > "$instance/events/sched/sched_process_exec/enable"
echo 1 > "$instance/tracing_on"
mount --bind "$instance" "$stage/trace"
args=("$phase" --work-root "$stage/work" --trace-dir "$stage/trace")
if test "$phase" != calibrate; then args+=(--frozen "$(realpath "$2")"); fi
systemd-run --quiet --wait --pipe --collect --unit="$(basename "$stage")" \
    -p "WorkingDirectory=$PWD" -p AllowedCPUs=1 -p PrivateNetwork=yes -p RuntimeMaxSec=1800 \
    /usr/bin/taskset -c 1 /usr/bin/python3 -B "$PWD/guest.py" "${args[@]}"
