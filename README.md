# aonohako

`aonohako` is a Go service for compiling and executing judge submissions over
SSE. It is designed for online judge pipelines that want a small control plane
binary, configurable runtime images, and testable build metadata.

## What is in this repository

- `POST /compile`, `POST /execute`, `GET /livez`, and `GET /readyz`
- queue-controlled SSE responses with `progress`, `log`, `image`, `error`, and
  final `result` events
- a `box` workspace layout that keeps submitted files immutable while allowing
  new files to be created in the same working directory
- symlink-safe output capture for file outputs and sidecar artifacts
- SPJ and interactive IO judging support for problems that need custom verdict
  logic or bidirectional contestant/interactor communication
- `communication-v1` execution on explicitly enabled Cloud Run embedded helpers
  and dedicated self-hosted cgroup runners: one private manager process
  coordinates 2–64 isolated launches of one shared, read-only participant binary
- `runtime-images.yml` as the source of truth for runtime image groups
- Docker build tooling that can emit production multi-language images and
  single-language CI smoke images from the same YAML catalog
- GitHub Actions CI that runs Go tests, repository policy checks, sandbox
  regressions, per-language smoke builds in parallel, and an explicit
  `plain`+`python`+`java` mixin smoke job, while publishing one consolidated
  toolchain summary across production runtime profiles

## Runtime image model

The runtime catalog lives in [`runtime-images.yml`](runtime-images.yml).
Reusable `shared_installs` blocks hold toolchains needed by several languages;
each referenced block is expanded once per generated image while direct
language-specific commands keep their declared order.

- Production mode builds grouped images such as `type-i` for common C/C++,
  Python, PyPy, and Java judge workloads, `type-a` for lighter scripting and
  esolang runtimes, `type-b` for JVM/Node/Web tooling, `type-c` for native and
  systems toolchains, `type-d` for Kotlin, `type-e` for .NET languages, and
  dedicated profiles for larger or isolated toolchains such as Julia, Swift,
  proof assistants, Dart, DuckDB/GDL/Octave, HDL simulators, CUDA Ocelot, Dafny,
  Isabelle, and Lean. The full production profile table is maintained in
  [docs/architecture.md](docs/architecture.md#runtime-image-model).
- CI mode expands the same catalog into one image per language so that each
  smoke job validates a single toolchain in isolation. A separate CI job builds
  the production profiles in parallel, runs
  [`scripts/report_toolchain_versions.sh`](scripts/report_toolchain_versions.sh)
  once per profile, records both toolchain versions and language-specific
  compile options, and uploads the profile summary fragment plus SBOM/scan
  diagnostics. Docker archive export is currently skipped in CI to conserve
  runner storage; each profile records an archive diagnostic JSON instead. A
  final CI job downloads those artifacts, publishes one consolidated GitHub
  Actions summary, verifies the exact profile and language inventories from the
  production matrix, binds each summary/SBOM/scan report and its hashes to one
  immutable image ID, regenerates the aggregate summary for exact comparison,
  and uploads the exact sorted manifest of summaries, provenance, SBOM/scan
  evidence, and archives or skip diagnostics as a single bundle. Missing
  downloads, empty profile sets, failed probes, malformed evidence, and
  non-portable archive checksum paths fail the summary job.
- The current catalog covers native binaries, Python plus bundled judge
  libraries (`numpy`, `pandas`, `seaborn`, `matplotlib`, `Pillow`, `qiskit`,
  `torch`, `torchvision`, `jax[cpu]`, and related dependencies), optional
  custom Python packages supplied at image build time, PyPy, Java/Kotlin/JVM languages,
  Node/Deno/TypeScript/CoffeeScript/Elm/ReScript/PureScript, AssemblyScript/WASI, .NET languages and PowerShell, Ruby, PHP, Lua, Perl,
  Elixir/Erlang/Gleam, Haskell, Idris2, Standard ML, OCaml, SQLite/DuckDB, Go, Rust, Zig, Nim,
  Pascal, Delphi, Object Pascal, Ada, GNU assembly, NASM, Objective-C/C++, C3, Crystal, D, Hare, Vala,
  Mojo, MoonBit, Fennel, Factor, Chapel, ALGOL 68, Koka, Pony, Bash/POSIX shell, Zerolang, Odin, V, FreeBASIC/QBasic, Julia, Swift, R, Racket/Chibi Scheme/Chez Scheme, Mercury, Prolog,
  Lisp/PicoLisp/Smalltalk/GolfScript, APECode, Befunge, Brainfuck, Malbolge, LOLCODE, Whitespace, WASM, Coq/Rocq, Lean, Agda,
  TLA+, Why3, Isabelle, Aheui, Dart, GDL/Octave, HDL simulation, CUDA Ocelot,
  Carbon, VB6, Dafny, BQN/APL/J/UIUA/Janet, and UHMLANG. C/C++ and assembly
  submitters compile into binaries and should target the `plain` runtime image
  rather than dedicated native runtime images. Add new languages by extending
  the YAML file instead of editing shell loops or workflow matrices.
- Zerolang is pinned to the official v0.3.4 Linux x64 release. Compilation
  imports canonical `.0` text into a graph and then builds a native executable
  with the `release-fast` profile. Its stable source projection does not yet
  expose a stdin contract, so runtime smoke coverage currently uses fixed
  output while still exercising import, validation, compilation, and execution.
- AssemblyScript is pinned to 0.28.20 with the official WASI shim 0.1.0 and
  checksum-verified compiler, shim, Binaryen, and Long npm archives. A trusted
  wrapper resolves the shim from its immutable installation directory, emits a
  validated Wasm command artifact, and executes it with Wasmtime's bounded
  memory/table/instance policy without preopening the submission directory.
- Factor is pinned to the official 0.101 image and isolated in its own runtime
  profile because the image includes a JIT and FFI surface. Compilation parses
  source without running top-level forms. Both phases disable Factor's signal
  helper thread while permitting the VM's internal thread-directed GC signals;
  process creation, network sockets, and the remaining default seccomp
  restrictions stay denied.
- Chez Scheme is pinned to Debian trixie's `chezscheme 10.0.0+dfsg-5` and
  exposed through the distinct `CHEZ_SCHEME` contract. Compilation invokes an
  immutable reader helper that consumes every form through EOF without loading
  or evaluating submitted top-level code; execution uses Chez's script mode.
  Neither phase receives a process or socket sandbox exception.
- MoonBit is pinned to the official `moonc 0.10.9+6e6c44045` Linux x64
  toolchain and its matching core snapshot. Both archives are checksum-verified;
  submission builds synthesize a dependency-free module and use a single-job,
  frozen native release build so they never update or contact the package registry.
- Fennel is pinned to the checksum-verified official 1.6.1 standalone Linux
  x64 compiler. A trusted writer uses its embedded `compile-string` API with
  the compiler sandbox and no user rc file, keeps compiler diagnostics out of
  the artifact, validates both generated and guarded Lua with `luac5.4`, and
  atomically publishes the result. The generated guard removes process
  spawning, the debug library, preloaded modules, and native-module loaders
  before submission code runs in the shared Lua runtime pack.
- Chapel is pinned to the official 2.9.0 Debian 13 package in its own runtime
  pack. Submissions compile for one local locale with the packaged qthreads
  tasking runtime, and execution always fixes both locale and worker-thread
  counts to one.
- ALGOL 68 uses checksum-pinned Algol 68 Genie 3.13.3 source. Its core build is
  additionally patched for judge use: cwd and environment option readers,
  process-related prelude names, monitor command execution, and underlying
  `system`/`fork`/`execve` calls are all disabled. Both syntax checking and
  execution force interpreted `-O0` mode and ignore source pragmats.
- Koka is pinned to the checksum-verified official 3.2.3 Linux x64 bundle.
  Submission compilation disables automatic dependency installation, fixes
  GCC 16 to the portable x86-64 baseline, and normalizes a lone `Main.kk` to
  canonical `main.kk`. Generated ELF files with RPATH/RUNPATH or path-bearing
  dynamic dependencies are rejected before publication.
- Pony is pinned to the checksum-verified official 0.69.1 Ubuntu 24.04 x86-64
  bundle and installed as a minimal compiler, runtime, and standard-library
  payload. Compilation targets the generic CPU, execution fixes the scheduler
  ceiling with `--ponymaxthreads=1`, and physical memory remains cgroup/RSS
  bounded while Pony's reserved virtual arena is exempted from `RLIMIT_AS`.
- Shell submissions use Debian Bash 5.2.37-2+b9 or dash 0.5.12-12 as runtime
  variants of one `.sh` language family. Syntax checks and execution disable
  startup files, while the dedicated type-x pack permits bounded child
  processes for normal shell composition with a 64-task helper budget and an
  80-task per-run cgroup ceiling, but keeps networking disabled. The
  final image exposes only an explicit utility allowlist and makes hidden
  compilers, package/network tools, and their loader inputs unreadable.
- PowerShell is pinned to the checksum-verified official 7.6.5 Linux x64
  bundle in the shared type-e pack. Syntax checking uses the parser API without
  executing the submission, while execution disables profiles, telemetry, and
  update checks. Its home/config/data roots point at root-owned `/var/empty`,
  and its module path contains only immutable empty compatibility roots plus
  the bundled modules, never the writable workspace. Its CoreCLR startup
  receives a 512-file-descriptor ceiling and no `RLIMIT_AS`, plus the normal
  request-derived GC heap cap; process creation, networking, memfd/NUMA
  exceptions, shared dotnet state, and the dotnet file-size override stay off.
  Its pack replaces `/etc/passwd` with a root-owned 0444 empty NSS compatibility
  stub required by pwsh, exposing no identity records; `/etc/group` and other
  account metadata stay root-only.
- Carbon is pinned to an official experimental nightly. The image precompiles
  that toolchain's Core objects and native runtimes, then submission compilation
  emits an object, links a native executable against those trusted inputs, and
  runs it through the standard binary sandbox. With the pinned nightly, `Run`
  should return `i32` and explicitly return zero; a void `Run` can leave the
  last output byte count as the process exit status.
- Compile and execute environments set `ONLINE_JUDGE=1`. Languages with
  compiler-supported defines or build tags also receive an `ONLINE_JUDGE`
  compile flag where appropriate, such as C/C++/Objective-C, NASM, Rust, Go,
  Pascal, Nim, D, Dart, Verilog, Crystal, V, Odin, C3, Swift, .NET, Cython,
  Haxe, and FreeBASIC/QBasic. JVM-family runtime launchers also pass
  `-DONLINE_JUDGE=1`.
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

Custom Python packages can be copied into runtime images by passing a directory
as a named build context:

```bash
./scripts/build_runtime_images.sh \
  -mode production \
  -only type-i \
  -tag-prefix ghcr.io/seo-rii/aonohako \
  -python-packages-context /path/to/python/packages
```

The same path can be supplied with `AONOHAKO_PYTHON_PACKAGES_CONTEXT`.
When neither is supplied and the repository `python/` directory exists, it is
used by default. Contents are copied to `/usr/local/lib/aonohako/python`, which
is exported as `PYTHONPATH` in runtime images. The fixed bundled
`sitecustomize.py` is copied after that context, so a custom package context
cannot replace the trusted startup hook. The hook stays inactive unless an
execution requests image sidecar output.

`deployment-contract.json` publishes the installed-library capability and its
supported interpreter targets. Downstream deployment automation should require
that contract before enabling PyPy installed-library requests, so an older
runner ref fails before an image or control-plane revision is deployed.

An embedded helper advertises `python-library-pypy-installed-v1` from
`GET /capabilities` only when it globally accepts explicit installed mode and
the running image declares exact `python` and `pypy` language membership, both
library-isolation flags, and the reserved external-library group. A
problem-specific installed-mode mapping alone does not advertise a fleet-wide
capability, and remote control planes do not advertise a downstream worker's
capability. The live response attests runner policy and trusted build metadata;
CI and startup selftests verify the actual image permissions, while image build
smoke tests verify the package catalog separately.

## Local development

For non-root local development, forward both `/compile` and `/execute` to a
hardened runner:

```bash
AONOHAKO_DEPLOYMENT_TARGET=dev \
AONOHAKO_EXECUTION_TRANSPORT=remote \
AONOHAKO_SANDBOX_BACKEND=none \
AONOHAKO_REMOTE_RUNNER_URL=https://runner.internal \
go run ./cmd/server
```

Bare `go run ./cmd/server` uses the compatibility `local-dev` shape, which is
still an embedded helper sandbox and requires a root parent. If you want the
local root-backed helper sandbox, run it explicitly with a dedicated work root:

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

Validate the current deployment environment without starting the HTTP server:

```bash
aonohako-selftest deployment-contract
```

The deployment contract JSON includes the selected execution shape, whether the
named security contract is implemented, effective and missing local
capabilities, queue and stream limits, inbound/remote auth posture, cgroup
parent presence, whether `AONOHAKO_REQUIRE_WORK_ROOT_TMPFS` is active, and the
configured `AONOHAKO_WORK_ROOT_MAX_BYTES` and `AONOHAKO_WORK_ROOT_MAX_FILES`
values.

Repository deployment tooling can validate its configured ceilings against the
machine-readable [`deployment-contract.json`](deployment-contract.json)
manifest before creating a revision.

Checked deployment environment examples live under
[`docs/examples/`](docs/examples/): `cloudrun-runner.env`,
`cloudrun-control-plane.env`, `selfhosted-runner.env`, and
`dev-control-plane.env`.

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
  `reserved-container-isolation`. The helper contract is process hardening by
  default; self-hosted helpers can opt into per-run cgroup memory, pids, and
  one-vCPU CPU bandwidth limits. `aonohako-selftest deployment-contract` moves
  those cgroup-backed capabilities from missing to effective when
  `AONOHAKO_CGROUP_PARENT` is configured. Mount-namespace, per-run UID, masked
  `/proc`, and post-start `execve()` isolation remain unavailable in the helper
  backend.
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
- `AONOHAKO_MAX_ACTIVE_STREAMS` defaults to `64` and caps simultaneous
  `/compile` and `/execute` request streams before they can occupy more server
  resources. Set it explicitly to `0` only for development cases that
  intentionally need unlimited open streams.
- `AONOHAKO_MAX_ACTIVE_UPLOADS` defaults to `4` outside development and
  `AONOHAKO_MAX_PRINCIPAL_ACTIVE_UPLOADS` defaults to `2`. These slots are
  acquired before authentication reads a signed request body, JSON decoding,
  Base64 validation, or payload URL fetches. The slot is released only after
  the request enters the bounded run queue or terminates early. Both limits
  default to `0` in development; production targets reject `0`.
- `AONOHAKO_PLATFORM_BODY_HASH_CONCURRENCY` defaults to
  `min(4, AONOHAKO_MAX_ACTIVE_STREAMS)` when streams are bounded, otherwise
  `min(4, AONOHAKO_MAX_ACTIVE_RUNS)` and accepts values from `1` through `64`.
  It separately caps concurrent pre-auth body hashing for signed platform-auth
  requests, so stream concurrency can be higher than the number of simultaneous
  64 MiB body hash operations.
- `AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS` defaults to `0` for `dev` and `16`
  for `cloudrun` or `selfhosted`. It caps simultaneous request streams per
  authenticated or platform principal; `0` disables the per-principal cap.
- `AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE` defaults to `0` for `dev` and
  `60` for `cloudrun` or `selfhosted`. It caps accepted `/compile` and
  `/execute` requests per principal per fixed one-minute window. Set it to `0`
  on any deployment target to disable the per-process request-rate cap. This is
  intended for trusted Cloud Run or self-hosted runners whose concurrency and
  fleet capacity are bounded at the deployment layer.
- `AONOHAKO_HEARTBEAT_INTERVAL_SEC` defaults to `10`
- `AONOHAKO_BODY_READ_TIMEOUT_SEC` defaults to `30` and bounds how long the
  HTTP server will spend reading one `/compile` or `/execute` request body.
  This keeps authenticated slow uploads from holding handler goroutines
  indefinitely before SSE streaming begins.
- `AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC` defaults to `30` and bounds how long
  a remote `/compile` or `/execute` SSE response may stay silent before the
  control plane cancels it.
- `AONOHAKO_REMOTE_STRICT_PROTOCOL` controls whether remote responses must carry
  `X-Aonohako-Protocol-Version`. It defaults to `true` outside `dev` and
  `false` in `dev`, so production remote fleets fail closed on unversioned
  runner responses while local compatibility testing can still accept them.
- `AONOHAKO_ALLOW_REQUEST_NETWORK` controls whether `/execute` may honor
  client-supplied `enable_network=true`. It defaults to `true` only for `dev`
  and `false` for `cloudrun` or `selfhosted`; public runners should route
  network-enabled problems to an explicitly opted-in runner pool.
  Outside `dev`, enabling it also requires
  `AONOHAKO_NETWORK_EGRESS_ISOLATED=true`, which asserts that the selected
  embedded runner or downstream remote runner is already inside a
  deny-by-default network namespace/cgroup-BPF/nftables or equivalent egress
  boundary that blocks loopback, private, link-local, and metadata addresses.
  The assertion does not create that infrastructure.
- `AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE` controls whether `/compile` and
  `/execute` may honor request-supplied `runtime_profile`. It defaults to
  `true` only for `dev` and `false` for `cloudrun` or `selfhosted`; production
  control planes should map problems to policy-owned profiles and enable this
  only on the trusted runner/control-plane boundary that receives those
  sanitized requests.
- `AONOHAKO_PROBLEM_RUNTIME_PROFILES` may define a JSON object mapping
  request `problem_id` values to named `AONOHAKO_RUNTIME_TUNING_PROFILES`.
  The server applies the mapped profile before stream or queue acquisition, so
  public entry points can keep direct `runtime_profile` selection disabled.
  With `remote` transport, deploy the same profile definitions and
  `problem_id` mapping to the downstream runner whenever the control plane
  forwards policy-selected `runtime_profile` values.
- `AONOHAKO_DEFAULT_GO_MODULE_MODE` selects the default for Go compilation:
  `stdlib` (default) or `installed`. Installed mode uses only the exact modules
  and checksums committed under `go-modules/`; compilation remains offline.
- `AONOHAKO_ALLOW_REQUEST_GO_INSTALLED_MODULES` controls whether a request may
  elevate from the `stdlib` default with `go_module_mode=installed`. It defaults
  to `true` only for `dev` and `false` for `cloudrun` or `selfhosted`.
- `AONOHAKO_PROBLEM_GO_MODULE_MODES` may define a JSON object mapping
  `problem_id` values to `stdlib` or `installed`. A problem mapping wins over a
  direct request and conflicting values are rejected before stream admission.
  Remote control planes and runners must use the same policy or explicitly
  trust the forwarded, policy-selected mode.
- `AONOHAKO_DEFAULT_RUST_CRATE_MODE` selects the default for Rust compilation:
  `stdlib` (default) or `installed`. Installed mode uses only the exact crate
  graph and checksums committed under `rust-crates/`; compilation remains
  offline.
- `AONOHAKO_ALLOW_REQUEST_RUST_INSTALLED_CRATES` controls whether a request may
  elevate from the `stdlib` default with `rust_crate_mode=installed`. It
  defaults to `true` only for `dev` and `false` for `cloudrun` or `selfhosted`.
- `AONOHAKO_PROBLEM_RUST_CRATE_MODES` may define a JSON object mapping
  `problem_id` values to `stdlib` or `installed`. A problem mapping wins over a
  direct request and conflicting values are rejected before stream admission.
  Remote control planes and runners must use the same policy or explicitly
  trust the forwarded, policy-selected mode.
- `AONOHAKO_DEFAULT_PYTHON_LIBRARY_MODE` selects the request-wide default for
  CPython and PyPy imports: `stdlib` (default) or `installed`. `stdlib` keeps
  submitted sibling modules and the standard library available while hiding
  the selected interpreter's protected package roots and the shared trusted
  image package root. Both modes use isolated interpreter startup; `installed`
  explicitly adds global system packages, the trusted image package directory,
  and the fixed image-owned startup hook.
- `AONOHAKO_ALLOW_REQUEST_PYTHON_INSTALLED_LIBRARIES` controls whether a
  request may elevate from the `stdlib` default with
  `python_library_mode=installed`. It defaults to `true` only for `dev` and
  `false` for `cloudrun` or `selfhosted`. Outside a problem-owned mapping, a
  request may always choose the safer `stdlib` mode.
- `AONOHAKO_PROBLEM_PYTHON_LIBRARY_MODES` may define a JSON object mapping
  request `problem_id` values to `stdlib` or `installed`. A problem mapping
  wins over direct request policy and conflicting request values are rejected
  before stream or queue admission. With `remote` transport, deploy the same
  default and problem mappings to the downstream runner, or explicitly allow
  the trusted runner boundary to receive the control plane's selected
  `installed` mode.
- `AONOHAKO_TRUSTED_RUNNER_INGRESS` asserts that a root-backed embedded helper
  runner is reachable only through trusted/private ingress, Cloud Run IAM, mTLS,
  a gateway, or an equivalent control-plane boundary. It defaults to `true` for
  `dev` and remote control planes, but non-dev `embedded + helper` runners must
  set it explicitly to `true`.
- Numeric environment variables are strict: malformed, negative, or zero values
  where a positive integer is required fail startup instead of falling back.
- `AONOHAKO_INBOUND_AUTH` controls inbound `/compile` and `/execute`
  authentication. It defaults to `none` for `dev` and `bearer` for `cloudrun`
  or `selfhosted`. Supported values are `none` for `dev` only, `bearer`, and
  `platform`.
- `AONOHAKO_API_BEARER_TOKEN` is required when
  `AONOHAKO_INBOUND_AUTH=bearer`.
- `AONOHAKO_INBOUND_AUTH=platform` documents that Cloud Run IAM, an API
  gateway, mTLS, private ingress, or another platform layer authenticates
  inbound calls before they reach this process. The upstream layer must strip
  any client-supplied identity headers and rewrite `X-Aonohako-Principal`;
  forwarded identity headers such as `X-Forwarded-Email` are ignored by the
  application. Do not expose platform mode directly to the public internet.
- `AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET` is required for
  `AONOHAKO_INBOUND_AUTH=platform` outside `dev`. It makes platform mode verify
  `X-Aonohako-Principal-Signature: v4=<hex-hmac-sha256>` over
  `method + "\n" + request_uri + "\n" + principal + "\n" + timestamp + "\n" +
  nonce + "\n" + sha256_hex(body)` before accepting the request. `request_uri`
  includes the path and query string. The timestamp comes from
  `X-Aonohako-Principal-Timestamp` in RFC3339 format and must be within five
  minutes of the server clock. `X-Aonohako-Principal-Nonce` must be a fresh,
  cryptographically random 128-bit value encoded as 32 lowercase hex
  characters. A bounded replay cache rejects reuse by the same principal until
  the signature validity window expires; it fails closed when capacity is
  exhausted. Legacy replayable `v3=` and bodyless `v2=` signatures are rejected.
  Concurrent pre-auth body hashing first requires global and claimed-principal
  upload admission, and is additionally capped by
  `AONOHAKO_PLATFORM_BODY_HASH_CONCURRENCY`, so invalid signatures cannot force
  unbounded parallel 64 MiB body buffers.
- `AONOHAKO_TRUSTED_PLATFORM_HEADERS` and
  `AONOHAKO_PLATFORM_TRUSTED_PROXY_CIDRS` remain available only as optional
  defense-in-depth assertions for deployments that want source-CIDR checks in
  addition to signed platform principals; unsigned platform headers are not
  accepted outside `dev`.
- `AONOHAKO_WORK_ROOT` points compile/run directories at a dedicated work root
  and is required for `cloudrun`, and for `selfhosted + embedded + helper`
- `AONOHAKO_REQUIRE_WORK_ROOT_TMPFS` is a strict boolean and defaults to
  `false` for development and remote control planes. Production
  `embedded + helper` runners require it to be `true`; startup verifies through
  `/proc/self/mountinfo` that `AONOHAKO_WORK_ROOT` is the tmpfs mount point,
  rather than merely a directory somewhere on a shared tmpfs.
- `AONOHAKO_WORK_ROOT_MAX_BYTES`, when nonzero, verifies through `statfs` that
  the required work-root filesystem is bounded to that many bytes or less.
  Production helper runners require a positive value no greater than 1 GiB.
- `AONOHAKO_WORK_ROOT_MAX_FILES`, when nonzero, verifies through `statfs` that
  the required work-root filesystem exposes no more than that many inodes.
  Production helper runners require a positive value no greater than 1048576.
  The only exception is the dedicated Cloud Run communication runner, which
  permits up to 4194304 advertised inodes because its 32 GiB instance exposes
  about 4.1 million inodes even on the bounded 1 GiB volume. This does not
  raise the ordinary runner ceiling; communication still allows only one
  session and scans each of its 65 workspaces with the 8192-entry limit.
- `AONOHAKO_CGROUP_PARENT` is required for
  `selfhosted + embedded + helper` and rejected for other deployment shapes.
  Startup validates that the parent directory is under a cgroup v2 mount and
  exposes `cpu`, `memory`, and `pids`; each compile/execute/SPJ run is placed in
  a per-run cgroup with `memory.max`, `pids.max`,
  `cpu.max=100000 100000`, and `memory.oom.group=1`.
- `GET /livez` reports only API-process liveness. `GET /readyz` additionally
  rechecks mandatory work-root and delegated-cgroup invariants and returns
  `503` if they disappear. `GET /healthz` remains a compatibility alias for
  readiness. These endpoints do not require application authentication.
- `AONOHAKO_REMOTE_RUNNER_URL` points `remote` transport at another
  `aonohako` runner service and must be an absolute `http(s)` URL without
  embedded credentials, query strings, or fragments. Outside `dev`, bearer and
  Cloud Run identity-token authentication require an `https` URL.
- `AONOHAKO_REMOTE_RUNNER_AUTH` can be `none`, `bearer`, or
  `cloudrun-idtoken`; `none` is allowed only for `dev`
- `AONOHAKO_REMOTE_RUNNER_TOKEN` provides the bearer token when
  `AONOHAKO_REMOTE_RUNNER_AUTH=bearer`
- `AONOHAKO_REMOTE_RUNNER_AUDIENCE` overrides the ID-token audience for
  `cloudrun-idtoken` auth; it defaults to `AONOHAKO_REMOTE_RUNNER_URL`

Per-request execution limits are part of the `/execute` payload. The
[generated public limit table](docs/limits.md) is the canonical numeric
contract:

- `limits.time_ms`
- `limits.memory_mb`
- `limits.output_bytes`
  Defaults to `64 KiB` when omitted and is capped internally at `64 MiB`.
  This remains the execution capture and output-limit verdict boundary.
- `capture_limits.stdout_bytes` / `capture_limits.stderr_bytes`
  Optionally clip `/execute` result fields, step fields, and `log` events per
  stream without changing output comparison, OLE detection, or step handoffs.
  Each omitted member retains the default response cap of at most `64 KiB`;
  explicit `0` suppresses that returned stream, and the maximum override is
  `8 MiB`.
- `emit_logs`
  Defaults to `true` for both `/compile` and `/execute`. Set it to `false` to
  suppress compiler or contestant stdout/stderr `log` SSE events without
  changing judging or the final result payload.
- `stdin` and `expected_stdout`
  Each inline field is capped at `64 MiB` before a request enters the shared
  queue. Use `stdin_url` and `expected_stdout_url` to have Aonohako download
  HTTP(S) payloads server-side instead of embedding them in the JSON body.
- `data_url`
  `sources[]`, `binaries[]`, `programs[].binaries[]`, `spj.binary`, and
  `interactor.binaries[]` may use HTTP(S) `data_url` instead of `data_b64`.
  Server-side payload downloads reject URL credentials and any destination
  that resolves to loopback, private, link-local, multicast, unspecified, or
  otherwise reserved address space. The same policy is enforced for every
  redirect and at connection time. All execute binaries across top-level,
  program, SPJ, and interactor fields share one request-wide 48 MiB decoded
  budget across inline and URL-backed payloads. URLs are resolved only after
  request structure and collection counts pass validation.
- `enable_network`
  Cloud Run embedded-helper runners reject `true`. Self-hosted embedded-helper
  runners honor it only when `AONOHAKO_ALLOW_REQUEST_NETWORK=true` and
  `AONOHAKO_NETWORK_EGRESS_ISOLATED=true`, and then allow outbound
  `AF_INET`/`AF_INET6` client sockets only; listener syscalls and host `AF_UNIX`
  sockets stay blocked. Control-plane instances can forward networked workloads
  only to egress-isolated opted-in runners with `remote` transport.
- `runtime_profile`
  Requests may select an operator-defined `AONOHAKO_RUNTIME_TUNING_PROFILES`
  entry only when `AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE=true`. Public entry
  points should keep this disabled and let a trusted control plane attach the
  profile after applying problem policy.

## Security notes

This repository does not ship cloud-vendor deployment credentials or `gcloud`
workflow dependencies. The CI policy script fails if common secret-like or
cloud CLI markers are checked in, and it requires Dockerfile base images to be
digest-pinned or routed through digest-pinned build arguments.

The local execution path now enforces these invariants:

- the process working directory is `box/`
- each execution workspace root remains server-owned, is assigned to its
  sandbox role's GID, and is mode `0710` (group traverse only)
- submitted files are materialized with immutable permissions (`0444` or
  `0555`)
- the `box/` directory is writable so submissions can create new files beside
  their own sources or binaries
- interactive contestants run as UID/GID `65532`, while the trusted interactor
  runs as UID/GID `65531` behind a different group-traversal boundary; this
  prevents either peer from traversing or mutating the other's workspace while
  preserving server ownership of the workspace root, even though the helper
  backend still shares the host mount and `/proc` namespaces
- captured outputs reject symlinks to avoid read-through escapes

The runtime sandbox uses helper-process hardening rather than mount-based
filesystem isolation. It applies `setrlimit`, `PR_SET_NO_NEW_PRIVS`, seccomp,
fd cleanup, immutable submitted files, a writable per-run workspace, and
process-group cleanup. Self-hosted helper deployments can additionally set
`AONOHAKO_CGROUP_PARENT` to place each compile/execute/SPJ sandbox process in a
per-run cgroup with kernel-enforced memory, pids, and one-vCPU CPU bandwidth
limits.

Verdicts are classified from wall time, target CPU time, procfs RSS samples,
cgroup `memory.peak` when available, workspace scans, process exit state, and
output/SPJ evaluation in that order. No-cgroup helper runs exclude
`rusage.Maxrss` because it also contains API-server fork and helper setup memory.
Final run responses include optional `verdict_source` diagnostics such as
`cpu_time`, `memory_rss`, `workspace_bytes`, `file_output`, or `spj` so
operators can see which measurement or judge step selected the status. See
[docs/architecture.md](docs/architecture.md#verdict-classification-policy) for
the exact policy and the remaining environment-dependent boundaries.

Security posture depends on where it runs:

- `cloudrun + embedded + helper` is the supported production security target.
  Startup fails closed unless `AONOHAKO_WORK_ROOT` is configured, writable,
  not group/world writable, owned by the server UID, the process is running as
  root, `AONOHAKO_TRUSTED_RUNNER_INGRESS=true` is asserted, and the helper
  queue is single-slot.
- `cloudrun + remote + none` is the supported Cloud Run control-plane shape
  when `/compile` and `/execute` should be forwarded to a separate hardened
  runner. It still requires a bounded `AONOHAKO_WORK_ROOT` because the Cloud
  Run deployment contract requires a dedicated, bounded workspace root; local
  untrusted compile and execute work is forwarded to the remote runner.
- `selfhosted + embedded + helper` applies the same dedicated work-root
  contract for local root-backed containers and VMs, including
  `AONOHAKO_MAX_ACTIVE_RUNS=1` because separate requests still reuse the same
  sandbox UID. Its work root must be a dedicated bounded tmpfs mount with byte
  and inode ceilings. Because only one request runs at a time, that kernel
  backing-store ceiling also covers unlinked-open files and write/unlink bursts
  that directory scanning cannot observe. A delegated cgroup v2 parent is also
  mandatory, so compiler children and process-spawning runtime wrappers stay
  inside aggregate CPU, memory, pids, and cleanup accounting. Interactive peers
  within one request use the distinct fixed role identities described above.
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
- `AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET` when using platform auth outside
  `dev`
- `AONOHAKO_TRUSTED_RUNNER_INGRESS=true` after configuring private ingress,
  Cloud Run IAM, mTLS, or an equivalent trusted control-plane boundary
- second-generation execution environment
- service concurrency `1`
- a bounded in-memory volume mounted at a path such as `/work`, with
  `AONOHAKO_WORK_ROOT=/work`; set `AONOHAKO_REQUIRE_WORK_ROOT_TMPFS=true` when
  startup should fail unless that path is actually backed by `tmpfs`, and set
  `AONOHAKO_WORK_ROOT_MAX_BYTES` and `AONOHAKO_WORK_ROOT_MAX_FILES` to the
  intended volume byte and inode budgets
- container memory sized above the work-root byte budget plus runtime headroom,
  because Cloud Run/no-cgroup runners rely on the outer container limit as the
  final OOM boundary
- communication runner memory sized for every participant limit plus the
  512 MiB manager and server/work-root headroom; without child cgroups an outer
  container OOM terminates the instance and cannot be attributed to one
  participant
- `AONOHAKO_COMMUNICATION_ENABLED=true` only on a dedicated communication
  service; ordinary Cloud Run runners do not advertise `communication-v1`
- `AONOHAKO_COMMUNICATION_MAX_PARTICIPANTS=64` (or a smaller dedicated tier
  limit) to reject oversized communication requests before starting processes
- `AONOHAKO_COMMUNICATION_MEMORY_BUDGET_MB=24576` (for the planned 32 GiB
  service) to reject requests whose declared participant memory plus the
  512 MiB manager allowance exceeds the reserved 24 GiB execution budget
- `AONOHAKO_COMMUNICATION_CPU_COUNT=8`, `GOMAXPROCS=8`, and
  `AONOHAKO_COMMUNICATION_WALL_BUDGET_MS=600000` on the dedicated eight-vCPU
  service; startup rejects a CPU-count mismatch and requests whose
  contention-adjusted wall allowance cannot finish inside that session budget
- a 1 GiB communication work root. Communication participants use a fixed
  8 MiB per-process workspace cap, the manager uses 128 MiB, and admission
  reserves 20% of the work root before any process starts
- Direct VPC egress with `all-traffic` routing and firewall-denied outbound
  traffic except for explicitly allowed targets
- a dedicated service account with no unnecessary IAM permissions and no baked
  secrets in the image

For a Cloud Run API/control-plane service that forwards `/compile` and
`/execute`, use
`AONOHAKO_EXECUTION_TRANSPORT=remote`,
`AONOHAKO_SANDBOX_BACKEND=none`, the same bounded `AONOHAKO_WORK_ROOT`, and a
private `AONOHAKO_REMOTE_RUNNER_URL` with `AONOHAKO_REMOTE_RUNNER_AUTH=bearer`
or `AONOHAKO_REMOTE_RUNNER_AUTH=cloudrun-idtoken`.

Cloud Run's own documentation states that volumes must be configured through
Cloud Run volume mounts and that arbitrary in-container mounting is not
supported, so `aonohako` does not depend on cgroup creation or mount-based
filesystem isolation when running there.

### Runtime memory tuning

The default runtime memory profile is locked down for public judge runners.
Operators can narrow selected numeric knobs without passing arbitrary runtime
flags through requests:

- `AONOHAKO_JVM_HEAP_PERCENT` controls the Java/Kotlin-JVM/Clojure/Groovy/Scala
  `-Xmx` share of the request memory limit. Java-family launchers also set
  direct-memory and metaspace/class-space caps from the request memory limit.
  Allowed range: `25..75`, default `50`.
- `AONOHAKO_GO_MEMORY_RESERVE_MB` subtracts reserved host/runtime memory from
  Go-based interpreter `GOMEMLIMIT`. Allowed range: `0..256`, default `32`.
- `AONOHAKO_GO_GOGC` controls Go GC aggressiveness for Go-based interpreters.
  Allowed range: `10..200`, default `50`.
- `AONOHAKO_ERLANG_SCHEDULERS` controls BEAM scheduler count for Erlang/Elixir.
  Allowed range: `1..4`, default `1`.
- `AONOHAKO_ERLANG_ASYNC_THREADS` controls BEAM async thread count for
  Erlang/Elixir. Allowed range: `0..4`, default `1`.
- `AONOHAKO_DOTNET_GC_HEAP_PERCENT` controls the .NET and PowerShell GC heap
  hard-limit share of request memory; the runner converts it to
  `DOTNET_GCHeapHardLimit`.
  Allowed range: `25..80`, default `60`. `dotnet`/Dafny use a high finite 2 TiB
  `RLIMIT_FSIZE` floor for CoreCLR/F# compatibility, so their practical
  disk-burst guard is workspace scanning plus bounded work-root/container
  storage rather than a tight file-size rlimit.
- `AONOHAKO_KOTLIN_NATIVE_COMPILER_HEAP_MB` controls Kotlin/Native compiler
  JVM heap. Allowed range: `256..1536`, default `1024`.
- `AONOHAKO_DENO_OLD_SPACE_PERCENT` controls the Deno/V8 old-space share used
  for `--v8-flags=--max-old-space-size=...`. Allowed range: `30..75`, default
  `60`.
- `AONOHAKO_NODE_OLD_SPACE_PERCENT` controls the Node/V8 old-space share of
  the request memory limit. Allowed range: `30..75`, default `60`.
- `AONOHAKO_NODE_MAX_SEMI_SPACE_MB` caps Node/V8 semi-space. Allowed range:
  `1..16`, default `8`.
- `AONOHAKO_NODE_STACK_SIZE_KB` sets Node stack size. Allowed range:
  `512..8192`, default `2048`.
- `AONOHAKO_WASMTIME_MEMORY_GUARD_BYTES` sets the Wasmtime guard size.
  Allowed range: `65536..16777216`, default `65536`.
- `AONOHAKO_WASMTIME_MAX_WASM_STACK_BYTES` sets the Wasmtime wasm stack cap.
  Allowed range: `262144..8388608`, default `1048576`.
- `AONOHAKO_RUNTIME_TUNING_PROFILES` may define named, policy-owned runtime
  profiles as a JSON object. Each profile inherits the global tuning values and
  may override the same bounded numeric keys with snake_case names, for example
  `{"low-memory":{"jvm_heap_percent":35,"node_old_space_percent":45}}`.
  `/compile` and `/execute` may select one with `runtime_profile` only when
  `AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE=true`; policy-disabled, unknown, or
  syntactically invalid profile names are rejected.
- `AONOHAKO_PROBLEM_RUNTIME_PROFILES` maps bounded `problem_id` strings to
  those named profiles, for example `{"contest-1/a":"low-memory"}`. A mapped
  `problem_id` applies the profile even when direct request profile selection
  is disabled; conflicting `runtime_profile` values are rejected. Remote runner
  pools should receive the same runtime profile config as the control plane
  when forwarded requests include policy-selected profiles.

Invalid values fail startup. These settings only tune memory-related runtime
caps; they do not expose network, filesystem, process, or arbitrary flag
controls to submissions.

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
