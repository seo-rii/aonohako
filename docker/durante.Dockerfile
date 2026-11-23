# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
# Explicit opt-in overlay. Supply a reviewed immutable execution-image digest.
ARG RUNTIME_IMAGE
ARG DURANTE_VERSION=v7
ARG GO_IMAGE=golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0
ARG BUILD_IMAGE=debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2

# Lightweight CI compiler image. This stage does not rebuild the pinned SDK;
# collection mounts the same verified SDK/client used by the sandbox tests.
FROM ${BUILD_IMAGE} AS reference-tools
RUN apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates python3 pypy3 rustc && rm -rf /var/lib/apt/lists/*

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS durante-go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG DURANTE_VERSION
ENV GOFLAGS="-tags=durante_${DURANTE_VERSION}"
RUN case "$DURANTE_VERSION" in v7|v8|v9|v10|v11) ;; *) echo 'unsupported Durante version' >&2; exit 1 ;; esac
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako ./cmd/server

FROM durante-go AS durante-go-test
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /out/durante-execute.test ./internal/execute

FROM ${BUILD_IMAGE} AS durante-build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake patch python3 ca-certificates && rm -rf /var/lib/apt/lists/*
COPY third_party/durante/ /vendor/durante/
COPY third_party/durante-v8/ /vendor/durante-v8/
COPY third_party/durante-v9/ /vendor/durante-v9/
COPY third_party/durante-v10/ /vendor/durante-v10/
COPY third_party/durante-v11/ /vendor/durante-v11/
COPY scripts/verify_durante_vendor.py /verify_durante_vendor.py
COPY --chmod=0755 scripts/install_durante_runtime.sh /install_durante_runtime.sh
COPY --chmod=0755 scripts/build_durante_client.sh /build_durante_client.sh
ARG DURANTE_VERSION
RUN python3 /verify_durante_vendor.py --root /vendor/durante --version "$DURANTE_VERSION" --output /durante
RUN python3 /durante/toolkit/fetch_sdk.py
RUN /build_durante_client.sh /durante /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1 /build
RUN /install_durante_runtime.sh /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1 /build/libdurante.so /durante /out/durante
COPY internal/execute/testdata/durante/probe.c /probe.c
RUN gcc -O2 -Wall -Wextra -Werror -static /probe.c -o /durante-probe
COPY internal/execute/testdata/durante/interactive.c /interactive.c
COPY internal/execute/testdata/durante/communication.c /communication.c
RUN gcc -O2 -Wall -Wextra -Werror -static /interactive.c -o /durante-interactive && gcc -O2 -Wall -Wextra -Werror -static /communication.c -o /durante-communication
COPY internal/execute/testdata/durante/reference.cpp /reference.cpp
RUN g++ -std=c++20 -O2 -Wall -Wextra -Werror -static /reference.cpp -o /durante-reference
COPY internal/execute/testdata/durante/rep_breakdown.S /rep_breakdown.S
RUN gcc -nostdlib -static -no-pie /rep_breakdown.S -o /durante-rep-breakdown

FROM ${RUNTIME_IMAGE} AS durante-runtime
COPY --from=durante-go /out/aonohako /usr/local/bin/aonohako
COPY --from=durante-build /out/durante/ /opt/durante/

FROM durante-runtime AS durante-validation
COPY --from=durante-go-test /out/durante-execute.test /opt/durante/durante-execute.test
COPY --from=durante-build /durante-probe /opt/durante/probe
COPY --from=durante-build /durante-interactive /opt/durante/interactive
COPY --from=durante-build /durante-communication /opt/durante/communication
COPY --from=durante-build /durante-reference /opt/durante/reference
COPY --from=durante-build /durante-rep-breakdown /opt/durante/rep-breakdown
ENV AONOHAKO_TEST_DURANTE=1 AONOHAKO_ENFORCE_SANDBOX_TESTS=1 AONOHAKO_DURANTE_TEST_BINARY=/opt/durante/probe AONOHAKO_TEST_DURANTE_INTERACTIVE=/opt/durante/interactive AONOHAKO_TEST_DURANTE_COMMUNICATION=/opt/durante/communication AONOHAKO_TEST_DURANTE_REFERENCE=/opt/durante/reference AONOHAKO_TEST_DURANTE_REP_BREAKDOWN=/opt/durante/rep-breakdown
ENTRYPOINT ["/opt/durante/durante-execute.test"]
CMD ["-test.v", "-test.run", "^(TestDurante(Sandbox|ResourceEnvelope|REPBreakdown|MemoryOperandBytes|TimeEstimate|Judge|Interactive|Communication)Integration|TestRunCPUFeaturePolicyInRootSandbox)$", "-test.timeout", "240s"]

FROM durante-runtime AS runtime
