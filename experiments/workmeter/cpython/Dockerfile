ARG BASE=asia.gcr.io/hancomac/aonohako-jungol@sha256:cb760e68ffeeee69d09f9f931b7bc462dc5b81b2cc0b787ab9bf8af674dcb1d0
FROM ${BASE} AS builder
USER root
RUN rm -f /etc/apt/sources.list.d/*.sources /etc/apt/sources.list.d/*.list && printf 'deb http://deb.debian.org/debian trixie main\ndeb http://security.debian.org/debian-security trixie-security main\n' > /etc/apt/sources.list && apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends clang-19 llvm-19-dev make curl ca-certificates xz-utils pkg-config libssl-dev zlib1g-dev libbz2-dev libreadline-dev libsqlite3-dev libffi-dev liblzma-dev libncurses-dev libgdbm-dev libexpat1-dev uuid-dev libzstd-dev
COPY . /src/
RUN bash /src/build.sh
FROM ${BASE}
USER root
COPY --from=builder /opt/interpreter-meter/cpython /opt/interpreter-meter/cpython
RUN python3 /opt/interpreter-meter/cpython/provenance/hashseed_smoke.py && python3 /opt/interpreter-meter/cpython/provenance/smoke.py
LABEL org.opencontainers.image.description="Experimental CPython IR work meter; diagnostic use only"
