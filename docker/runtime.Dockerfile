# syntax=docker/dockerfile:1.7

ARG GO_IMAGE=golang:1.23-bookworm
ARG RUNTIME_BASE=debian:bookworm-slim
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako ./cmd/server

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
      python3 -m pip install --no-cache-dir ${PIP_PACKAGES}; \
    fi

RUN if [[ -n "${NPM_PACKAGES}" ]]; then \
      npm install --global ${NPM_PACKAGES}; \
    fi

COPY --from=builder /usr/local/go /usr/local/go

RUN if [[ -n "${INSTALL_SCRIPT}" ]]; then \
      /bin/bash -euo pipefail -c "${INSTALL_SCRIPT}"; \
    fi

COPY --from=builder /out/aonohako /usr/local/bin/aonohako
COPY scripts/smoke_runtime.sh /usr/local/bin/aonohako-smoke

ENV PATH=/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    AONOHAKO_IMAGE_NAME=${IMAGE_NAME} \
    AONOHAKO_LANGUAGES=${LANGUAGES} \
    AONOHAKO_SMOKE_COMMAND=${SMOKE_COMMAND}

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["aonohako"]
