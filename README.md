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
  and `type-b` (`java`).
- CI mode expands the same catalog into one image per language so that each
  smoke job validates a single toolchain in isolation.
- The current catalog intentionally stays small: `plain` for native binaries,
  Python with `numpy`, and Java 17. C/C++ submitters compile into binaries and
  should target the `plain` runtime image rather than dedicated C/C++ runtime
  images. Add new languages by extending the YAML file instead of editing shell
  loops or workflow matrices.

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
- `AONOHAKO_MAX_ACTIVE_RUNS` defaults to `max(1, cpu-2)`
- `AONOHAKO_MAX_PENDING_QUEUE` defaults to `0` for unlimited queue depth
- `AONOHAKO_HEARTBEAT_INTERVAL_SEC` defaults to `10`
- `AONOHAKO_UNSHARE_ENABLED` controls `unshare` usage in the local execution
  path

Legacy `GO_*` environment variables are still accepted as fallbacks for
backward compatibility.

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

For stronger isolation, pair `aonohako` with a dedicated runner environment
that supports mount namespaces, pid limits, and cgroup enforcement.

## License

MIT. See [LICENSE](LICENSE).
