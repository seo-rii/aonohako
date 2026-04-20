#!/bin/sh
set -eu

scratch_dirs=${AONOHAKO_SCRATCH_DIRS:-/tmp /var/tmp /run/lock /dev/shm /dev/mqueue}

for dir in $scratch_dirs; do
	if [ -d "$dir" ]; then
		chmod 0755 "$dir"
	fi
done

exec "$@"
