# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.22-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako ./cmd/server

FROM debian:bookworm-slim AS base
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
    openjdk-17-jdk-headless \
    && rm -rf /var/lib/apt/lists/*

FROM mcr.microsoft.com/dotnet/sdk:8.0 AS compile-dotnet
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

FROM python:3.11-slim-bookworm AS execute-python
RUN apt-get update && apt-get install -y --no-install-recommends \
    tini pypy3 openjdk-17-jre-headless \
    ca-certificates curl bzip2 \
    && rm -rf /var/lib/apt/lists/*
RUN python -m pip install --no-cache-dir --upgrade pip && \
    python -m pip install --no-cache-dir \
      pandas numpy seaborn matplotlib pillow six qiskit pyparsing pylatexenc torch torchvision jax 'jax[cpu]' jungol-robot
COPY --from=builder /out/aonohako /usr/local/bin/aonohako
ENV PATH=/usr/local/bin:/usr/bin:/bin LANG=C.UTF-8 LC_ALL=C.UTF-8 PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
ENTRYPOINT ["/usr/bin/tini","--"]
CMD ["aonohako"]

FROM base AS execute-jvm
RUN apt-get update && apt-get install -y --no-install-recommends \
    openjdk-17-jre-headless \
    && rm -rf /var/lib/apt/lists/*

FROM base AS execute-node
RUN apt-get update && apt-get install -y --no-install-recommends \
    nodejs npm \
    && rm -rf /var/lib/apt/lists/*

FROM base AS execute-script
RUN apt-get update && apt-get install -y --no-install-recommends \
    ruby php lua5.4 perl \
    && rm -rf /var/lib/apt/lists/*
