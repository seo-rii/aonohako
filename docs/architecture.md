# Architecture

## Scope

`aonohako` is the low-level compile and execute service used by an online judge
control plane. It accepts compile and run requests over HTTP, streams progress
and results over SSE, and executes untrusted submissions inside a hardened
runtime image.

The current production target is Cloud Run. The design intentionally avoids
mechanisms that Cloud Run cannot be relied on to provide consistently, such as
runtime-created cgroups, mount-based filesystem sandboxes, `chroot`,
`pivot_root`, or Landlock.

Related docs:

- [protocol.md](./protocol.md): API contract
- [payload.md](./payload.md): request and response examples
- [selfhosted.md](./selfhosted.md): self-hosted topology guidance
- [`runtime-images.yml`](../runtime-images.yml): runtime image catalog

## System View

```
                ┌─────────────────────────────────────────┐
                │             aonohako (HTTP)            │
                │                                         │
POST /compile ─▶│  API + SSE  ─▶  Compile Service         │
                │                                         │
POST /execute ─▶│  API + Queue ─▶  Execute Service        │
                │                         │               │
                │                         ├─ root parent  │
                │                         ├─ helper exec  │
                │                         └─ target code  │
                │                                         │
GET /healthz  ─▶│  health check                           │
                └─────────────────────────────────────────┘
```

High-level responsibilities:

| Package | Responsibility |
| --- | --- |
| `cmd/server` | HTTP entry point |
| `internal/api` | request decoding, SSE wiring, queue gating |
| `internal/compile` | language-specific build drivers and artifact collection |
| `internal/execute` | sandboxed execution, output comparison, SPJ handling |
| `internal/profiles` | compile/run language registry |
| `internal/security` | workspace-scoped env and thread limit env |
| `internal/sandbox` | helper-process request bridge and Linux hardening |
| `internal/timing` | monotonic wall clock and process CPU clock helpers |
| `internal/queue` | bounded FIFO execution queue |

## Request Lifecycle

### Compile

`/compile` writes decoded sources into a temporary directory, resolves the
language profile, and runs the appropriate toolchain with a 60-second timeout.
Artifacts are returned as base64 payloads. This step is for building judge
artifacts, not for enforcing the main untrusted runtime boundary.
Compiler frontends still parse attacker-controlled source code, so production
deployments should treat `/compile` as an untrusted execution surface rather
than a safe control-plane helper.

When `AONOHAKO_EXECUTION_TRANSPORT=remote`, both `/compile` and `/execute` are
forwarded to the downstream runner, so non-root control-plane instances do not
build or run untrusted inputs locally.
High-trust deployments should use that remote shape or run local compile only
inside the same hardened single-slot runner envelope as execution.

Even so, the local compile path applies the same helper-process hardening model
as `/execute` when it runs as a root-backed embedded helper:

- submitted source files are made immutable (`0444`)
- the compile root and any nested submitted source directory are sticky and
  writable (`01777`) so compilers can create sibling outputs without replacing
  submitted files by name
- workspace-scoped scratch directories stay sandbox-owned and private (`0700`)
- Python-like compile checks run in isolated startup mode (`-I -S`) so
  submission-controlled `sitecustomize.py`, user site packages, and `PYTHON*`
  environment hooks do not execute during bytecode compilation

### Execute

`/execute` is the security-sensitive path.

1. The request acquires a queue permit.
2. A per-run workspace is created under `AONOHAKO_WORK_ROOT` when the selected
   runtime shape requires a dedicated work root, or under the system temp root
   for local development shapes that do not.
3. Submitted files are materialized into `box/`.
4. Existing submitted files are immutable:
   - regular files: `0444`
   - executable files: `0555`
5. `box/` itself is writable and sticky (`01777`) so the submission can create
   new files in the same folder without overwriting somebody else's existing
   files by name.
6. Any nested submission directory created under `box/` is also made sticky and
   writable so `pkg/Main.class` style layouts remain readable and can create
   sibling files under the sandbox UID.
7. Side directories such as `.tmp`, `.cache`, `.home`, `.mix`, `.hex`, and
   image output directories are created per request and redirected through
   environment variables.
8. The parent either starts the local sandbox helper or forwards the request to
   a remote hardened runner, depending on the configured execution transport.
9. The local helper applies hardening, then `execve()`s the real target
   command. The remote transport proxies the same SSE event contract from the
   downstream runner.
10. The parent watches time, memory, workspace growth, stdout, stderr, and
    optional sidecar image output when running locally.
11. The parent compares output or runs an SPJ and returns the final result.

## Sandbox Process Model

The runtime uses a parent/helper/target split:

1. Parent process:
   - prepares the workspace
   - passes the helper request JSON through an inherited pipe file descriptor
   - opens stdout and stderr pipes
   - starts the helper in its own process group
   - kills the entire group on timeout or quota violation

2. Helper process:
   - runs from the same `aonohako` binary in internal mode
   - reads the helper request from the inherited pipe file descriptor
   - applies `setrlimit`
   - enables `PR_SET_DUMPABLE=0`
   - enables `PR_SET_NO_NEW_PRIVS=1`
   - installs seccomp
   - closes inherited file descriptors
   - changes directory to `box/`
   - `execve()`s the target runtime or binary

3. Target process:
   - runs with the request-specific environment
   - inherits the helper's limits and seccomp filter
   - stays in the same process group for cleanup

`/execute` requires a root parent. The parent drops the helper/target to
UID/GID `65532`, while the runtime image is hardened so only explicitly
readable paths remain accessible to that account.

The normal embedded-helper path does not materialize the helper request as a
workspace file. The parent writes the request JSON to an inherited pipe fd and
the helper consumes that fd before applying the target hardening; the legacy
request-file environment variable remains accepted only for direct helper
compatibility.

## Enforcement Layers

### Process and syscall controls

The Linux helper applies:

| Layer | Mechanism | Notes |
| --- | --- | --- |
| CPU hard limit | `RLIMIT_CPU` | helper-side hard stop |
| Address space limit | `RLIMIT_AS` | based on request memory plus language-specific virtual-memory headroom; .NET remains the compatibility exception |
| Stack size | `RLIMIT_STACK=8MiB` | bounds native stack growth and argument/environment footprint |
| Locked memory | `RLIMIT_MEMLOCK=0` | prevents `mlock`-style RAM pinning |
| POSIX message queue bytes | `RLIMIT_MSGQUEUE=0` | prevents message-queue allocation by the sandbox UID |
| Open files | `RLIMIT_NOFILE=64` | keeps fd surface small |
| Tasks | `RLIMIT_NPROC` | sized from current UID usage plus thread limit |
| File growth | `RLIMIT_FSIZE` | tied to workspace byte limit; .NET disables this rlimit for CoreCLR/F# compatibility and relies on workspace accounting |
| Workspace breadth | periodic workspace scan | enforces total bytes plus entry-count and depth caps |
| Core dumps | `RLIMIT_CORE=0` | disables core files |
| Privilege escalation | `PR_SET_NO_NEW_PRIVS` | prevents gaining new privileges after exec |
| Dumpability | `PR_SET_DUMPABLE=0` | blocks ptrace-style exposure |
| FD inheritance | `CloseRange(3, ..., CLOSE_RANGE_CLOEXEC)` fallback `CloseOnExec` loop | blocks descriptor inheritance across `execve` |
| Process cleanup | `Setpgid` + group kill | kills helper and target together |

The seccomp filter denies high-risk operations, including:

- `fork`, `vfork`, and `clone3`
- `clone` without `CLONE_THREAD`
- `unshare`, `setns`, `chroot`, `mount`, `pivot_root`, and newer mount APIs
- `ptrace`, `process_vm_*`, `pidfd_*`
- `kill`, `tkill`, `tgkill`
- `prlimit64`, `setpriority`
- `bpf`, `io_uring_*`, `userfaultfd`, memory locking, SysV shared memory, `perf_event_open`
- `open_by_handle_at`, `name_to_handle_at`
- `fanotify_*`, keyring syscalls, module loading, swap, reboot, syslog
- `chmod`, `chown`, `mknod`

The helper must allow the initial `execve()` into the requested runtime or
compiled binary. In the current denylist profile, that also leaves a post-start
`execve()` surface: normal child-process creation is blocked, so shell-spawn
patterns generally cannot fork a separate process, but a running submission can
replace itself with another world-executable binary that is present in the
runtime image. This is tracked as an image-surface risk until language-family
allowlist profiles and minimal execute-only images are available.

Per-request network disable adds seccomp denies for socket-related syscalls:

- `socket`, `socketpair`
- `connect`, `bind`, `listen`, `accept`, `accept4`, `shutdown`
- `sendto`, `sendmsg`, `sendmmsg`
- `recvfrom`, `recvmsg`, `recvmmsg`

When `enable_network=true` on a self-hosted embedded-helper runner, seccomp
still keeps the surface narrower than the default host namespace:

- `socket()` is limited to `AF_INET` and `AF_INET6`
- `bind`, `listen`, `accept`, and `accept4` stay denied
- host `AF_UNIX` sockets remain blocked; only explicit local socketpair
  allowances for managed runtimes survive

This is paired with two additional protections:

- proxy-related environment variables are cleared for network-disabled requests
- deployment-level egress policy should still be deny-by-default on Cloud Run
- Cloud Run embedded-helper execution rejects `enable_network=true` outright
  because metadata endpoints cannot be reliably excluded inside the helper
  process alone; networked workloads should run through a self-hosted runner,
  either directly or through `remote` transport

### Workspace controls

The execution workspace is intentionally split:

| Path | Purpose |
| --- | --- |
| `box/` | submission-visible working directory |
| `.tmp` | temp files for runtimes |
| `.cache` | generic cache root |
| `.home` | synthetic HOME |
| `.mix`, `.hex` | Elixir caches |
| `.pip-cache`, `.mpl`, `.nuget`, `.konan*` | language/runtime-specific caches |
| `__img__/` | image sidecar output |

Environment variables redirect common runtime scratch paths into the per-run
workspace, for example `HOME`, `TMPDIR`, `JAVA_TOOL_OPTIONS`,
`XDG_CACHE_HOME`, `PIP_CACHE_DIR`, `MIX_HOME`, and `HEX_HOME`.

To avoid escaping into global writable directories, the runtime image itself
ships shared scratch paths such as `/tmp`, `/var/tmp`, and `/run/lock` with
non-writable permissions for the sandbox UID. The entrypoint no longer mutates
container-global scratch state at startup.

### Output capture

`stdout` and `stderr` are captured through pipes into capped in-memory buffers.
The request field `limits.output_bytes` controls both:

- the live capture buffer size
- the maximum response payload returned to the caller

Defaults and caps:

- default: `64 KiB`
- hard cap: `8 MiB`

Requested file outputs are validated as relative paths. At most one file output
may replace judged stdout; missing, symlinked, or non-regular outputs are
reported as runtime failure instead of silently falling back to process stdout.

## Time and Memory Accounting

`aonohako` distinguishes wall-clock time from CPU time.

| Metric | Source | Why |
| --- | --- | --- |
| `wall_time_ms` | `CLOCK_MONOTONIC` | stable wall clock, not affected by time jumps |
| `cpu_time_ms` | `CLOCK_PROCESS_CPUTIME_ID` on the target PID | aggregates all threads inside the process |
| `memory_kb` | `/proc/<pid>/statm` sampled during execution, `/proc/<pid>/smaps_rollup` near the limit or when AS is disabled, then `rusage.Maxrss` fallback | captures live RSS peaks, uses a more accurate procfs source in risky ranges, and keeps a post-exit fallback |

Important consequence:

- multithreading is allowed
- multiprocessing is not allowed by seccomp
- because `fork`/`vfork`/`clone3` are denied and only thread-form `clone` is
  allowed, `cpu_time_ms` remains meaningful for the whole submission process

Memory enforcement uses several layers:

- live RSS sampling from `/proc/<pid>/statm`
- `/proc/<pid>/smaps_rollup` confirmation when RSS reaches 80% of the limit or when the runtime cannot use address-space limits
- `RLIMIT_AS` to constrain virtual address space growth; native programs use a tight memory-plus-slack cap, while Node, Wasmtime, and umjunsik-lang-go use higher but finite virtual caps
- runtime memory knobs for managed runtimes: Node receives V8 old-space, semi-space, stack, and disabled wasm trap-handler flags; Wasmtime receives memory-reservation, linear-memory, table, instance, and wasm-stack caps; umjunsik-lang-go receives `GOMEMLIMIT` and lower `GOGC`
- deployment-validated runtime tuning for selected Node and Wasmtime numeric
  knobs, with bounded environment variables and startup rejection for unsafe
  values rather than request-controlled arbitrary runtime flags
- child `oom_score_adj=1000` as a best-effort fallback so the sandboxed process is preferred over the server if the host/container OOM killer has to choose
- a post-exit address-space proximity check with slack
- workspace byte accounting, so temp-file growth is also limited

### Verdict Classification Policy

The runner treats verdict classification as a best-effort policy layered on top
of Linux process accounting, not as a perfectly reproducible hardware benchmark.
The stable contract is:

- wall time is the outer safety deadline; if the request context expires before
  the target exits, the process group is killed and the run is classified as
  `Time Limit Exceeded` with reason `wall time limit exceeded` unless an earlier
  resource verdict was already recorded
- CPU time is sampled from `CLOCK_PROCESS_CPUTIME_ID` after the helper has
  execed the target; because the execute sandbox denies process creation and
  allows only thread-form `clone`, this includes all target threads
- RSS is sampled from procfs immediately after helper start, refined with
  `smaps_rollup` near the limit or when address-space limits are disabled, and
  falls back to `rusage.Maxrss` after exit for reporting
- workspace limit classification is based on periodic workspace scans for total
  bytes, entry count, and directory depth
- when a watchdog observes TLE, MLE, or WLE, that first resource verdict kills
  the process group and is preserved through process exit
- after a normal `OK` resource run, non-zero exit becomes `Runtime Error`; then
  stdout/file-output or SPJ evaluation decides between `Accepted` and
  `Wrong Answer`

Operators should compare results within one deployment profile. Cloud Run CPU
allocation, JIT warmup, GC timing, runtime memory reservations, and procfs
sampling races can still change the exact boundary between TLE, MLE, WLE, and
runtime error. Language profile multipliers in `internal/profiles` are the
explicit place to document broad runtime compensation; per-problem limits remain
the main way to absorb known JIT or GC cost.

Compile commands use the same helper process-hardening path. Because compilers
can legitimately spawn child processes, compile memory enforcement also samples
the helper process tree and kills the compile sandbox when aggregate RSS exceeds
the compile sandbox memory budget. This remains best-effort accounting until a
cgroup-backed compile backend is available.

.NET is the main compatibility exception: `dotnet` invocations still disable
`RLIMIT_AS` and `RLIMIT_FSIZE` because CoreCLR reserves a very large
memfd-backed double-mapped region before user code starts, and F# compiler
startup can fail under finite file-size rlimits. The helper still applies RSS
watchdogs, workspace byte accounting, output caps, open-file limits, thread
limits, OOM-victim preference, and single-slot execution. Before each sandboxed
`dotnet` invocation, the runner recreates `/tmp/.dotnet` with the sandbox UID
and `0700` modes so CoreCLR/F# shared lock
and shared-memory state does not leak between sequential runs in the same
container.

Self-hosted cgroup support is being staged behind an explicit preflight layer
before it is wired into execution. `internal/isolation/cgroup` currently checks
for a cgroup v2 mount, `cgroup.controllers`, `cgroup.subtree_control`, and the
required `cpu`, `memory`, and `pids` controllers. The `io` controller is
reported separately because it is useful for future throttling but not required
for the first hard memory/process boundary. This preflight does not mutate the
host cgroup tree; future self-hosted backends should fail closed when it reports
that required controls are unavailable.

The same package also owns the low-level run-group write contract for the
future backend. Parent cgroups enable child controllers by writing values such
as `+cpu +memory +pids` to `cgroup.subtree_control`. A run group must then be
created with positive `memory.max` and `pids.max` values, and
`memory.oom.group` is set so the kernel treats the run as one OOM domain.
`cpu.max` is written only when both quota and period are set, and a target
process is admitted by writing its PID to `cgroup.procs`. Cleanup removes the
run cgroup directory without recursive deletion so leftover processes or files
surface as cleanup errors. The helper execution path does not use this yet; it
remains the contract that the self-hosted isolated backend will wire into
process launch.

The accounting reader for that future backend reads `memory.current`,
`memory.peak` when present, `memory.events`, `pids.current`, `pids.events`,
and `cpu.stat`. Verdict integration should prefer `memory.events` `max`,
`oom`, `oom_kill`, and `oom_group_kill`, `pids.events` `max`, and `cpu.stat`
throttling counters over RSS polling once cgroups own the run boundary.

## Deployment Contract

The runtime now separates three concerns:

- `AONOHAKO_DEPLOYMENT_TARGET`: `cloudrun`, `selfhosted`, or `dev`
- `AONOHAKO_EXECUTION_TRANSPORT`: `embedded` or `remote`
- `AONOHAKO_SANDBOX_BACKEND`: `helper` or `none` in supported deployments.
  `container` is recognized only as a reserved future backend value.

`AONOHAKO_EXECUTION_MODE` remains as a compatibility shorthand that maps to the
legacy embedded-helper shapes.

Supported combinations today:

- `cloudrun + embedded + helper`: supported production target
- `cloudrun + remote + none`: supported Cloud Run control-plane target that
  forwards `/compile` and `/execute` to another hardened runner; it still
  requires `AONOHAKO_WORK_ROOT`
- `selfhosted + embedded + helper`: supported root-backed local/container target
- `dev + remote + none`: supported non-root control-plane target that forwards
  `/compile` and `/execute` to another runner

`embedded + container` is reserved for a future self-hosted backend and is
currently rejected at startup.

The supported shapes map to an explicit runtime security contract in
`internal/platform`:

| Shape | Contract | Local guarantees | Missing local boundary |
| --- | --- | --- | --- |
| `embedded + helper` | `embedded-helper-process-hardening` | root parent with dropped UID child, `setrlimit`, `PR_SET_NO_NEW_PRIVS`, seccomp denylist, network syscall gate, fd cleanup, process-group cleanup, immutable submissions, symlink-safe output capture, workspace accounting | per-run cgroup, mount namespace, read-only rootfs, masked `/proc`, per-run UID, child-process accounting, seccomp allowlist, post-start `execve()` blocking |
| `remote + none` | `remote-control-plane` | `/compile` and `/execute` are forwarded to the configured runner and no local untrusted compile/run work is performed | isolation is delegated to the downstream runner and its private ingress/auth boundary |
| `embedded + container` | `reserved-container-isolation` | not implemented | must provide per-run cgroup, mount namespace, read-only rootfs, masked `/proc`, per-run UID or user namespace, child-process accounting, allowlist-oriented seccomp, and post-start `execve()` blocking before it can be enabled |

Server startup validates the deployment contract instead of trusting docs alone.
The following checks are enforced before the HTTP server starts:

- Cloud Run marker envs require `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`
- unsupported runtime security contracts fail startup before request handling
- `remote` transport requires `AONOHAKO_REMOTE_RUNNER_URL`
- `AONOHAKO_REMOTE_RUNNER_AUTH=none` is rejected outside `dev`
- `remote + bearer` requires `AONOHAKO_REMOTE_RUNNER_TOKEN`
- `remote + cloudrun-idtoken` defaults its audience to the remote runner URL if
  `AONOHAKO_REMOTE_RUNNER_AUDIENCE` is unset
- remote runner SSE responses are parsed with bounded line, event, and stream
  sizes, and the remote HTTP transport sets dial, TLS handshake, response
  header, idle connection, and SSE idle heartbeat timeouts
- remote runner protocol-version headers are backward compatible when absent
  and fail closed when present with an unsupported value
- malformed remote `log`, `image`, `error`, or `result` events fail the remote
  request as a protocol error instead of being silently ignored
- inbound `/compile` and `/execute` authentication defaults to bearer tokens
  outside `dev`; `AONOHAKO_INBOUND_AUTH=platform` must be explicit when an
  upstream platform layer owns inbound authentication
- `AONOHAKO_INBOUND_AUTH=none` is rejected outside `dev`
- numeric values such as `AONOHAKO_MAX_ACTIVE_RUNS`,
  `AONOHAKO_MAX_PENDING_QUEUE`, `AONOHAKO_MAX_ACTIVE_STREAMS`,
  `AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS`,
  `AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE`, and
  `AONOHAKO_HEARTBEAT_INTERVAL_SEC`, and
  `AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC` are strict; malformed or out-of-range
  values fail startup
- `AONOHAKO_ALLOW_REQUEST_NETWORK` is strict boolean configuration and defaults
  to `true` only for `dev`; outside `dev`, client-supplied `enable_network=true`
  is rejected unless this is explicitly enabled for a dedicated runner policy
- `cloudrun` always requires `AONOHAKO_WORK_ROOT`
- `selfhosted + embedded + helper` requires `AONOHAKO_WORK_ROOT`
- every required work root must already exist, be a directory, be owned by the
  current server UID, not be group/world writable, and accept a probe
  directory create/remove cycle
- `embedded + helper` requires the process to be running as root
- `embedded + helper` also requires `AONOHAKO_MAX_ACTIVE_RUNS=1` so helper
  executions do not overlap under the shared sandbox UID
- `/compile` and `/execute` streams are capped globally, and outside `dev` they
  are also capped per principal. Bearer auth uses a token fingerprint as the
  principal key; platform auth uses the upstream principal header such as
  `X-Aonohako-Principal`. Platform auth is safe only behind an upstream
  identity layer that strips client-supplied identity headers and rewrites the
  trusted principal header before the request reaches `aonohako`.
- Outside `dev`, `/compile` and `/execute` requests are also capped per
  principal in a fixed one-minute window before they enter the run queue.

Recommended Cloud Run deployment baseline:

- `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`
- `AONOHAKO_EXECUTION_TRANSPORT=embedded`
- `AONOHAKO_SANDBOX_BACKEND=helper`
- `AONOHAKO_API_BEARER_TOKEN` set to a strong secret, unless
  `AONOHAKO_INBOUND_AUTH=platform` is set because Cloud Run IAM, mTLS, private
  ingress, or a gateway enforces inbound authentication
- second-generation execution environment
- service concurrency `1`
- bounded in-memory volume mounted at `AONOHAKO_WORK_ROOT`
- separate runner service account with minimal IAM permissions
- Direct VPC egress with `all-traffic`
- firewall-denied outbound traffic except for explicitly allowed destinations

For a Cloud Run API/control-plane service that forwards `/compile` and
`/execute` to a separate runner, use `cloudrun + remote + none` with the same bounded
`AONOHAKO_WORK_ROOT` requirement and a private `AONOHAKO_REMOTE_RUNNER_URL`.

Recommended non-Cloud-Run control-plane baseline:

- `AONOHAKO_DEPLOYMENT_TARGET=dev`
- `AONOHAKO_EXECUTION_TRANSPORT=remote`
- `AONOHAKO_SANDBOX_BACKEND=none`
- `AONOHAKO_REMOTE_RUNNER_URL=https://<dedicated-runner>`
- optional `AONOHAKO_REMOTE_RUNNER_AUTH=bearer` with
  `AONOHAKO_REMOTE_RUNNER_TOKEN=...`
- or `AONOHAKO_REMOTE_RUNNER_AUTH=cloudrun-idtoken` when the downstream runner
  is another Cloud Run service

Why the design looks this way:

- Cloud Run is the intended security boundary, not nested container tricks
- the runtime does not depend on child cgroup creation
- the runtime does not depend on mount-based filesystem isolation
- the runtime does not assume Landlock availability
- Cloud Run marker env vars alone do not switch security policy; the deployment
  target is explicit to avoid accidental partial hardening
- the helper backend intentionally serializes active runs because every
  sandboxed process currently drops to the same UID/GID pair inside the runner

## Self-Hosted Scale Path

`selfhosted + embedded + helper` is supported, but it deliberately keeps one
active execution per instance. The helper backend drops targets to a shared
sandbox UID and depends on a dedicated work root plus immutable submitted
files, so startup rejects `AONOHAKO_MAX_ACTIVE_RUNS` values other than `1`.

For higher-throughput self-hosted deployments, prefer this shape:

1. API/control-plane instances in `dev + remote + none`
2. dedicated runner instances in `selfhosted + embedded + helper`
3. `AONOHAKO_MAX_ACTIVE_RUNS=1` on every runner instance
4. horizontal scale by adding runner instances instead of increasing local
   helper slots

`embedded + container` is reserved for a future self-hosted backend. It should
not be enabled until it can provide stronger per-run ownership separation and a
dedicated writable filesystem view for each execution.

## Runtime Image Model

Runtime images are generated from [`runtime-images.yml`](../runtime-images.yml).
One catalog drives both production images and CI smoke images.

Production profiles currently group languages like this:

| Profile | Languages |
| --- | --- |
| `type-a` | `aheui`, `bf`, `elixir`, `erlang`, `haskell`, `lisp`, `lua`, `ocaml`, `perl`, `php`, `plain`, `prolog`, `pypy`, `r`, `racket`, `ruby`, `sqlite`, `wasm`, `whitespace` |
| `type-b` | `clojure`, `groovy`, `java`, `javascript`, `scala`, `typescript` |
| `type-c` | `ada`, `asm`, `d`, `fortran`, `go`, `nasm`, `nim`, `pascal`, `rust`, `zig` |
| `type-d` | `kotlin` |
| `type-e` | `csharp`, `fsharp` |
| `type-f` | `uhmlang` |
| `type-g` | `julia` |
| `type-h` | `swift` |
| `type-i` | `plain`, `python`, `java` |
| `type-j` | `coq` |
| `type-k` | `dart` |
| `type-l` | `python` |

CI mode expands the same catalog into one image per language so each smoke job
validates a single runtime in isolation. A dedicated CI summary job builds the
production profiles in a parallel matrix and runs
`scripts/report_toolchain_versions.sh` once per profile. Each matrix leg uploads
its summary fragment and a `docker save` archive for the image as artifacts. A
final CI summary job downloads those artifacts, concatenates the per-profile
reports into one GitHub Actions summary, and republishes the summaries plus
image archives as a single bundle artifact.

Debian-based production profiles now use a digest-pinned
`debian:trixie-slim` base, which raises the baseline Python, PyPy, and GCC
versions seen by both production and CI runtime images while keeping base-image
drift explicit in review. The Go builder image and non-Debian profile bases are
also digest pinned. Python judge libraries are pinned in the catalog so rebuilds
stay reproducible, and vendored helpers such as `jungol_robot` are copied into
the runtime image directly because they are not published on PyPI.

The runtime Docker image is also hardened to reduce the readable surface for the
sandbox UID. Non-essential metadata and package-manager paths are made
root-only, while shared libraries and language runtimes remain readable so the
interpreter or binary can still start normally.

Until execute-only images are split from compile images, image maintainers
should treat every world-executable binary in the runtime image as reachable by
submissions through the post-start `execve()` surface. Keep shells, package
managers, compilers, debuggers, and diagnostics tooling out of execute images
where language support allows it, and keep the remaining image content free of
secrets.

## Security Boundary and Non-goals

This is the most important operational point.

What `aonohako` does aim to protect:

- other active requests
- inherited file descriptors
- process creation outside the allowed threading model
- network access when disabled
- writes outside the per-run workspace
- common sandbox escape primitives based on namespaces, mounts, ptrace, pidfds,
  signals, and privileged syscalls

What the current Cloud Run-compatible design does not claim:

- full filesystem read isolation from the runtime image
- a mount-level view that exposes only `box/`
- prevention of replacing the running process with another world-executable
  binary from the runtime image
- prevention of dynamic code execution inside languages such as Python, Elixir,
  JavaScript, or Java

In practice, submissions should be treated as able to read world-readable files
inside the runtime image. That is why the image must never contain secrets,
private credentials, or sensitive configuration.

## Verification Strategy

The repository verifies the design through:

- Go unit and integration tests around compile and execute behavior
- selftests embedded in runtime images
- smoke builds generated from the runtime catalog
- `govulncheck` in CI for Go dependency and standard-library reachability
- Syft SBOM generation in CI for the root-backed Python runtime image used by
  sandbox regression tests
- non-blocking Grype scan artifacts in CI for the same sandbox runtime image,
  so runtime CVE drift is visible before promotion
- regression tests for sandbox escape attempts such as network use, process
  creation, inherited-fd access, and writable scratch bypasses
- root-backed sandbox regression tests executed inside a runtime container in CI,
  with skip paths promoted to failures there
- operational image promotion pipelines should still turn CVE scans into
  release gates for the full production matrix and add image signing before
  promotion

For operational use, keep architecture and security decisions aligned with the
actual code in `internal/execute`, `internal/sandbox`, and
`docker/runtime.Dockerfile`.
