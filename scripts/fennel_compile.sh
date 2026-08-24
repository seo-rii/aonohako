#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: aonohako-fennel-compile SOURCE TARGET" >&2
	exit 2
fi

source_path=$1
target_path=$2
umask 077

case ${target_path} in
	/*)
		target_dir=${target_path%/*}
		[ -n "${target_dir}" ] || target_dir=/
		;;
	*/*) target_dir=${target_path%/*} ;;
	*) target_dir=. ;;
esac

temp_dir=
compiled_path=
compiler_stdout_path=
guarded_path=
cleanup() {
	[ -z "${compiled_path}" ] || rm -f -- "${compiled_path}"
	[ -z "${compiler_stdout_path}" ] || rm -f -- "${compiler_stdout_path}"
	[ -z "${guarded_path}" ] || rm -f -- "${guarded_path}"
	[ -z "${temp_dir}" ] || rmdir -- "${temp_dir}" 2>/dev/null || true
}
on_signal() {
	signal_number=$1
	trap - 0 1 2 3 15
	cleanup
	exit $((128 + signal_number))
}
trap cleanup 0
trap 'on_signal 1' 1
trap 'on_signal 2' 2
trap 'on_signal 3' 3
trap 'on_signal 15' 15

temp_dir=$(mktemp -d "${target_dir}/.aonohako-fennel.XXXXXX")
compiled_path=${temp_dir}/compiled.lua
compiler_stdout_path=${temp_dir}/compiler.stdout
guarded_path=${temp_dir}/guarded.lua

# The trusted writer obtains Lua from Fennel's API return value. CLI stdout is
# captured separately and can never become part of the executable artifact.
fennel --no-fennelrc /usr/local/lib/aonohako/fennel_writer.fnl \
	"${source_path}" "${compiled_path}" >"${compiler_stdout_path}"
if [ -s "${compiler_stdout_path}" ]; then
	echo "fennel compiler emitted unexpected stdout" >&2
	exit 1
fi
luac5.4 -p -- "${compiled_path}"

{
	# Fennel compiles to ordinary Lua. Remove process spawning, the debug
	# library, and every native-module loader before submission code runs.
	printf '%s\n' \
		'if os then os.execute = nil end' \
		'if io then io.popen = nil end' \
		'debug = nil' \
		'if package then' \
		'  if package.loaded then package.loaded.debug = nil end' \
		'  if package.preload then' \
		'    for name in pairs(package.preload) do package.preload[name] = nil end' \
		'  end' \
		'  package.loadlib = nil' \
		"  package.cpath = ''" \
		'  if package.searchers then' \
		'    package.searchers[4] = nil' \
		'    package.searchers[3] = nil' \
		'  end' \
		'  if package.loaders then' \
		'    package.loaders[4] = nil' \
		'    package.loaders[3] = nil' \
		'  end' \
		'end'
	cat -- "${compiled_path}"
} >"${guarded_path}"

luac5.4 -p -- "${guarded_path}"
mv -f -- "${guarded_path}" "${target_path}"
