# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.26-bookworm@sha256:47ce5636e9936b2c5cbf708925578ef386b4f8872aec74a67bd13a627d242b19
ARG RUNTIME_BASE=debian:trixie-slim@sha256:cedb1ef40439206b673ee8b33a46a03a0c9fa90bf3732f54704f99cb061d2c5a
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako ./cmd/server
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako-selftest ./cmd/selftest

FROM ${RUNTIME_BASE} AS runtime

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG IMAGE_NAME=runtime
ARG LANGUAGES=
ARG APT_PACKAGES=
ARG PIP_PACKAGES=
ARG NPM_PACKAGES=
ARG INSTALL_SCRIPT=
ARG SMOKE_COMMAND=

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates coreutils tini util-linux && \
    if [[ -n "${APT_PACKAGES}" ]]; then \
      apt-get install -y --no-install-recommends ${APT_PACKAGES}; \
    fi && \
    rm -rf /var/lib/apt/lists/*

RUN if [[ -n "${PIP_PACKAGES}" ]]; then \
      python3 -m pip install --break-system-packages --no-cache-dir ${PIP_PACKAGES}; \
    fi

COPY --from=builder /usr/local/go /usr/local/go

RUN if [[ -n "${INSTALL_SCRIPT}" ]]; then \
      /bin/bash -euo pipefail -c "${INSTALL_SCRIPT}"; \
    fi

RUN if [[ -n "${NPM_PACKAGES}" ]]; then \
      env NPM_CONFIG_PREFIX=/usr/local npm install --global ${NPM_PACKAGES}; \
    fi

RUN install -d -m 0755 /usr/local/lib/aonohako

COPY --from=builder /out/aonohako /usr/local/bin/aonohako
COPY --from=builder /out/aonohako-selftest /usr/local/bin/aonohako-selftest
COPY scripts/smoke_runtime.sh /usr/local/bin/aonohako-smoke
COPY --chmod=0644 scripts/brainfuck.py /usr/local/lib/aonohako/brainfuck.py
COPY --chmod=0644 scripts/whitespace.py /usr/local/lib/aonohako/whitespace.py
COPY python/ /usr/local/lib/aonohako/python/
COPY --chmod=0755 scripts/runtime_entrypoint.sh /usr/local/bin/aonohako-entrypoint

RUN chmod 0755 /usr/local/lib/aonohako && \
    chmod 0644 /usr/local/lib/aonohako/brainfuck.py /usr/local/lib/aonohako/whitespace.py && \
    find /usr/local/lib/aonohako/python -type d -exec chmod 0755 {} + && \
    find /usr/local/lib/aonohako/python -type f -exec chmod 0644 {} + && \
    install -d -m 0700 /var/aonohako /var/aonohako/protected && \
    printf 'runtime-owned\n' > /var/aonohako/protected/probe.txt && \
    chmod 0700 /var/aonohako /var/aonohako/protected && \
    chmod 0600 /var/aonohako/protected/probe.txt && \
    for path in /etc/debian_version /etc/os-release /etc/issue /etc/issue.net /etc/motd /etc/passwd /etc/group /etc/gshadow /etc/shadow /etc/subuid /etc/subgid /etc/shells /etc/login.defs; do \
      if [[ -e "${path}" ]]; then chmod 0600 "${path}"; fi; \
    done && \
    for path in /usr/share/doc /usr/share/info /usr/share/man /usr/share/lintian /usr/share/bug /usr/share/common-licenses /usr/share/bash-completion /var/cache/apt /var/cache/debconf /var/lib/apt /var/lib/dpkg /var/lib/systemd /etc/apt; do \
      if [[ -e "${path}" ]]; then chmod -R go-rwx "${path}"; fi; \
    done && \
    for path in /tmp /var/tmp /run/lock /dev/shm /dev/mqueue; do \
      if [[ -d "${path}" ]]; then chmod 0755 "${path}"; fi; \
    done && \
    install -d -m 0700 /tmp/.dotnet /tmp/.dotnet/shm /tmp/.dotnet/shm/global /tmp/.dotnet/lockfiles /tmp/.dotnet/lockfiles/global && \
    chown -R 65532:65532 /tmp/.dotnet && \
    chmod 0700 /root && \
    if [[ -d /var/log ]]; then chmod 0700 /var/log; fi && \
    if [[ -d /var/spool ]]; then chmod 0700 /var/spool; fi && \
    if [[ -d /var/mail ]]; then chmod 0700 /var/mail; fi && \
    if [[ -d /etc/ssl/private ]]; then chmod 0700 /etc/ssl/private; fi && \
    for tool in apt apt-get apt-cache apt-config dpkg dpkg-query dpkg-deb curl wget git pip pip3 npm npx yarn pnpm gem bundle bundler ssh scp sftp rsync nc netcat ncat socat telnet ftp lftp gdb gdbserver strace ltrace tcpdump tshark wireshark nmap dig nslookup host ip ss ifconfig route ping ping6 traceroute tracepath arp arping; do \
      if command -v "${tool}" >/dev/null 2>&1; then chmod 0750 "$(command -v "${tool}")"; fi; \
    done && \
    shopt -s nullglob && \
    for path in /usr/lib/python*/dist-packages/pip /usr/local/lib/python*/dist-packages/pip /usr/lib/python*/site-packages/pip /usr/local/lib/python*/site-packages/pip /usr/local/lib/node_modules/npm /opt/node-*/lib/node_modules/npm; do \
      chmod -R go-rwx "${path}"; \
    done

ENV PATH=/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    PYTHONPATH=/usr/local/lib/aonohako/python \
    RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo \
    AONOHAKO_IMAGE_NAME=${IMAGE_NAME} \
    AONOHAKO_LANGUAGES=${LANGUAGES} \
    AONOHAKO_SMOKE_COMMAND=${SMOKE_COMMAND}

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/aonohako-entrypoint"]
CMD ["aonohako"]
