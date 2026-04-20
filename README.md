# aonohako

`aonohako` is a Go service for compiling and executing judge submissions over
SSE. It is designed for online judge pipelines that want a small control plane
binary, configurable runtime images, and testable build metadata.

## What is in this repository

- `POST /compile`, `POST /execute`, and `GET /healthz`
- queue-controlled SSE responses with `progress`, `log`, `image`, `error`, and
  final `result` events
- a `box` workspace layout that keeps submitted files immutable while allowing
  new files to be created in the same working directory
- symlink-safe output capture for file outputs and sidecar artifacts
- `runtime-images.yml` as the source of truth for runtime image groups
- Docker build tooling that can emit production multi-language images and
  single-language CI smoke images from the same YAML catalog
- GitHub Actions CI that runs Go tests, repository policy checks, and language
  smoke builds in parallel

## Runtime image model

The runtime catalog lives in [`runtime-images.yml`](runtime-images.yml).

- Production mode builds grouped images such as `type-a` (`plain`, `python`)
  and `type-b` (`java`), plus dedicated profiles where a toolchain needs its
  own base image such as `swift` or `julia`.
- CI mode expands the same catalog into one image per language so that each
  smoke job validates a single toolchain in isolation.
- The current catalog covers native binaries, Python with `numpy`, Java,
  JavaScript/TypeScript, Ruby, PHP, Lua, Perl, Elixir, Haskell, OCaml, SQLite,
  Go, Rust, Kotlin, C#, Julia, Swift, and UHMLANG. C/C++ submitters compile
  into binaries and should target the `plain` runtime image rather than
  dedicated C/C++ runtime images. Add new languages by extending the YAML file
  instead of editing shell loops or workflow matrices.

Inspect the generated matrix:

```bash
go run ./cmd/runtime-matrix -mode production
go run ./cmd/runtime-matrix -mode ci
```

Dry-run image builds:

```bash
./scripts/build_runtime_images.sh -mode production -dry-run -tag-prefix ghcr.io/seo-rii/aonohako
./scripts/build_runtime_images.sh -mode ci -dry-run -tag-prefix aonohako-ci
```

## Local development

Run the server:

```bash
go run ./cmd/server
```

Run the test suite:

```bash
go test ./...
```

Repository policy check:

```bash
./scripts/check_repo_policy.sh
```

## Configuration

- `PORT` defaults to `8080`
- `AONOHAKO_MAX_ACTIVE_RUNS` defaults to `1` on Cloud Run, otherwise `max(1, cpu-2)`
- `AONOHAKO_MAX_PENDING_QUEUE` defaults to `0` for unlimited queue depth
- `AONOHAKO_HEARTBEAT_INTERVAL_SEC` defaults to `10`
- `AONOHAKO_WORK_ROOT` points temp compile/run directories at a dedicated work root

Per-request execution limits are part of the `/execute` payload:

- `limits.time_ms`
- `limits.memory_mb`
- `limits.output_bytes`
  Defaults to `64 KiB` when omitted and is capped internally at `8 MiB`

## Security notes

This repository does not ship cloud-vendor deployment credentials or `gcloud`
workflow dependencies. The CI policy script fails if common secret-like or
cloud CLI markers are checked in.

The local execution path now enforces these invariants:

- the process working directory is `box/`
- submitted files are materialized with immutable permissions (`0444` or
  `0555`)
- the `box/` directory is writable so submissions can create new files beside
  their own sources or binaries
- captured outputs reject symlinks to avoid read-through escapes

The runtime sandbox uses helper-process hardening rather than child cgroups or
mount-based filesystem isolation. It applies `setrlimit`, `PR_SET_NO_NEW_PRIVS`,
seccomp, fd cleanup, immutable submitted files, a writable per-run workspace,
and process-group cleanup.

Security posture depends on where it runs:

- Cloud Run deployment mode is the supported security target. This is where the
  runtime image permissions, service account scoping, and deployment-level
  egress controls form the expected boundary.
- Local non-root execution is development mode. It still applies process-level
  limits and seccomp, but it does not promise the same filesystem isolation as a
  root-owned runtime container on Cloud Run.

For Cloud Run deployments, use this baseline:

- second-generation execution environment
- service concurrency `1`
- a bounded in-memory volume mounted at a path such as `/work`, with
  `AONOHAKO_WORK_ROOT=/work`
- Direct VPC egress with `all-traffic` routing and firewall-denied outbound
  traffic except for explicitly allowed targets
- a dedicated service account with no unnecessary IAM permissions and no baked
  secrets in the image

Cloud Run's own documentation states that volumes must be configured through
Cloud Run volume mounts and that arbitrary in-container mounting is not
supported, so `aonohako` does not depend on cgroup creation or mount-based
filesystem isolation when running there.

## License

MIT. See [LICENSE](LICENSE).
