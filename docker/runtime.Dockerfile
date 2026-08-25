# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

ARG GO_IMAGE=golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36
ARG RUNTIME_BASE=debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go-toolchain

FROM go-toolchain AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako ./cmd/server
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako-selftest ./cmd/selftest

FROM builder AS ci-artifact-builder

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako-runtime-builder ./cmd/runtime-builder

FROM scratch AS aonohako-runtime-binaries

COPY --chmod=0755 --from=builder /out/aonohako /aonohako
COPY --chmod=0755 --from=builder /out/aonohako-selftest /aonohako-selftest

FROM scratch AS ci-runtime-artifacts

COPY --from=aonohako-runtime-binaries / /
COPY --chmod=0755 --from=ci-artifact-builder /out/aonohako-runtime-builder /aonohako-runtime-builder

FROM ${RUNTIME_BASE} AS runtime-foundation

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates coreutils tini util-linux && \
    rm -rf /var/lib/apt/lists/*

FROM runtime-foundation AS runtime-toolchain

ARG APT_PACKAGES=
ARG PIP_PACKAGES=
ARG NPM_PACKAGES=
ARG INSTALL_SCRIPT=
ARG GO_MODULE_ISOLATION=false

RUN if [[ -n "${APT_PACKAGES}" ]]; then \
      apt-get update && \
      apt-get install -y --no-install-recommends ${APT_PACKAGES}; \
    fi && \
    rm -rf /var/lib/apt/lists/*

RUN if [[ -n "${PIP_PACKAGES}" ]]; then \
      python3 -m pip install --break-system-packages --no-cache-dir ${PIP_PACKAGES}; \
    fi

COPY --from=go-toolchain /usr/local/go /usr/local/go

RUN --mount=type=bind,source=go-modules,target=/tmp/aonohako-go-modules,ro \
    if [[ "${GO_MODULE_ISOLATION}" == "true" ]]; then \
      install -d -m 0755 /usr/local/lib/aonohako/go-modcache /tmp/aonohako-go-build-cache && \
      cd /tmp/aonohako-go-modules && \
      env GOCACHE=/tmp/aonohako-go-build-cache \
        GOMODCACHE=/usr/local/lib/aonohako/go-modcache \
        GOENV=off GOTELEMETRY=off GOTOOLCHAIN=local \
        /usr/local/go/bin/go mod download all && \
      env GOMODCACHE=/usr/local/lib/aonohako/go-modcache GOENV=off GOTOOLCHAIN=local /usr/local/go/bin/go mod verify && \
      env CGO_ENABLED=0 GOCACHE=/tmp/aonohako-go-build-cache \
        GOMODCACHE=/usr/local/lib/aonohako/go-modcache \
        GOENV=off GOTELEMETRY=off GOTOOLCHAIN=local \
        /usr/local/go/bin/go test -mod=readonly ./... && \
      rm -rf /tmp/aonohako-go-build-cache; \
    fi

RUN if [[ -n "${INSTALL_SCRIPT}" ]]; then \
      env -u INSTALL_SCRIPT /bin/bash -euo pipefail -c "${INSTALL_SCRIPT}"; \
    fi

RUN if [[ -n "${NPM_PACKAGES}" ]]; then \
      env NPM_CONFIG_PREFIX=/usr/local npm install --global ${NPM_PACKAGES}; \
    fi

FROM runtime-toolchain AS runtime

ARG IMAGE_NAME=runtime
ARG LANGUAGES=
ARG SANDBOX_TOOLS=
ARG SMOKE_COMMAND=
ARG GO_MODULE_ISOLATION=false
ARG GO_EXTERNAL_MODULE_GID=65528
ARG PYTHON_LIBRARY_ISOLATION=false
ARG PYTHON_EXTERNAL_LIBRARY_GID=65530

RUN install -d -m 0755 /usr/local/lib/aonohako /usr/local/lib/aonohako/python /usr/local/include
COPY --chmod=0644 third_party/testlib/testlib.h /usr/local/include/testlib.h

COPY --chmod=0755 --from=aonohako-runtime-binaries /aonohako /usr/local/bin/aonohako
COPY --chmod=0755 --from=aonohako-runtime-binaries /aonohako-selftest /usr/local/bin/aonohako-selftest
COPY scripts/smoke_runtime.sh /usr/local/bin/aonohako-smoke
COPY --chmod=0644 scripts/brainfuck.py /usr/local/lib/aonohako/brainfuck.py
COPY --chmod=0644 scripts/whitespace.py /usr/local/lib/aonohako/whitespace.py
COPY --chmod=0644 scripts/befunge.py /usr/local/lib/aonohako/befunge.py
COPY --chmod=0644 scripts/malbolge.py /usr/local/lib/aonohako/malbolge.py
COPY --chmod=0755 scripts/apl_kanapl_runner.js /usr/local/bin/apl
COPY --chmod=0755 scripts/acl2_check.sh /usr/local/bin/aonohako-acl2-check
COPY --chmod=0755 scripts/alloy_check.py /usr/local/bin/aonohako-alloy-check
COPY --chmod=0755 scripts/why3_prove_z3.sh /usr/local/bin/aonohako-why3-prove
COPY --chmod=0755 scripts/gdl_run.sh /usr/local/bin/aonohako-gdl-run
COPY --chmod=0755 scripts/cuda_ocelot_build.sh /usr/local/bin/aonohako-cuda-ocelot-build
COPY --chmod=0755 scripts/cuda_ocelot_run.sh /usr/local/bin/aonohako-cuda-ocelot-run
COPY --chmod=0755 scripts/duckdb_run.sh /usr/local/bin/aonohako-duckdb-run
COPY --chmod=0755 scripts/gleam_run.sh /usr/local/bin/aonohako-gleam-run
COPY --chmod=0755 scripts/graphql_run.py /usr/local/bin/aonohako-graphql-run
COPY --chmod=0755 scripts/kframework_check.sh /usr/local/bin/aonohako-kframework-check
COPY --chmod=0755 scripts/tla_run.sh /usr/local/bin/aonohako-tla-run
COPY --chmod=0755 scripts/vhdl_run.sh /usr/local/bin/aonohako-vhdl-run
COPY --chmod=0755 scripts/vb6_run.rb /usr/local/bin/aonohako-vb6-run
COPY --chmod=0755 scripts/golfscript_sandboxed.rb /usr/local/lib/aonohako/golfscript_sandboxed.rb
COPY --chmod=0755 scripts/fennel_compile.sh /usr/local/bin/aonohako-fennel-compile
COPY --chmod=0644 scripts/fennel_writer.fnl /usr/local/lib/aonohako/fennel_writer.fnl
COPY --chmod=0700 scripts/harden_shell_runtime.sh /usr/local/lib/aonohako/harden_shell_runtime.sh
COPY --chmod=0644 scripts/shell_runtime_allowlist.txt /usr/local/lib/aonohako/shell_runtime_allowlist.txt
COPY --from=aonohako-python-packages / /usr/local/lib/aonohako/python/
COPY --chmod=0755 scripts/runtime_entrypoint.sh /usr/local/bin/aonohako-entrypoint

RUN chmod 0755 /usr/local/lib/aonohako && \
    rm -f /usr/local/lib/aonohako/python/.empty && \
    chmod 0644 /usr/local/lib/aonohako/brainfuck.py /usr/local/lib/aonohako/whitespace.py /usr/local/lib/aonohako/befunge.py /usr/local/lib/aonohako/malbolge.py && \
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
    install -d -o 0 -g 0 -m 0700 /tmp/.dotnet && \
    chmod 0700 /root && \
    if [[ -d /var/log ]]; then chmod 0700 /var/log; fi && \
    if [[ -d /var/spool ]]; then chmod 0700 /var/spool; fi && \
    if [[ -d /var/mail ]]; then chmod 0700 /var/mail; fi && \
    if [[ -d /etc/ssl/private ]]; then chmod 0700 /etc/ssl/private; fi && \
    for tool in apt apt-get apt-cache apt-config dpkg dpkg-query dpkg-deb curl wget git pip pip3 npm npx yarn pnpm gem bundle bundler ssh scp sftp rsync nc netcat ncat socat telnet ftp lftp gdb gdbserver strace ltrace tcpdump tshark wireshark nmap dig nslookup host ip ss ifconfig route ping ping6 traceroute tracepath arp arping; do \
      if command -v "${tool}" >/dev/null 2>&1; then chmod 0750 "$(command -v "${tool}")"; fi; \
    done && \
    for tool in ${SANDBOX_TOOLS}; do \
      if command -v "${tool}" >/dev/null 2>&1; then chmod 0755 "$(command -v "${tool}")"; fi; \
    done && \
    shopt -s nullglob && \
    if [[ "${GO_MODULE_ISOLATION}" == "true" ]]; then \
      test -d /usr/local/lib/aonohako/go-modcache && \
      chown -R "0:${GO_EXTERNAL_MODULE_GID}" /usr/local/lib/aonohako/go-modcache && \
      find /usr/local/lib/aonohako/go-modcache -type d -exec chmod 0750 {} + && \
      find /usr/local/lib/aonohako/go-modcache -type f -exec chmod 0640 {} +; \
    else \
      rm -rf /usr/local/lib/aonohako/go-modcache; \
    fi && \
    if [[ "${PYTHON_LIBRARY_ISOLATION}" == "true" ]]; then \
      for path in /usr/lib/python*/dist-packages /usr/local/lib/python*/dist-packages /usr/lib/python*/site-packages /usr/local/lib/python*/site-packages /usr/share/python-wheels /usr/local/lib/aonohako/python; do \
        if [[ ! -e "${path}" ]]; then continue; fi; \
        chown -R "0:${PYTHON_EXTERNAL_LIBRARY_GID}" "${path}"; \
        find "${path}" -type d -exec chmod 0750 {} +; \
        find "${path}" -type f -perm /0111 -exec chmod 0750 {} +; \
        find "${path}" -type f ! -perm /0111 -exec chmod 0640 {} +; \
      done; \
    fi && \
    for path in /usr/lib/python*/dist-packages/pip /usr/local/lib/python*/dist-packages/pip /usr/lib/python*/site-packages/pip /usr/local/lib/python*/site-packages/pip /usr/local/lib/node_modules/npm /opt/node-*/lib/node_modules/npm; do \
      if [[ -e "${path}" ]]; then chmod -R go-rwx "${path}"; fi; \
    done

RUN if [[ "${IMAGE_NAME}" == "type-x" || "${IMAGE_NAME}" == "ci-bash" || "${IMAGE_NAME}" == "ci-posix-sh" ]]; then \
      /usr/local/lib/aonohako/harden_shell_runtime.sh; \
    else \
      rm -f /usr/local/lib/aonohako/shell_runtime_allowlist.txt; \
    fi && \
    if [[ ",${LANGUAGES}," == *",powershell,"* ]]; then \
      for path in /var/empty /var/empty/.config /var/empty/.local /var/empty/.local/share /var/empty/.local/share/powershell /var/empty/.local/share/powershell/Modules /usr/local/share/powershell /usr/local/share/powershell/Modules; do \
        install -d -o 0 -g 0 -m 0555 "${path}"; \
      done; \
      : > /etc/passwd && chown 0:0 /etc/passwd && chmod 0444 /etc/passwd; \
    fi && \
    rm -f /usr/local/lib/aonohako/harden_shell_runtime.sh

ENV PATH=/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    PYTHONPATH=/usr/local/lib/aonohako/python \
    RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo \
    AONOHAKO_IMAGE_NAME=${IMAGE_NAME} \
    AONOHAKO_LANGUAGES=${LANGUAGES} \
    AONOHAKO_GO_MODULE_ISOLATION=${GO_MODULE_ISOLATION} \
    AONOHAKO_GO_EXTERNAL_MODULE_GID=${GO_EXTERNAL_MODULE_GID} \
    AONOHAKO_PYTHON_LIBRARY_ISOLATION=${PYTHON_LIBRARY_ISOLATION} \
    AONOHAKO_PYTHON_EXTERNAL_LIBRARY_GID=${PYTHON_EXTERNAL_LIBRARY_GID} \
    AONOHAKO_SANDBOX_TOOLS=${SANDBOX_TOOLS} \
    AONOHAKO_SMOKE_COMMAND=${SMOKE_COMMAND}

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/aonohako-entrypoint"]
CMD ["aonohako"]
