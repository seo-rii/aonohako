#!/bin/bash
# Run once as root from the extracted payload on the disposable guest.
set -euo pipefail
cd "$(dirname "$0")"
umask 022
test "$(id -u)" = 0
test ! -e output
test ! -e /opt/durante
stage=$(mktemp -d /var/tmp/aonohako-v12-study-XXXXXXXX)
chmod 0755 "$stage"
trace=/sys/kernel/tracing
instance="$trace/instances/$(basename "$stage")"
installed=0
cleanup() {
    if mountpoint -q "$stage/trace"; then umount "$stage/trace"; fi
    if test -d "$instance"; then
        echo 0 > "$instance/tracing_on"
        echo 0 > "$instance/events/sched/sched_process_exec/enable"
        rmdir "$instance"
    fi
    if mountpoint -q "$stage/work"; then umount "$stage/work"; fi
    if test "$installed" = 1; then rm -rf -- /opt/durante; fi
    rm -rf -- "$stage"
}
trap cleanup EXIT
install -m 0755 server "$stage/server"
install -d -m 0755 /opt/durante
installed=1
cp -R durante/. /opt/durante/
chmod -R a+rX,go-w /opt/durante
mkdir "$stage/work"
mount -t tmpfs -o rw,exec,nosuid,nodev,mode=0755,size=256m,nr_inodes=1048576 tmpfs "$stage/work"
if ! mountpoint -q "$trace"; then mount -t tracefs tracefs "$trace"; fi
mkdir "$instance"
echo 0 > "$instance/tracing_on"
echo 4096 > "$instance/buffer_size_kb"
echo 1 > "$instance/events/sched/sched_process_exec/enable"
echo 1 > "$instance/tracing_on"
# PrivateNetwork can remount /sys. Bind the instance into the study directory.
mkdir "$stage/trace"
mount --bind "$instance" "$stage/trace"
systemd-run --quiet --wait --pipe --collect --unit="$(basename "$stage")" \
    -p "WorkingDirectory=$PWD" -p AllowedCPUs=1 -p PrivateNetwork=yes -p RuntimeMaxSec=2700 \
    /usr/bin/taskset -c 1 /usr/bin/python3 -B "$PWD/guest.py" \
    --server "$stage/server" --work-root "$stage/work" --trace-dir "$stage/trace"
