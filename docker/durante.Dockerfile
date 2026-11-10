# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
# Explicit opt-in overlay. Supply a reviewed immutable execution-image digest.
ARG RUNTIME_IMAGE
ARG GO_IMAGE=golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af
ARG BUILD_IMAGE=debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS durante-go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako ./cmd/server

FROM durante-go AS durante-go-test
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /out/durante-execute.test ./internal/execute

FROM ${BUILD_IMAGE} AS durante-build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake python3 ca-certificates && rm -rf /var/lib/apt/lists/*
COPY third_party/durante/ /durante/
COPY scripts/verify_durante_vendor.py /verify_durante_vendor.py
RUN python3 /verify_durante_vendor.py --root /durante
RUN python3 /durante/toolkit/fetch_sdk.py
RUN cmake -S /durante/client -B /build -DCMAKE_BUILD_TYPE=Release -DDynamoRIO_DIR=/durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/cmake && cmake --build /build --target durante -j2
COPY internal/execute/testdata/durante/probe.c /probe.c
RUN gcc -O2 -Wall -Wextra -Werror -static /probe.c -o /durante-probe

FROM ${RUNTIME_IMAGE} AS durante-runtime
COPY --from=durante-go /out/aonohako /usr/local/bin/aonohako
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/bin64/ /opt/durante/dynamorio/bin64/
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib64/release/libdynamorio.so /opt/durante/dynamorio/lib64/release/libdynamorio.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib64/debug/libdynamorio.so /opt/durante/dynamorio/lib64/debug/libdynamorio.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib32/release/libdynamorio.so /opt/durante/dynamorio/lib32/release/libdynamorio.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib32/debug/libdynamorio.so /opt/durante/dynamorio/lib32/debug/libdynamorio.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/ext/lib64/release/libdrmgr.so /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/ext/lib64/release/libdrreg.so /opt/durante/dynamorio/ext/lib64/release/
COPY --from=durante-build /build/libdurante.so /opt/durante/lib/libdurante.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/License.txt /opt/durante/DynamoRIO-LICENSE.txt
COPY third_party/durante/LICENSE third_party/durante/UPSTREAM.json /opt/durante/

FROM durante-runtime AS durante-validation
COPY --from=durante-go-test /out/durante-execute.test /opt/durante/durante-execute.test
COPY --from=durante-build /durante-probe /opt/durante/probe
ENV AONOHAKO_TEST_DURANTE=1 AONOHAKO_ENFORCE_SANDBOX_TESTS=1 AONOHAKO_DURANTE_TEST_BINARY=/opt/durante/probe
ENTRYPOINT ["/opt/durante/durante-execute.test"]
CMD ["-test.v", "-test.run", "^TestDurante(Sandbox|ResourceEnvelope)Integration$", "-test.timeout", "90s"]

FROM durante-runtime AS runtime
