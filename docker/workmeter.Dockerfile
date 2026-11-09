# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
# Explicit opt-in overlay. Supply a reviewed immutable execution-image digest.
ARG RUNTIME_IMAGE
ARG GO_IMAGE=golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af
ARG BUILD_IMAGE=debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS workmeter-go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako ./cmd/server

FROM workmeter-go AS workmeter-go-test
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /out/workmeter-execute.test ./internal/execute

FROM ${BUILD_IMAGE} AS workmeter-build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake python3 ca-certificates && rm -rf /var/lib/apt/lists/*
COPY third_party/workmeter/ /workmeter/
COPY scripts/verify_workmeter_vendor.py /verify_workmeter_vendor.py
RUN python3 /verify_workmeter_vendor.py --root /workmeter
RUN python3 /workmeter/toolkit/fetch_sdk.py
RUN cmake -S /workmeter/client -B /build -DCMAKE_BUILD_TYPE=Release -DDynamoRIO_DIR=/workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/cmake && cmake --build /build --target workmeter -j2
COPY internal/execute/testdata/workmeter/probe.c /probe.c
RUN gcc -O2 -Wall -Wextra -Werror -static /probe.c -o /workmeter-probe

FROM ${RUNTIME_IMAGE} AS workmeter-runtime
COPY --from=workmeter-go /out/aonohako /usr/local/bin/aonohako
COPY --from=workmeter-build /workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/bin64/ /opt/workmeter/dynamorio/bin64/
COPY --from=workmeter-build /workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib64/release/libdynamorio.so /opt/workmeter/dynamorio/lib64/release/libdynamorio.so
COPY --from=workmeter-build /workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib64/debug/libdynamorio.so /opt/workmeter/dynamorio/lib64/debug/libdynamorio.so
COPY --from=workmeter-build /workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib32/release/libdynamorio.so /opt/workmeter/dynamorio/lib32/release/libdynamorio.so
COPY --from=workmeter-build /workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib32/debug/libdynamorio.so /opt/workmeter/dynamorio/lib32/debug/libdynamorio.so
COPY --from=workmeter-build /workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/ext/lib64/release/libdrmgr.so /workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/ext/lib64/release/libdrreg.so /opt/workmeter/dynamorio/ext/lib64/release/
COPY --from=workmeter-build /build/libworkmeter.so /opt/workmeter/lib/libworkmeter.so
COPY --from=workmeter-build /workmeter/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/License.txt /opt/workmeter/DynamoRIO-LICENSE.txt
COPY third_party/workmeter/LICENSE third_party/workmeter/UPSTREAM.json /opt/workmeter/

FROM workmeter-runtime AS workmeter-validation
COPY --from=workmeter-go-test /out/workmeter-execute.test /opt/workmeter/workmeter-execute.test
COPY --from=workmeter-build /workmeter-probe /opt/workmeter/probe
ENV AONOHAKO_TEST_WORKMETER=1 AONOHAKO_ENFORCE_SANDBOX_TESTS=1 AONOHAKO_WORKMETER_TEST_BINARY=/opt/workmeter/probe
ENTRYPOINT ["/opt/workmeter/workmeter-execute.test"]
CMD ["-test.v", "-test.run", "^TestWorkmeter(Sandbox|ResourceEnvelope)Integration$", "-test.timeout", "90s"]

FROM workmeter-runtime AS runtime
