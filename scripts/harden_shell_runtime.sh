#!/usr/bin/env bash
set -euo pipefail

allowlist=/usr/local/lib/aonohako/shell_runtime_allowlist.txt
test -s "${allowlist}"

for root in /usr/bin /usr/sbin /usr/local/bin /usr/local/sbin /usr/local/go/bin /usr/local/go/pkg/tool /usr/local/lib/aonohako /usr/libexec; do
	if [[ ! -d "${root}" ]]; then
		continue
	fi
	find "${root}" -xdev -type f -perm /0111 -exec chown root:root {} + -exec chmod 0750 {} +
done

while IFS= read -r -d '' link; do
	target="$(readlink -f -- "${link}")"
	if [[ -f "${target}" ]]; then
		chown root:root "${target}"
		chmod 0750 "${target}"
	fi
done < <(find /usr/bin /usr/sbin /usr/local/bin /usr/local/sbin -xdev -type l -print0)

for root in /usr/local/go /usr/local/cargo /usr/lib/apt /usr/lib/dpkg /usr/lib/git-core /usr/lib/init /usr/lib/openssh /usr/lib/ssl/misc /usr/lib/systemd /usr/lib/util-linux; do
	if [[ -e "${root}" ]]; then
		chmod -R go-rwx "${root}"
	fi
done

find / -xdev -type f -perm /6000 -exec chmod ug-s {} +

library_inventory="$(mktemp)"
for root in /usr/lib /usr/local/lib; do
	if [[ ! -d "${root}" ]]; then
		continue
	fi
	find "${root}" -xdev -type f -perm /0001 -print0 > "${library_inventory}"
	while IFS= read -r -d '' path; do
		magic="$(od -An -tx1 -N4 -- "${path}" | tr -d '[:space:]')"
		if [[ "${magic}" != "7f454c46" ]]; then
			chmod go-x "${path}"
		fi
	done < "${library_inventory}"
done
rm -f "${library_inventory}"

while IFS= read -r path; do
	if [[ -z "${path}" || "${path}" == \#* ]]; then
		continue
	fi
	if [[ ! -e "${path}" ]]; then
		echo "shell allowlist path is missing: ${path}" >&2
		exit 1
	fi
	target="$(readlink -f -- "${path}")"
	test -f "${target}"
	chown root:root "${target}"
	chmod 0755 "${target}"
done < "${allowlist}"

while IFS= read -r -d '' loader; do
	chown root:root "${loader}"
	chmod 0755 "${loader}"
done < <(find /lib /usr/lib -xdev -type f -name 'ld-linux*.so*' -print0 2>/dev/null)

while IFS= read -r -d '' libc; do
	chown root:root "${libc}"
	chmod 0644 "${libc}"
done < <(find /lib /usr/lib -xdev -type f -name 'libc.so.6' -print0 2>/dev/null)

chown root:root "${allowlist}"
chmod 0444 "${allowlist}"
