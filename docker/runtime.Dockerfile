# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.23-bookworm
ARG RUNTIME_BASE=debian:trixie-slim
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

RUN if [[ -n "${NPM_PACKAGES}" ]]; then \
      npm install --global ${NPM_PACKAGES}; \
    fi

COPY --from=builder /usr/local/go /usr/local/go

RUN if [[ -n "${INSTALL_SCRIPT}" ]]; then \
      /bin/bash -euo pipefail -c "${INSTALL_SCRIPT}"; \
    fi

COPY --from=builder /out/aonohako /usr/local/bin/aonohako
COPY --from=builder /out/aonohako-selftest /usr/local/bin/aonohako-selftest
COPY scripts/smoke_runtime.sh /usr/local/bin/aonohako-smoke
COPY --chmod=0644 scripts/brainfuck.py /usr/local/lib/aonohako/brainfuck.py
COPY --chmod=0644 scripts/whitespace.py /usr/local/lib/aonohako/whitespace.py
COPY python/ /usr/local/lib/aonohako/python/
COPY --chmod=0755 scripts/runtime_entrypoint.sh /usr/local/bin/aonohako-entrypoint

RUN find /usr/local/lib/aonohako/python -type d -exec chmod 0755 {} + && \
    find /usr/local/lib/aonohako/python -type f -exec chmod 0644 {} + && \
    install -d -m 0700 /var/aonohako /var/aonohako/protected && \
    printf 'runtime-owned\n' > /var/aonohako/protected/probe.txt && \
    chmod 0700 /var/aonohako /var/aonohako/protected && \
    chmod 0600 /var/aonohako/protected/probe.txt && \
    for path in /etc/debian_version /etc/os-release /etc/issue /etc/issue.net /etc/motd; do \
      if [[ -e "${path}" ]]; then chmod 0600 "${path}"; fi; \
    done && \
    for path in /usr/share/doc /usr/share/info /usr/share/man /usr/share/lintian /usr/share/bug /var/cache/apt /var/lib/apt /var/lib/dpkg; do \
      if [[ -e "${path}" ]]; then chmod -R go-rwx "${path}"; fi; \
    done && \
    chmod 0700 /root && \
    if [[ -d /var/log ]]; then chmod 0700 /var/log; fi && \
    if [[ -d /var/spool ]]; then chmod 0700 /var/spool; fi && \
    if [[ -d /var/mail ]]; then chmod 0700 /var/mail; fi && \
    if [[ -d /etc/ssl/private ]]; then chmod 0700 /etc/ssl/private; fi

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
