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
   - writes the helper request file
   - opens stdout and stderr pipes
   - starts the helper in its own process group
   - kills the entire group on timeout or quota violation

2. Helper process:
   - runs from the same `aonohako` binary in internal mode
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

## Enforcement Layers

### Process and syscall controls

The Linux helper applies:

| Layer | Mechanism | Notes |
| --- | --- | --- |
| CPU hard limit | `RLIMIT_CPU` | helper-side hard stop |
| Address space limit | `RLIMIT_AS` | based on request memory plus headroom |
| Open files | `RLIMIT_NOFILE=64` | keeps fd surface small |
| Tasks | `RLIMIT_NPROC` | sized from current UID usage plus thread limit |
| File growth | `RLIMIT_FSIZE` | tied to workspace byte limit |
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
- `bpf`, `io_uring_*`, `userfaultfd`, `perf_event_open`
- `open_by_handle_at`, `name_to_handle_at`
- `fanotify_*`, keyring syscalls, module loading, swap, reboot, syslog
- `chmod`, `chown`, `mknod`

Per-request network disable adds seccomp denies for socket-related syscalls:

- `socket`, `socketpair`
- `connect`, `bind`, `listen`, `accept`, `accept4`, `shutdown`
- `sendto`, `sendmsg`, `sendmmsg`
- `recvfrom`, `recvmsg`, `recvmmsg`

This is paired with two additional protections:

- proxy-related environment variables are cleared for network-disabled requests
- deployment-level egress policy should still be deny-by-default on Cloud Run

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

To avoid escaping into global writable directories, the runtime image entrypoint
hardens shared scratch directories such as `/tmp`, `/var/tmp`, `/dev/shm`, and
`/run/lock` once at container startup before the server begins accepting
requests.

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
| `memory_kb` | `/proc/<pid>/statm` sampled during execution, then `rusage.Maxrss` fallback | captures live RSS peaks and keeps a post-exit fallback |

Important consequence:

- multithreading is allowed
- multiprocessing is not allowed by seccomp
- because `fork`/`vfork`/`clone3` are denied and only thread-form `clone` is
  allowed, `cpu_time_ms` remains meaningful for the whole submission process

Memory enforcement uses several layers:

- live RSS sampling from `/proc/<pid>/statm`
- `RLIMIT_AS` to constrain virtual address space growth
- a post-exit address-space proximity check with slack
- workspace byte accounting, so temp-file growth is also limited

## Deployment Contract

The runtime now separates three concerns:

- `AONOHAKO_DEPLOYMENT_TARGET`: `cloudrun`, `selfhosted`, or `dev`
- `AONOHAKO_EXECUTION_TRANSPORT`: `embedded` or `remote`
- `AONOHAKO_SANDBOX_BACKEND`: `helper`, `container`, or `none`

`AONOHAKO_EXECUTION_MODE` remains as a compatibility shorthand that maps to the
legacy embedded-helper shapes.

Supported combinations today:

- `cloudrun + embedded + helper`: supported production target
- `selfhosted + embedded + helper`: supported root-backed local/container target
- `dev + remote + none`: supported non-root control-plane target that forwards
  `/execute` to another runner

`embedded + container` is reserved for a future self-hosted backend and is
currently rejected at startup.

Server startup validates the deployment contract instead of trusting docs alone.
The following checks are enforced before the HTTP server starts:

- Cloud Run marker envs require `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`
- `remote` transport requires `AONOHAKO_REMOTE_RUNNER_URL`
- `remote + bearer` requires `AONOHAKO_REMOTE_RUNNER_TOKEN`
- `remote + cloudrun-idtoken` defaults its audience to the remote runner URL if
  `AONOHAKO_REMOTE_RUNNER_AUDIENCE` is unset
- `cloudrun` always requires `AONOHAKO_WORK_ROOT`
- `selfhosted + embedded + helper` requires `AONOHAKO_WORK_ROOT`
- every required work root must already exist, be a directory, be owned by the
  current server UID, and accept a probe directory create/remove cycle
- `embedded + helper` requires the process to be running as root

Recommended Cloud Run deployment baseline:

- `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`
- `AONOHAKO_EXECUTION_TRANSPORT=embedded`
- `AONOHAKO_SANDBOX_BACKEND=helper`
- second-generation execution environment
- service concurrency `1`
- bounded in-memory volume mounted at `AONOHAKO_WORK_ROOT`
- separate runner service account with minimal IAM permissions
- Direct VPC egress with `all-traffic`
- firewall-denied outbound traffic except for explicitly allowed destinations

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

## Runtime Image Model

Runtime images are generated from [`runtime-images.yml`](../runtime-images.yml).
One catalog drives both production images and CI smoke images.

Production profiles currently group languages like this:

| Profile | Languages |
| --- | --- |
| `type-a` | `bf`, `elixir`, `erlang`, `haskell`, `lisp`, `lua`, `ocaml`, `perl`, `php`, `plain`, `prolog`, `pypy`, `python`, `r`, `racket`, `ruby`, `sqlite`, `wasm`, `whitespace` |
| `type-b` | `clojure`, `groovy`, `java`, `javascript`, `scala`, `typescript` |
| `type-c` | `ada`, `d`, `fortran`, `go`, `nim`, `pascal`, `rust`, `zig` |
| `type-d` | `kotlin` |
| `type-e` | `csharp`, `fsharp` |
| `type-f` | `uhmlang` |
| `type-g` | `julia` |
| `type-h` | `swift` |
| `type-i` | `plain`, `python`, `java` |
| `type-j` | `coq` |
| `type-k` | `dart` |

CI mode expands the same catalog into one image per language so each smoke job
validates a single runtime in isolation. Each smoke job also runs
`scripts/report_toolchain_versions.sh`, which records toolchain versions and
Python judge-library versions in the GitHub Actions job summary.

The runtime Docker image is also hardened to reduce the readable surface for the
sandbox UID. Non-essential metadata and package-manager paths are made
root-only, while shared libraries and language runtimes remain readable so the
interpreter or binary can still start normally.

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
- regression tests for sandbox escape attempts such as network use, process
  creation, inherited-fd access, and writable scratch bypasses
- root-backed sandbox regression tests executed inside a runtime container in CI,
  with skip paths promoted to failures there

For operational use, keep architecture and security decisions aligned with the
actual code in `internal/execute`, `internal/sandbox`, and
`docker/runtime.Dockerfile`.
