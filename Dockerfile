# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

ARG GO_IMAGE=golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0
ARG RUNTIME_BASE=debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2
ARG DOTNET_SDK_IMAGE=mcr.microsoft.com/dotnet/sdk:8.0@sha256:4b1cdaa57eed2cecabcf29bdb9bce11e8ca1c287d39dfd2c8b534663ea94d493
ARG PYTHON_IMAGE=python:3.13-slim-trixie@sha256:eb43ff125d8d58d7449dcba7d336c23bcac412f526d861db493b9994d8010280

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako ./cmd/server

FROM ${RUNTIME_BASE} AS cpu-policy-builder
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl xz-utils patch gcc libc6-dev make ninja-build python3 python3-venv \
    pkg-config libglib2.0-dev libcapstone-dev zlib1g-dev && rm -rf /var/lib/apt/lists/*
COPY --chmod=0755 scripts/build_cpu_policy_qemu.sh scripts/build_cpu_policy_plugin.sh scripts/test_cpu_policy_runtime.py /src/scripts/
COPY runtime/cpu-policy.c runtime/qemu-plugin-compat.h /src/runtime/
COPY runtime/qemu-10.0.15-guest-bounds.patch /src/runtime/
RUN /src/scripts/build_cpu_policy_qemu.sh /out/cpu-policy && \
    /src/scripts/build_cpu_policy_plugin.sh /out/cpu-policy/cpu-policy.so && \
    python3 /src/scripts/test_cpu_policy_runtime.py /out/cpu-policy && \
    chmod 0444 /out/cpu-policy/cpu-policy.so

# PYTHON_IMAGE may use a different distribution or library ABI than RUNTIME_BASE.
FROM ${PYTHON_IMAGE} AS cpu-policy-python-builder
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl xz-utils patch gcc libc6-dev make ninja-build python3 python3-venv \
    pkg-config libglib2.0-dev libcapstone-dev zlib1g-dev && rm -rf /var/lib/apt/lists/*
COPY --chmod=0755 scripts/build_cpu_policy_qemu.sh scripts/build_cpu_policy_plugin.sh scripts/test_cpu_policy_runtime.py /src/scripts/
COPY runtime/cpu-policy.c runtime/qemu-plugin-compat.h /src/runtime/
COPY runtime/qemu-10.0.15-guest-bounds.patch /src/runtime/
RUN /src/scripts/build_cpu_policy_qemu.sh /out/cpu-policy && \
    /src/scripts/build_cpu_policy_plugin.sh /out/cpu-policy/cpu-policy.so && \
    python3 /src/scripts/test_cpu_policy_runtime.py /out/cpu-policy && \
    chmod 0444 /out/cpu-policy/cpu-policy.so

FROM ${RUNTIME_BASE} AS base
RUN --mount=type=bind,source=scripts/cpu_policy_runtime_packages.sh,target=/tmp/cpu-policy-runtime-packages.sh,ro \
    apt-get update && \
    runtime_packages="$(sh /tmp/cpu-policy-runtime-packages.sh)" && \
    apt-get install -y --no-install-recommends ca-certificates tini ${runtime_packages} && \
    rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/aonohako /usr/local/bin/aonohako
COPY --from=cpu-policy-builder /out/cpu-policy/ /usr/local/lib/aonohako/cpu-policy/
RUN /usr/local/lib/aonohako/cpu-policy/qemu-x86_64 --version && \
    plugin_libraries="$(ldd /usr/local/lib/aonohako/cpu-policy/cpu-policy.so)" && \
    case "$plugin_libraries" in *"not found"*) printf '%s\n' "$plugin_libraries" >&2; exit 1 ;; esac
ENV PATH=/usr/local/bin:/usr/bin:/bin LANG=C.UTF-8 LC_ALL=C.UTF-8
ENTRYPOINT ["/usr/bin/tini","--"]
CMD ["aonohako"]

# Compile images
FROM base AS compile-native
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc g++ make \
    golang-go \
    rustc cargo \
    python3 python3-pip pypy3 \
    nodejs npm \
    ruby php lua5.4 perl \
    && rm -rf /var/lib/apt/lists/*

FROM base AS compile-jvm
RUN apt-get update && apt-get install -y --no-install-recommends \
    openjdk-21-jdk-headless \
    && rm -rf /var/lib/apt/lists/*

FROM ${DOTNET_SDK_IMAGE} AS compile-dotnet
COPY --from=builder /out/aonohako /usr/local/bin/aonohako
ENV PATH=/usr/local/bin:/usr/bin:/bin LANG=C.UTF-8 LC_ALL=C.UTF-8
ENTRYPOINT ["aonohako"]

FROM base AS compile-script
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip pypy3 \
    nodejs npm \
    ruby php lua5.4 perl \
    && rm -rf /var/lib/apt/lists/*

# Execute images
FROM base AS execute-native
RUN apt-get update && apt-get install -y --no-install-recommends \
    libstdc++6 \
    && rm -rf /var/lib/apt/lists/*

FROM ${PYTHON_IMAGE} AS execute-python
RUN --mount=type=bind,source=scripts/cpu_policy_runtime_packages.sh,target=/tmp/cpu-policy-runtime-packages.sh,ro \
    apt-get update && \
    runtime_packages="$(sh /tmp/cpu-policy-runtime-packages.sh)" && \
    apt-get install -y --no-install-recommends \
    tini pypy3 openjdk-21-jre-headless \
    ca-certificates curl bzip2 ${runtime_packages} \
    && rm -rf /var/lib/apt/lists/*
# Install patched filelock from PyPI before using the CPU PyTorch index.
RUN python -m pip install --no-cache-dir --upgrade pip && \
    python -m pip install --no-cache-dir \
      setuptools==84.1.0 filelock==4.2.1 \
      numpy==2.4.4 pandas==3.0.2 seaborn==0.13.2 matplotlib==3.10.8 pillow==13.0.0 \
      six==1.17.0 qiskit==2.4.0 pyparsing==3.3.2 pylatexenc==2.10 jax[cpu]==0.10.0 && \
    python -m pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu \
      torch==2.13.0+cpu torchvision==0.28.0+cpu
RUN install -d -m 0755 /usr/local/lib/aonohako/python
COPY --from=builder /out/aonohako /usr/local/bin/aonohako
COPY --from=cpu-policy-python-builder /out/cpu-policy/ /usr/local/lib/aonohako/cpu-policy/
RUN /usr/local/lib/aonohako/cpu-policy/qemu-x86_64 --version && \
    plugin_libraries="$(ldd /usr/local/lib/aonohako/cpu-policy/cpu-policy.so)" && \
    case "$plugin_libraries" in *"not found"*) printf '%s\n' "$plugin_libraries" >&2; exit 1 ;; esac
ENV PATH=/usr/local/bin:/usr/bin:/bin LANG=C.UTF-8 LC_ALL=C.UTF-8 PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PYTHONPATH=/usr/local/lib/aonohako/python
ENTRYPOINT ["/usr/bin/tini","--"]
CMD ["aonohako"]

FROM base AS execute-jvm
RUN apt-get update && apt-get install -y --no-install-recommends \
    openjdk-21-jre-headless \
    && rm -rf /var/lib/apt/lists/*

FROM base AS execute-node
RUN apt-get update && apt-get install -y --no-install-recommends \
    nodejs npm \
    && rm -rf /var/lib/apt/lists/*

FROM base AS execute-script
RUN apt-get update && apt-get install -y --no-install-recommends \
    ruby php lua5.4 perl \
    && rm -rf /var/lib/apt/lists/*
