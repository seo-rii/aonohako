#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
    printf '%s\n' 'usage: build_cpu_policy_plugin.sh OUTPUT [QEMU_PLUGIN_API_VERSION]' >&2
    exit 2
fi
api_version=${2:-4}
case "$api_version" in
    1|4) ;;
    *) printf '%s\n' 'supported QEMU plugin API versions: 4 (production), 1 (local QEMU 7 tests)' >&2; exit 2 ;;
esac
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
"${CC:-cc}" -std=c11 -O2 -Wall -Wextra -Werror -fPIC -fvisibility=hidden \
    -shared -Wl,-z,relro,-z,now -DAONOHAKO_QEMU_PLUGIN_API_VERSION="$api_version" \
    "$script_dir/../runtime/cpu-policy.c" -o "$1"
