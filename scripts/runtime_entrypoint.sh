#!/bin/sh
set -eu

if [ "$(id -u)" -eq 0 ]; then
	for path in /dev/shm /dev/mqueue; do
		if [ -d "${path}" ]; then
			chmod 0755 "${path}"
		fi
	done
fi

if [ "${AONOHAKO_DEPLOYMENT_TARGET:-}" = "cloudrun" ] && \
	[ -n "${AONOHAKO_WORK_ROOT:-}" ] && \
	[ -d "${AONOHAKO_WORK_ROOT}" ]; then
	chmod 0711 "${AONOHAKO_WORK_ROOT}"
fi

exec "$@"
