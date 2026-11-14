#!/bin/sh
set -eu

if [ "$#" -ne 0 ]; then
  echo 'usage: cpu_policy_runtime_packages.sh' >&2
  exit 2
fi
export LC_ALL=C

# Resolve the runtime ABI selected by the same development packages used in
# the builder. Debian and Ubuntu use different GLib and Capstone package names;
# newer GLib development packages reach the runtime through libgio-2.0-dev.
runtime_dependency() {
  if ! metadata=$(apt-cache depends --important --recurse "$1"); then
    echo "cannot read CPU policy runtime dependencies for $1" >&2
    return 1
  fi
  if ! package=$(printf '%s\n' "$metadata" | awk -v pattern="$2" '
    $1 == "Depends:" || $1 == "|Depends:" {
      name = $2
      sub(/:[a-zA-Z0-9_-]+$/, "", name)
      if (name ~ pattern) matches[name] = 1
    }
    END {
      for (name in matches) { count++; package = name }
      if (count != 1) exit 1
      print package
    }
  '); then
    echo "expected one CPU policy runtime dependency for $1" >&2
    return 1
  fi
  printf '%s\n' "$package"
}

glib_package=$(runtime_dependency libglib2.0-dev '^libglib2[.]0-0(t64)?$')
capstone_package=$(runtime_dependency libcapstone-dev '^libcapstone[0-9]+$')
printf '%s\n' "$glib_package" "$capstone_package" zlib1g libatomic1
