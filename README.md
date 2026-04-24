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
- GitHub Actions CI that runs Go tests, repository policy checks, sandbox
  regressions, per-language smoke builds in parallel, and an explicit
  `plain`+`python`+`java` mixin smoke job, while publishing one consolidated
  toolchain summary across production runtime profiles

## Runtime image model

The runtime catalog lives in [`runtime-images.yml`](runtime-images.yml).

- Production mode builds grouped images such as `type-a` (`plain`, `pypy`,
  `aheui`, `racket`, `bf`, `whitespace`, `wasm`, and other lighter scripting runtimes),
  `type-b` (`clojure`, `java`, `javascript`, `scala`, `typescript`), `type-c`
  (`ada`, `asm`, `d`, `fortran`, `go`, `nasm`, `nim`, `pascal`, `rust`, `zig`),
  `type-e` (`csharp`, `fsharp`), and the mixin validation profile `type-i` (`plain`,
  `python`, `java`), plus dedicated profiles where a toolchain needs its own
  base image or install path such as `python` judge libraries (`type-l`),
  `swift`, `julia`, `coq`, or `dart`.
- CI mode expands the same catalog into one image per language so that each
  smoke job validates a single toolchain in isolation. A separate CI job builds
  the production profiles in parallel, runs
  [`scripts/report_toolchain_versions.sh`](scripts/report_toolchain_versions.sh)
  once per profile, and uploads both the profile summary fragment and a
  `docker save` archive for that image as artifacts. A final CI job downloads
  those artifacts, publishes one consolidated GitHub Actions summary, and
  re-uploads the collected summaries plus image archives as a single bundle.
- The current catalog covers native binaries, Python plus bundled judge
  libraries (`numpy`, `pandas`, `seaborn`, `matplotlib`, `Pillow`, `qiskit`,
  `torch`, `torchvision`, `jax[cpu]`, and related dependencies), plus vendored
  `jungol_robot` and `robot_judge` helpers, PyPy, Java, Groovy, Scala,
  Clojure, JavaScript/TypeScript, Ruby, PHP, Lua, Perl, Elixir, Haskell,
  OCaml, SQLite, Go, Rust, Zig, Nim, Pascal, Ada, GNU assembly, NASM, Kotlin,
  C#, F#, Julia, Swift, R, Racket, Erlang, Prolog, Brainfuck, Whitespace,
  WASM, Coq, Aheui, Dart, and UHMLANG. C/C++ and assembly submitters compile
  into binaries and should target the `plain` runtime image rather than
  dedicated native runtime images. Add new languages by extending the YAML file
  instead of editing shell loops or workflow matrices.
- Debian-based production profiles track `debian:trixie-slim`, which raises the
  default Python, PyPy, and GCC toolchain versions for both production and
  single-language CI runtime images.
- Python judge libraries in the runtime catalog are pinned to exact versions so
  runtime rebuilds stay reproducible across CI and production.

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

For non-root local development, keep `/compile` local and forward `/execute` to
a hardened runner:

```bash
AONOHAKO_DEPLOYMENT_TARGET=dev \
AONOHAKO_EXECUTION_TRANSPORT=remote \
AONOHAKO_SANDBOX_BACKEND=none \
AONOHAKO_REMOTE_RUNNER_URL=https://runner.internal \
go run ./cmd/server
```

If you want the local root-backed helper sandbox, run it explicitly with a
dedicated work root:

```bash
sudo env \
  AONOHAKO_DEPLOYMENT_TARGET=selfhosted \
  AONOHAKO_EXECUTION_TRANSPORT=embedded \
  AONOHAKO_SANDBOX_BACKEND=helper \
  AONOHAKO_API_BEARER_TOKEN=replace-me \
  AONOHAKO_WORK_ROOT=/work \
  AONOHAKO_MAX_ACTIVE_RUNS=1 \
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

Self-hosted runner hosts can also check future cgroup backend prerequisites:

```sh
aonohako-selftest cgroup-preflight
```

## Configuration

- `PORT` defaults to `8080`
- `AONOHAKO_DEPLOYMENT_TARGET` selects where the server is meant to run:
  `cloudrun`, `selfhosted`, or `dev` (default)
- `AONOHAKO_EXECUTION_TRANSPORT` selects how `/compile` and `/execute` are
  handled:
  `embedded` (default) or `remote`
- `AONOHAKO_SANDBOX_BACKEND` selects the local sandbox implementation:
  `helper` or `none`. `container` is a reserved enum value for a future
  backend and is rejected by startup validation today.
- These axes map to explicit security contracts in code:
  `embedded-helper-process-hardening`, `remote-control-plane`, and reserved
  `reserved-container-isolation`. The helper contract is process hardening,
  not per-run cgroup or mount-namespace isolation.
- `AONOHAKO_EXECUTION_MODE` remains as a compatibility shorthand:
  `cloudrun` → `cloudrun + embedded + helper`
  `local-root` → `selfhosted + embedded + helper`
  `local-dev` → `dev + embedded + helper` (compatibility only; it is not the
  non-root development path)
- `AONOHAKO_MAX_ACTIVE_RUNS` defaults to `1` for `embedded + helper`, stays `1`
  for `cloudrun`, and otherwise defaults to `max(1, cpu-2)`. The
  `embedded + helper` backend rejects values other than `1`.
- `AONOHAKO_MAX_PENDING_QUEUE` defaults to `16`. Set it explicitly to `0` only
  for development cases that intentionally need an unlimited queue.
- `AONOHAKO_HEARTBEAT_INTERVAL_SEC` defaults to `10`
- Numeric environment variables are strict: malformed, negative, or zero values
  where a positive integer is required fail startup instead of falling back.
- `AONOHAKO_INBOUND_AUTH` controls inbound `/compile` and `/execute`
  authentication. It defaults to `none` for `dev` and `bearer` for `cloudrun`
  or `selfhosted`. Supported values are `none`, `bearer`, and `platform`.
- `AONOHAKO_API_BEARER_TOKEN` is required when
  `AONOHAKO_INBOUND_AUTH=bearer`.
- `AONOHAKO_INBOUND_AUTH=platform` documents that Cloud Run IAM, an API
  gateway, mTLS, private ingress, or another platform layer authenticates
  inbound calls before they reach this process.
- `AONOHAKO_WORK_ROOT` points compile/run directories at a dedicated work root
  and is required for `cloudrun`, and for `selfhosted + embedded + helper`
- `AONOHAKO_REMOTE_RUNNER_URL` points `remote` transport at another
  `aonohako` runner service and must be an absolute `http(s)` URL without
  embedded credentials, query strings, or fragments
- `AONOHAKO_REMOTE_RUNNER_AUTH` can be `none`, `bearer`, or
  `cloudrun-idtoken`
- `AONOHAKO_REMOTE_RUNNER_TOKEN` provides the bearer token when
  `AONOHAKO_REMOTE_RUNNER_AUTH=bearer`
- `AONOHAKO_REMOTE_RUNNER_AUDIENCE` overrides the ID-token audience for
  `cloudrun-idtoken` auth; it defaults to `AONOHAKO_REMOTE_RUNNER_URL`

Per-request execution limits are part of the `/execute` payload:

- `limits.time_ms`
- `limits.memory_mb`
- `limits.output_bytes`
  Defaults to `64 KiB` when omitted and is capped internally at `8 MiB`
- `stdin` and `expected_stdout`
  Each field is capped at `16 MiB` before a request enters the shared queue.
- `enable_network`
  Cloud Run embedded-helper runners reject `true`. Self-hosted embedded-helper
  runners allow outbound `AF_INET`/`AF_INET6` client sockets only; listener
  syscalls and host `AF_UNIX` sockets stay blocked. Control-plane instances can
  forward networked workloads to those runners with `remote` transport.

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

- `cloudrun + embedded + helper` is the supported production security target.
  Startup fails closed unless `AONOHAKO_WORK_ROOT` is configured, writable,
  not group/world writable, owned by the server UID, the process is running as
  root, and the helper queue is single-slot.
- `cloudrun + remote + none` is the supported Cloud Run control-plane shape
  when `/compile` and `/execute` should be forwarded to a separate hardened
  runner. It still requires a bounded `AONOHAKO_WORK_ROOT` for local
  compile/workspace handling.
- `selfhosted + embedded + helper` applies the same dedicated work-root
  contract for local root-backed containers and VMs, including
  `AONOHAKO_MAX_ACTIVE_RUNS=1` so concurrent runs do not share the same sandbox
  UID.
- `dev + remote + none` is the non-root development path. The local server
  forwards `/compile` and `/execute` to a remote hardened runner instead of
  building or running untrusted inputs locally.
- `dev + embedded + helper` remains available through the compatibility mode, but
  `/execute` still requires root because the local helper sandbox is root-backed.
- for higher-throughput self-hosted deployments, keep helper-backed runners at
  one active execution each and scale a remote runner pool horizontally instead
  of increasing helper slots inside one process. See
  [docs/selfhosted.md](docs/selfhosted.md).

For Cloud Run deployments, use this baseline:

- `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`
- `AONOHAKO_EXECUTION_TRANSPORT=embedded`
- `AONOHAKO_SANDBOX_BACKEND=helper`
- `AONOHAKO_API_BEARER_TOKEN` set to a strong secret, or
  `AONOHAKO_INBOUND_AUTH=platform` only when an upstream layer enforces
  inbound authentication
- second-generation execution environment
- service concurrency `1`
- a bounded in-memory volume mounted at a path such as `/work`, with
  `AONOHAKO_WORK_ROOT=/work`
- Direct VPC egress with `all-traffic` routing and firewall-denied outbound
  traffic except for explicitly allowed targets
- a dedicated service account with no unnecessary IAM permissions and no baked
  secrets in the image

For a Cloud Run API/control-plane service that forwards `/compile` and
`/execute`, use
`AONOHAKO_EXECUTION_TRANSPORT=remote`,
`AONOHAKO_SANDBOX_BACKEND=none`, the same bounded `AONOHAKO_WORK_ROOT`, and a
private `AONOHAKO_REMOTE_RUNNER_URL`.

Cloud Run's own documentation states that volumes must be configured through
Cloud Run volume mounts and that arbitrary in-container mounting is not
supported, so `aonohako` does not depend on cgroup creation or mount-based
filesystem isolation when running there.

For non-Cloud-Run control-plane deployments that should still execute safely,
use this baseline:

- `AONOHAKO_DEPLOYMENT_TARGET=dev`
- `AONOHAKO_EXECUTION_TRANSPORT=remote`
- `AONOHAKO_SANDBOX_BACKEND=none`
- `AONOHAKO_REMOTE_RUNNER_URL=https://<dedicated-runner>`
- optional `AONOHAKO_REMOTE_RUNNER_AUTH=bearer` with
  `AONOHAKO_REMOTE_RUNNER_TOKEN=...`, or
  `AONOHAKO_REMOTE_RUNNER_AUTH=cloudrun-idtoken` when calling another Cloud Run
  service

## License

MIT. See [LICENSE](LICENSE).
