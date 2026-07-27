# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

ARG GO_IMAGE=golang:1.26.5-bookworm@sha256:18aedc16aa19b3fd7ded7245fc14b109e054d65d22ed53c355c899582bbb2113
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

FROM ${RUNTIME_BASE} AS base
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tini && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/aonohako /usr/local/bin/aonohako
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
RUN apt-get update && apt-get install -y --no-install-recommends \
    tini pypy3 openjdk-21-jre-headless \
    ca-certificates curl bzip2 \
    && rm -rf /var/lib/apt/lists/*
RUN python -m pip install --no-cache-dir --upgrade pip && \
    python -m pip install --no-cache-dir \
      setuptools==80.10.2 \
      numpy==2.4.4 pandas==3.0.2 seaborn==0.13.2 matplotlib==3.10.8 pillow==12.3.0 \
      six==1.17.0 qiskit==2.4.0 pyparsing==3.3.2 pylatexenc==2.10 jax[cpu]==0.10.0 && \
    python -m pip install --no-cache-dir --index-url https://download.pytorch.org/whl/cpu \
      torch==2.11.0+cpu torchvision==0.26.0+cpu
RUN install -d -m 0755 /usr/local/lib/aonohako/python
COPY --from=builder /out/aonohako /usr/local/bin/aonohako
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
