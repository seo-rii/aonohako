# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
# Explicit opt-in overlay. Supply a reviewed immutable execution-image digest.
ARG RUNTIME_IMAGE
ARG GO_IMAGE=golang:1.26.11-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0
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
COPY internal/execute/testdata/durante/interactive.c /interactive.c
COPY internal/execute/testdata/durante/communication.c /communication.c
RUN gcc -O2 -Wall -Wextra -Werror -static /interactive.c -o /durante-interactive && gcc -O2 -Wall -Wextra -Werror -static /communication.c -o /durante-communication

FROM ${RUNTIME_IMAGE} AS durante-runtime
COPY --from=durante-go /out/aonohako /usr/local/bin/aonohako
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/bin64/ /opt/durante/dynamorio/bin64/
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib64/release/libdynamorio.so /opt/durante/dynamorio/lib64/release/libdynamorio.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib64/debug/libdynamorio.so /opt/durante/dynamorio/lib64/debug/libdynamorio.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib32/release/libdynamorio.so /opt/durante/dynamorio/lib32/release/libdynamorio.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/lib32/debug/libdynamorio.so /opt/durante/dynamorio/lib32/debug/libdynamorio.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/.durante-runtime.json /opt/durante/dynamorio/.durante-runtime.json
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/ext/lib64/release/libdrmgr.so /opt/durante/dynamorio/ext/lib64/release/
COPY --from=durante-build /build/libdurante.so /opt/durante/lib/libdurante.so
COPY --from=durante-build /durante/toolkit/sdk/DynamoRIO-Linux-11.3.0-1/License.txt /opt/durante/DynamoRIO-LICENSE.txt
COPY third_party/durante/LICENSE third_party/durante/UPSTREAM.json /opt/durante/

FROM durante-runtime AS durante-validation
COPY --from=durante-go-test /out/durante-execute.test /opt/durante/durante-execute.test
COPY --from=durante-build /durante-probe /opt/durante/probe
COPY --from=durante-build /durante-interactive /opt/durante/interactive
COPY --from=durante-build /durante-communication /opt/durante/communication
ENV AONOHAKO_TEST_DURANTE=1 AONOHAKO_ENFORCE_SANDBOX_TESTS=1 AONOHAKO_DURANTE_TEST_BINARY=/opt/durante/probe AONOHAKO_TEST_DURANTE_INTERACTIVE=/opt/durante/interactive AONOHAKO_TEST_DURANTE_COMMUNICATION=/opt/durante/communication
ENTRYPOINT ["/opt/durante/durante-execute.test"]
CMD ["-test.v", "-test.run", "^TestDurante(Sandbox|ResourceEnvelope|Interactive|Communication)Integration$", "-test.timeout", "240s"]

FROM durante-runtime AS runtime
