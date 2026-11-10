#!/bin/sh
set -eu
child() {
  n=$1
  x=1
  i=0
  while [ "$i" -lt "$n" ]; do
    x=$(( (x * 1664525 + 1013904223) & 4294967295 ))
    i=$((i + 1))
  done
  printf '%s\n' "$x"
}
child 1000 &
pid=$!
wait "$pid"
printf 'parent-done\n'
