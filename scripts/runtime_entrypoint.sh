#!/bin/sh
set -eu

if [ "${AONOHAKO_DEPLOYMENT_TARGET:-}" = "cloudrun" ] && \
	[ -n "${AONOHAKO_WORK_ROOT:-}" ] && \
	[ -d "${AONOHAKO_WORK_ROOT}" ]; then
	chmod 0711 "${AONOHAKO_WORK_ROOT}"
fi

exec "$@"
