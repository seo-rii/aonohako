#!/bin/bash
set -euo pipefail
root=$(cd -- "$(dirname -- "$0")" && pwd)
case ${1-} in
    primary) engines=(v13 v12);;
    repeat) engines=(v12 v13);;
    *) exit 2;;
esac
test "$#" = 1
mkdir "$root/output"
for engine in "${engines[@]}"; do
    bash "$root/$engine/guest.sh" "$1"
    mv "$root/$engine/output/clock" "$root/output/$engine"
done
