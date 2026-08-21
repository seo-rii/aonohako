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
| `internal/compile` | compile request service, compiler registry, language-specific build drivers, artifact collection |
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

The compile package is split into a small service/sandbox core and a compiler
registry. `internal/compile/registry.go` maps each profile `CompileKind` to a
`Compiler` implementation, while language-family files such as
`native_compilers.go`, `jvm_compilers.go`, `dotnet_compilers.go`,
`script_compilers.go`, `toolchain_compilers.go`, `beam_compilers.go`, and
`proof_compilers.go` hold the concrete build drivers. Profile coverage tests
assert that every configured language profile has a matching registry entry.

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
11. The parent compares output, runs an SPJ, or runs an interactive interactor
    and returns the final result.

Two-step problems use the same `/execute` endpoint with `programs` and `steps`
fields instead of a separate API path. The runner executes exactly two step
requests sequentially, each in its own fresh sandbox workspace. The first step
may hand off capped stdout or one captured file to the second step stdin; no
other filesystem state is shared between the workspaces. Final output
comparison, file-output judging, sidecar capture, `ignore_tle`, and SPJ
evaluation apply only to the second step. The top-level response reports
aggregate wall/CPU time, peak memory across steps, and a per-step diagnostic
summary. Intermediate step success is exposed as the public `Accepted` status;
the internal sandbox-only `OK` marker is not part of the step response
contract.

Interactive IO problems also use `/execute`, but provide `interactor` instead
of `spj` or `programs`/`steps`. The runner starts the contestant and interactor
concurrently in separate sandbox workspaces, streams contestant stdout to
interactor stdin, and streams interactor stdout to contestant stdin. The
interactor receives read-only input and answer files through argv, plus a
writable output path for optional protocol logging. The contestant workspace
uses role UID/GID `65532`, and the trusted interactor uses role UID/GID `65531`.
Their roots stay server-owned with the selected role GID and mode `0710`, giving
that role traverse permission without giving it root-directory write
permission. This is a fixed trust-role split, not a general per-run UID
allocator. Concurrent interactive peers that need fixed runtime compatibility
state fail closed: CoreCLR still opens `/tmp/.dotnet`, but the parent exposes
that name only as a root-created link to the active peer's workspace and never
allows another sandbox command to overlap it.

Python package visibility uses the same process boundary rather than a separate
container image. Python runtime images assign site/dist-packages,
`/usr/share/python-wheels`, and `/usr/local/lib/aonohako/python` to the
root-owned supplementary GID `65530`; directories are `0750`, ordinary files
are `0640`, and executable files are `0750`. In `stdlib` mode the target does
not receive that group and Python starts through an isolated `-I -S` launcher.
The launcher restores the normal script-level `exit`, `quit`, `help`,
`copyright`, `credits`, and `license` helpers without running site
initialization, then executes the submission with its entry-point directory at
the front of `sys.path`. This blocks both normal imports and direct path reads
while preserving standard-library and submitted sibling-module imports. In
`installed` mode only Python targets receive GID `65530` and use normal site
initialization. The request-wide mode is propagated to two-step programs,
interactors, Python SPJs, and remote runners. Runtime-image permission selftests
verify that imports fail without the group and succeed with it. This is a
package-visibility boundary; enabled package code still runs under the target's
existing syscall, network, and resource limits.

## Sandbox Process Model

The runtime uses a parent/helper/target split:

1. Parent process:
   - disables its own dumpability at startup with `PR_SET_DUMPABLE=0` so
     same-container sandbox UIDs cannot use ptrace-style procfs reads against
     the long-lived API/selftest process
   - prepares the workspace
   - passes the helper request JSON through an inherited pipe file descriptor
   - for execution, creates one ready pipe and one release pipe so the helper
     cannot `execve()` the target before CPU/cgroup baselines are captured; the
     ready descriptor remains open with `CLOEXEC` so its EOF also marks the
     helper-to-target execution transition
   - opens stdout and stderr pipes
   - starts the helper in its own process group
   - kills the entire group on timeout or quota violation

2. Helper process:
   - runs from the same `aonohako` binary in internal mode
   - supports only Linux/amd64; other build targets fail closed instead of
     claiming an incomplete alternate syscall ABI policy
   - reads the helper request from the inherited pipe file descriptor
   - applies `setrlimit`
   - enables `PR_SET_DUMPABLE=0`
   - enables `PR_SET_NO_NEW_PRIVS=1`
   - installs seccomp
   - closes inherited file descriptors
   - changes directory to `box/`
   - signals the execute parent immediately before target `execve()`, then
     blocks until the parent captures process CPU and cgroup CPU/event baselines
     and sends the release byte
   - leaves the ready descriptor open and `execve()`s the target runtime or
     binary, letting close-on-exec notify the parent before target VM sampling

   `PR_SET_DUMPABLE=0` protects the helper before `execve()`. Linux resets the
   dumpable state for an ordinary target image during `execve()`, so the helper
   setting is not treated as a persistent target-to-target boundary. Interactive
   peer confidentiality instead comes from distinct UIDs/GIDs and the
   server-owned role-traversable workspace roots.

3. Target process:
   - runs with a request-specific environment built from fixed base variables,
     workspace-scoped tool/cache directories, and explicit per-runtime entries;
     it does not inherit the server process environment
   - receives `ONLINE_JUDGE=1`; languages with compiler-supported define,
     build-tag, or config flags also receive a compile-time `ONLINE_JUDGE`
     marker from the compile service
   - inherits the helper's limits and seccomp filter
   - stays in the same process group for cleanup

`/execute` requires a root parent. The parent drops ordinary and contestant
helpers/targets to UID/GID `65532`, and trusted interactive judge targets to
UID/GID `65531`. Each workspace root remains owned by the root parent, is
assigned to the selected role GID, and is set to mode `0710` before the helper
starts. The target can traverse but cannot rename root-level judge files or
workspace directories. The runtime image is hardened so only explicitly
readable paths remain accessible to either account.

The normal embedded-helper path does not materialize the helper request as a
workspace file. The parent writes the request JSON to an inherited pipe fd and
the helper consumes that fd before applying the target hardening; the legacy
request-file environment variable remains accepted only for direct helper
compatibility.

## Enforcement Layers

### Process and syscall controls

The syscall filter first requires `AUDIT_ARCH_X86_64` and rejects syscall
numbers carrying the x32 ABI bit (`0x40000000`) before any syscall-specific
rule. This prevents x32 numbers from bypassing exact native syscall matches on
kernels that enable the x32 ABI. The helper does not advertise 386 support;
legacy multiplexers such as `socketcall` and `ipc` therefore cannot fall
through an incomplete 32-bit policy.

The Linux helper applies these resource and process controls:

| Layer | Mechanism | Notes |
| --- | --- | --- |
| CPU hard limit | `RLIMIT_CPU` | helper-side hard stop |
| Address space limit | `RLIMIT_AS` | based on request memory plus language-specific virtual-memory headroom; compiled Go, Java, and .NET runtimes remain compatibility exceptions |
| Stack size | Defaults to the inherited hard stack limit, usually unlimited | avoids rejecting legacy deep-recursion submissions while memory and address-space limits still bound practical growth |
| Locked memory | `RLIMIT_MEMLOCK=0` | prevents `mlock`-style RAM pinning |
| POSIX message queue bytes | `RLIMIT_MSGQUEUE=0` | prevents message-queue allocation by the sandbox UID |
| Open files | `RLIMIT_NOFILE=64` | keeps fd surface small |
| Tasks | `RLIMIT_NPROC` | sized from current UID usage plus thread limit |
| File growth | `RLIMIT_FSIZE` | tied to workspace byte limit; .NET/Dafny get a high finite 2 TiB floor for CoreCLR/F# compatibility |
| Workspace breadth | periodic workspace scan | enforces total bytes plus entry-count and depth caps |
| Core dumps | `RLIMIT_CORE=0` | disables core files |
| Privilege escalation | `PR_SET_NO_NEW_PRIVS` | prevents gaining new privileges after exec |
| Dumpability | `PR_SET_DUMPABLE=0` | protects the long-lived parent and pre-`execve()` helper; ordinary target `execve()` may reset dumpability, so this is not the interactive peer boundary |
| FD inheritance | `CloseRange(3, ..., CLOSE_RANGE_CLOEXEC)` fallback `CloseOnExec` loop | blocks descriptor inheritance across `execve` |
| Process cleanup | `Setpgid` + group kill | kills helper and target together |

The seccomp filter denies high-risk operations, including:

- `clone3` in every profile with `ENOSYS`, so runtimes can use a legacy
  fallback without exposing an argument structure that classic BPF cannot
  inspect
- `CLONE_NEW*`, `CLONE_PARENT`, `CLONE_PTRACE`, `CLONE_UNTRACED`, and unknown
  high-word flags in classic `clone`, including process-enabled compiler
  profiles
- `fork`, `vfork`, and `clone` without `CLONE_THREAD` in execution profiles
  that do not permit child processes
- `unshare`, `setns`, `chroot`, `mount`, `pivot_root`, and newer mount APIs
  including `statmount` and `listmount`
- `ptrace`, `process_vm_*`, `process_madvise`, `process_mrelease`, `pidfd_*`
- NUMA and memory-policy syscalls such as `get_mempolicy`, `mbind`,
  `set_mempolicy`, `migrate_pages`, and `move_pages`
- `kcmp`, nested `seccomp`, Landlock policy syscalls, and LSM attribute/module
  syscalls
- `kill`, `tkill`, `tgkill`, `rt_sigqueueinfo`, `rt_tgsigqueueinfo`
- `prlimit64` except self queries with a null new-limit pointer, `setpriority`
- `bpf`, `io_uring_*`, `userfaultfd`, `memfd_create` except for .NET and
  Wasmtime runtime compatibility, memory locking, SysV shared memory,
  message-queue, and semaphore operations,
  `perf_event_open`, `cachestat`
- `open_by_handle_at`, `name_to_handle_at`, `lookup_dcookie`
- `fanotify_*`, keyring syscalls, module loading, kexec, NFS server control,
  quota control, swap, reboot, syslog
- `clock_settime`, `settimeofday`, `adjtimex`
- `personality` mutations; `personality(0xffffffff)` queries remain allowed for
  runtime compatibility
- `chmod`, `fchmodat2`, `chown`, `mknod`

The helper must allow the initial `execve()` into the requested runtime or
compiled binary. In the current denylist profile, that also leaves a post-start
`execve()` surface: normal child-process creation is blocked, so shell-spawn
patterns generally cannot fork a separate process, but a running submission can
replace itself with another world-executable binary that is present in the
runtime image. This is tracked as an image-surface risk until language-family
allowlist profiles and minimal execute-only images are available.
Runtime image hardening reduces the reachable surface where it can do so without
breaking required language tools: package-manager, fetcher, build-time
toolchain-manager, remote-access, debugger, and network-diagnostic binaries such
as `apt`, `dpkg`, `curl`, `wget`, `git`, `pip`, `npm`, `gem`, `ssh`, `rsync`,
`gdb`, `strace`, `tcpdump`, `nmap`, `dig`, `ip`, and `ping` are root-only
executable, so the sandbox UID cannot use them as post-start replacement
targets. Package-manager module directories such as Python `pip` and Node
`npm` are also root-only so submissions cannot invoke them through
interpreter-level entrypoints such as `python3 -m pip` or `node .../npm-cli.js`.
Rust toolchain shims stay executable because rustup proxy hard links are also
the `rustc` entrypoint used by the Rust compiler smoke.

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

This is paired with fail-closed deployment requirements:

- proxy-related environment variables are cleared for network-disabled requests
- outside `dev`, `AONOHAKO_ALLOW_REQUEST_NETWORK=true` also requires
  `AONOHAKO_NETWORK_EGRESS_ISOLATED=true`; this is an explicit operator
  assertion that a deny-by-default network namespace, nftables/cgroup-BPF
  policy, constrained proxy boundary, or equivalent blocks loopback, private,
  link-local, and metadata destinations before the helper starts
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

The workspace root containing these paths is server-owned, assigned to the
target role GID, and mode `0710`. `box/` remains sticky and writable inside
that role-traversable root, so a program can create ordinary output files
without gaining root-level mutation rights or making the workspace traversable
by the other interactive peer.

Environment variables redirect common runtime scratch paths into the per-run
workspace, for example `HOME`, `TMPDIR`, `JAVA_TOOL_OPTIONS`,
`XDG_CACHE_HOME`, `PIP_CACHE_DIR`, `MIX_HOME`, and `HEX_HOME`.

To avoid escaping into global writable directories, the runtime image itself
ships shared scratch paths such as `/tmp`, `/var/tmp`, and `/run/lock` with
non-writable permissions for the sandbox UID. Because container runtimes mount
`/dev/shm` and `/dev/mqueue` after image construction, the root entrypoint also
tightens those mounts to `0755`. It tightens a Cloud Run mounted
`AONOHAKO_WORK_ROOT` to mode `0711` because the in-memory volume is likewise
created after the image filesystem has already been built. Production helper
runners require that work root to be the dedicated tmpfs mount point, enforce
positive whole-filesystem byte and inode ceilings, and keep one active request.
The bounded backing filesystem is authoritative for unlinked-open files and
short write/unlink bursts; directory scanning remains the per-request
entry/depth check and byte-verdict telemetry. Image-permission
selftests scan the root filesystem device and reject every unexpected object
owned by the shared sandbox UID or GID.

### Output capture

`stdout` and `stderr` are captured through pipes into capped in-memory buffers.
The request field `limits.output_bytes` controls the judging boundary for each
stream:

- the live capture buffer size
- output-limit detection and verdicts
- step handoff data

`capture_limits.stdout_bytes` and `capture_limits.stderr_bytes` independently
clip response fields and live log events without changing that judging
boundary. The cap applies separately to stdout and stderr.

Defaults and caps:

- default: `64 KiB`
- hard judging cap: `64 MiB`
- response/log cap: `min(limits.output_bytes, 64 KiB)` by default, or up to
  `8 MiB` through the corresponding explicit `capture_limits` member
  (`0` suppresses the stream)

`/compile` and `/execute` emit buffered `log` SSE events by default for
compatibility. Setting `emit_logs=false` suppresses those events only; judging,
output-limit detection, truncation metadata, and the final `result` remain
unchanged.

Requested file outputs are validated as relative paths. At most one file output
may replace judged stdout; missing, symlinked, or non-regular outputs are
reported as runtime failure instead of silently falling back to process stdout.

## Time and Memory Accounting

`aonohako` distinguishes wall-clock time from CPU time.

| Metric | Source | Why |
| --- | --- | --- |
| `wall_time_ms` | `CLOCK_MONOTONIC` | stable wall clock, not affected by time jumps |
| `cpu_time_ms` | `CLOCK_PROCESS_CPUTIME_ID` on the target PID; self-hosted cgroup mode also uses `cpu.stat` `usage_usec` for run-cgroup CPU usage | aggregates all threads inside the process and, when cgroups are enabled, covers the run cgroup rather than only the main PID |
| `memory_kb` | `/proc/<pid>/statm` sampled during execution and `/proc/<pid>/smaps_rollup` near the limit or when AS is disabled; cgroup runs additionally use aggregate `memory.peak` | captures target RSS without charging the API server or sandbox helper, and reports the kernel aggregate peak for cgroup process trees |

Important consequence:

- multithreading is allowed
- multiprocessing is not allowed by seccomp
- because `fork`/`vfork`/`clone3` are denied and only thread-form `clone` is
  allowed, `cpu_time_ms` remains meaningful for the whole submission process

Memory enforcement uses several layers:

- live RSS sampling from `/proc/<pid>/statm`
- `/proc/<pid>/smaps_rollup` confirmation when RSS reaches 80% of the limit or when the runtime cannot use address-space limits
- cgroup `memory.peak` reporting when aggregate process-tree accounting is active;
  no-cgroup helper runs deliberately do not use process `rusage.Maxrss` because
  it includes the API server's fork-time RSS and sandbox-helper setup before the
  target `execve()`
- `RLIMIT_AS` to constrain virtual address space growth; most native programs use a tight memory-plus-slack cap, while Python/PyPy, Node, Deno, Wasmtime, and umjunsik-lang-go use higher but finite virtual caps; compiled Go binaries, Java, and .NET disable this limit because their startup reservations are not proportional to RSS
- runtime memory knobs for managed runtimes: Node receives V8 old-space, semi-space, stack, and disabled wasm trap-handler flags; Deno receives a V8 old-space cap through `--v8-flags`; Java-family launchers receive heap, stack, direct-memory, metaspace/class-space, and code-cache caps as applicable; Wasmtime receives memory-reservation, linear-memory, table, instance, and wasm-stack caps; umjunsik-lang-go receives `GOMEMLIMIT` and lower `GOGC`
- deployment-validated runtime tuning for selected JVM, Go, Erlang/Elixir,
  .NET GC, Kotlin/Native, Deno, Node, and Wasmtime
  numeric knobs, with bounded environment variables and startup rejection for
  unsafe values rather than request-controlled arbitrary runtime flags
- optional policy-owned runtime profiles from
  `AONOHAKO_RUNTIME_TUNING_PROFILES`; `/compile` and `/execute` can select a
  named profile with `runtime_profile`, but the profile can only contain the
  same bounded numeric tuning keys, requires
  `AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE=true`, and unknown names fail closed
- optional problem-owned profile mapping from
  `AONOHAKO_PROBLEM_RUNTIME_PROFILES`; a request `problem_id` can select a
  configured profile without letting the client choose arbitrary profile names,
  and conflicting direct `runtime_profile` values are rejected before queueing
- child `oom_score_adj=1000` as a best-effort fallback so the sandboxed process is preferred over the server if the host/container OOM killer has to choose
- a native-command-only post-exit address-space proximity check with slack;
  interpreter and managed runtimes rely on RSS/runtime-knob signals to avoid
  false MLE classification from large virtual-memory reservations
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
  completed sandbox setup but before the parent releases the target `execve()`;
  because the execute sandbox denies process creation and allows only
  thread-form `clone`, this includes all target threads without charging helper
  setup time
- RSS and virtual size are sampled from procfs only after the ready pipe reports
  the close-on-exec target transition and are refined with `smaps_rollup` near
  the limit or when address-space limits are disabled; cgroup runs additionally
  report aggregate `memory.peak`
- no-cgroup helper runs never use `rusage.Maxrss` for reporting or verdicts:
  `fork()` initially gives the child the API server's resident set and the same
  process then runs the sandbox helper before `execve()`, so that process-wide
  maximum cannot be attributed to submitted code
- workspace limit classification is based on periodic workspace scans for total
  bytes, entry count, and directory depth; scanner errors other than disappearing
  files fail closed as WLE so unreadable subtrees cannot hide quota usage
- when a watchdog observes TLE, MLE, or WLE, that first resource verdict kills
  the process group and is preserved through process exit
- the parent records the source for every watchdog/context `SIGKILL`; only a
  recorded wall/CPU limit or a confirmed context deadline becomes TLE, while an
  unexplained `SIGKILL` becomes `Runtime Error` with
  `verdict_source=signal_unattributed`
- after a normal `OK` resource run, non-zero exit becomes `Runtime Error`; then
  stdout/file-output or SPJ evaluation decides between `Accepted` and
  `Wrong Answer`

Local helper responses include `verdict_source` diagnostic metadata when the
runner can identify the source that selected the final status. The field is
intended for operations and judge debugging, not as a security boundary. Typical
values identify output comparison (`stdout`, `file_output`, `spj`,
`interactor`, `contestant:*`, `interactor:*`), process exit (`exit_code`,
`signal`, `signal_unattributed`), time (`wall_time`, `cpu_time`,
`cpu_time_final`, `cpu_time_cgroup`, `cpu_time_cgroup_final`, `cpu_rlimit`),
memory (`memory_rss`, `memory_cgroup`, `memory_reported`, `address_space`),
cgroup pids (`pids_cgroup`), and
workspace checks (`workspace_bytes`, `workspace_entries`, `workspace_depth`,
`workspace_scan`). This makes classification drift easier to compare across
Cloud Run helper, self-hosted helper, and self-hosted cgroup-backed runners.

Operators should compare results within one deployment profile. Cloud Run CPU
allocation, JIT warmup, GC timing, runtime memory reservations, and procfs
sampling races can still change the exact boundary between TLE, MLE, WLE, and
runtime error. Language profile multipliers in `internal/profiles` are the
explicit place to document broad runtime compensation; per-problem limits remain
the main way to absorb known JIT or GC cost.

Compile commands use the same helper process-hardening path. Because compilers
can legitimately spawn child processes, compile memory enforcement samples the
helper process tree and kills the compile sandbox when aggregate RSS exceeds the
compile sandbox memory budget. Process-enabled compilation still rejects all
namespace, parentage, tracing, and unknown high-word clone flags, and rejects
`clone3`; ordinary `fork`, `vfork`, and safe classic `clone` remain available
for compiler subprocesses. Compile watchdogs also run the shared workspace
scanner, so total bytes, entry count, directory depth limits, and fail-closed
scan-error handling apply during compile as well as execute. If
`AONOHAKO_CGROUP_PARENT` is configured for a self-hosted helper runner, compile,
execute, and SPJ helper processes are also placed into per-run cgroups with
`memory.max`, `pids.max`, `memory.swap.max=0` when supported, and
`memory.oom.group=1`. Cleanup uses `cgroup.kill` when the kernel exposes it
before retrying run-group removal, and cleanup failures are logged so leaked
cgroups are visible operationally. Execute and compile read final cgroup stats after the
sandbox process exits and before signal fallback classification, so a kernel
`memory.max` OOM kill is reported as memory-limit exceeded instead of being
misclassified as a generic `SIGKILL` timeout or runtime error.

.NET is the main compatibility exception for address-space limits, and compiled
Go binaries require the same treatment. `dotnet` invocations disable `RLIMIT_AS`
because CoreCLR reserves a very
large memfd-backed double-mapped region before user code starts. Go runtime
startup likewise makes large, ASLR-sensitive virtual reservations before
`main`, so `go-binary` execution disables `RLIMIT_AS`. Their physical memory is
still enforced through RSS/smaps sampling, mandatory self-hosted cgroup
`memory.max`, and the outer container limit. Lower file-size
rlimits can also break CoreCLR/F# startup, so `dotnet` and `dafny` receive a
high finite 2 TiB `RLIMIT_FSIZE` floor instead of disabling the file-size rlimit
entirely. That floor is a compatibility guard, not the effective workspace
quota: .NET/Dafny disk burst protection still comes from workspace scanning,
bounded `AONOHAKO_WORK_ROOT` storage, mandatory self-hosted cgroups, and the
outer container or filesystem limit. The helper still applies a request-memory-derived
`DOTNET_GCHeapHardLimit`, RSS watchdogs, workspace byte accounting, output caps,
open-file limits, thread limits, OOM-victim preference, and single-slot
execution. The image keeps `/tmp/.dotnet` root-owned and non-traversable. For
each sandboxed `dotnet` invocation, the runner temporarily replaces that name
with a root-created symlink to a `0700`, sandbox-owned directory inside the
active workspace, then restores a root-owned sealed directory after the whole
sandbox command and its cgroup cleanup finish. Kotlin/Native's global cache
stays root-owned and read-only; its compatibility lock is similarly linked to
a per-compile workspace file only for the command lifetime. A process-wide
runtime-state lease prevents another sandbox command from overlapping either
fixed path.

Self-hosted cgroup support is gated by an explicit preflight layer and an
operator-selected parent cgroup. `internal/isolation/cgroup` checks for a cgroup
v2 mount, `cgroup.controllers`, `cgroup.subtree_control`, and the required
`cpu`, `memory`, and `pids` controllers. The `io` controller is reported
separately because it is useful for future throttling but not required for the
first hard memory/process boundary. `AONOHAKO_CGROUP_PARENT` is required for
`selfhosted + embedded + helper` and rejected for other shapes. Startup
validates the selected
parent is under a cgroup v2 mount, is not group/world writable, and accepts a
probe run-group create/remove cycle before request handling. The probe uses the
same `memory.max`, `memory.swap.max`, `memory.oom.group`, `pids.max`, and
`cpu.max` write path as real runs. It also requires the parent `cgroup.procs` to be empty and writes the requested controllers to `cgroup.subtree_control` at startup,
so delegation failures are reported before the runner accepts work.

The same package owns the low-level run-group write contract used by the
self-hosted cgroup guardrail and the future isolated backend. Parent cgroups enable
child controllers by writing values such as `+cpu +memory +pids` to
`cgroup.subtree_control`. A run group must then be created with positive
`memory.max` and `pids.max` values. When the kernel exposes `memory.swap.max`,
the guardrail writes `0` so swap cannot extend the run's effective memory
budget, and `memory.oom.group` is set so the kernel treats the run as one OOM
domain. Compile, execute, and SPJ run groups also write
`cpu.max=100000 100000` so one self-hosted sandbox cannot burst beyond one
vCPU. A target process is admitted by
writing its PID to `cgroup.procs`. Cleanup removes the run cgroup directory
without recursive deletion so leftover processes or files surface as cleanup
errors, with a short retry window for kernel-side process cleanup races.

The accounting reader reads `memory.current`, `memory.peak` when present,
`memory.events`, `pids.current`, `pids.events`, and `cpu.stat`. When a run
cgroup is present, watchdogs prefer `memory.events` `max`, `oom`, `oom_kill`,
and `oom_group_kill`, plus `pids.events` `max`, over RSS polling for hard memory
and pids-limit classification. Final stats are read before cleanup so late
kernel OOM/pids events that beat the watchdog tick still take priority over
signal-based fallback classification. Execute watchdogs also use `cpu.stat`
`usage_usec` to update reported CPU time and classify CPU-time TLE for the whole
run cgroup. CPU throttling counters remain diagnostic; the current cgroup CPU
setting is a bandwidth guardrail, not a separate verdict classification source.

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
  with a dedicated bounded tmpfs work-root mount
- `dev + remote + none`: supported non-root control-plane target that forwards
  `/compile` and `/execute` to another runner

`embedded + container` is reserved for a future self-hosted backend and is
currently rejected at startup.

The supported shapes map to an explicit runtime security contract in
`internal/platform`:

| Shape | Contract | Local guarantees | Missing local boundary |
| --- | --- | --- | --- |
| `embedded + helper` | `embedded-helper-process-hardening` | root parent with dropped UID child, a dedicated bounded tmpfs work-root mount and single active request, server-owned role-traversable workspace roots, distinct fixed contestant/trusted-interactor identities for interactive requests, `setrlimit`, `PR_SET_NO_NEW_PRIVS`, seccomp denylist, network syscall gate, fd cleanup, process-group cleanup, immutable submissions, symlink-safe output capture, workspace accounting; self-hosted deployments require per-run cgroup and child-process accounting through `AONOHAKO_CGROUP_PARENT`, while Cloud Run delegates that final aggregate boundary to its disposable outer container | mount namespace, read-only rootfs, masked `/proc`, per-request UID allocation or user namespace, seccomp allowlist, post-start `execve()` blocking; Cloud Run helper mode has no child cgroup control |
| `remote + none` | `remote-control-plane` | `/compile` and `/execute` are forwarded to the configured runner and no local untrusted compile/run work is performed | isolation is delegated to the downstream runner and its private ingress/auth boundary |
| `embedded + container` | `reserved-container-isolation` | not implemented | must provide per-run cgroup, mount namespace, read-only rootfs, masked `/proc`, per-run UID or user namespace, child-process accounting, allowlist-oriented seccomp, and post-start `execve()` blocking before it can be enabled |

The future mount-isolated backend is still unavailable, but
`aonohako-selftest mount-preflight` can probe self-hosted runner hosts without
mutating the parent process. It starts a child process and verifies
`unshare(CLONE_NEWNS)`, private mount propagation, a bounded tmpfs mount, a
procfs mount with `hidepid=2`, and a read-only bind remount. This is a
prerequisite check only; a successful result does not add mount namespace,
read-only rootfs, or masked `/proc` capabilities to the current helper backend.

Server startup validates the deployment contract instead of trusting docs alone.
The following checks are enforced before the HTTP server starts:

- Cloud Run marker envs require `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`
- unsupported runtime security contracts fail startup before request handling
- `remote` transport requires `AONOHAKO_REMOTE_RUNNER_URL`
- `AONOHAKO_REMOTE_RUNNER_AUTH=none` is rejected outside `dev`
- authenticated remote runner URLs must use HTTPS outside `dev`
- `remote + bearer` requires `AONOHAKO_REMOTE_RUNNER_TOKEN`
- `remote + cloudrun-idtoken` defaults its audience to the remote runner URL if
  `AONOHAKO_REMOTE_RUNNER_AUDIENCE` is unset
- remote runner SSE responses are parsed with bounded line, event, and stream
  sizes, and the remote HTTP transport sets dial, TLS handshake, response
  header, request-upload, idle connection, and SSE idle heartbeat timeouts;
  successful responses must use the exact `text/event-stream` media type, and
  endpoint URLs with a trailing slash are normalized without duplicating the
  `/compile` or `/execute` path
- Cloud Run identity-token metadata requests use a dedicated HTTP transport
  that never consults process proxy environment variables
- remote runner protocol-version headers fail closed when missing or unsupported
  in strict mode; `dev` can opt into the backward-compatible policy that accepts
  missing headers while still rejecting unsupported present values
- malformed remote `log`, `image`, `error`, or `result` events fail the remote
  request as a protocol error instead of being silently ignored
- when a control plane maps `problem_id` to `runtime_profile` and forwards the
  selected profile, downstream remote runners must receive the same
  `AONOHAKO_RUNTIME_TUNING_PROFILES` and `AONOHAKO_PROBLEM_RUNTIME_PROFILES`
  config, or must be otherwise configured as a trusted internal boundary that
  accepts those policy-selected profiles
- inbound `/compile` and `/execute` authentication defaults to bearer tokens
  outside `dev`; `AONOHAKO_INBOUND_AUTH=platform` must be explicit when an
  upstream platform layer owns inbound authentication
- `AONOHAKO_INBOUND_AUTH=none` is rejected outside `dev`
- `AONOHAKO_INBOUND_AUTH=platform` outside `dev` requires
  `AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET`; the application verifies
  `X-Aonohako-Principal-Signature` and
  `X-Aonohako-Principal-Timestamp` over the request method, request URI
  including query string, principal, RFC3339 timestamp, a fresh 128-bit
  `X-Aonohako-Principal-Nonce`, and the SHA-256 request-body digest with a
  five-minute validity window; a bounded per-principal replay cache rejects
  nonce reuse and fails closed at capacity, so unsigned trusted platform
  headers are not accepted outside `dev`; legacy replayable signatures are
  rejected
- every request to `/compile` or `/execute` acquires
  `AONOHAKO_MAX_ACTIVE_UPLOADS` and claimed-principal
  `AONOHAKO_MAX_PRINCIPAL_ACTIVE_UPLOADS` admission before authentication can
  read a signed body, JSON decoding, Base64 validation, or payload URL fetches;
  the upload admission is released after bounded queue admission or any early
  failure
- platform body hashing happens before principal rate limiting, so concurrent
  hash operations are additionally capped by
  `AONOHAKO_PLATFORM_BODY_HASH_CONCURRENCY`
- `AONOHAKO_TRUSTED_PLATFORM_HEADERS=true` and
  `AONOHAKO_PLATFORM_TRUSTED_PROXY_CIDRS` remain optional defense-in-depth
  assertions for source-CIDR checks in addition to signed platform principals
- numeric values such as `AONOHAKO_MAX_ACTIVE_RUNS`,
  `AONOHAKO_MAX_PENDING_QUEUE`, `AONOHAKO_MAX_ACTIVE_STREAMS`,
  `AONOHAKO_MAX_ACTIVE_UPLOADS`,
  `AONOHAKO_MAX_PRINCIPAL_ACTIVE_UPLOADS`,
  `AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS`,
  `AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE`, and
  `AONOHAKO_HEARTBEAT_INTERVAL_SEC`, and
  `AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC` are strict; malformed or out-of-range
  values fail startup
- `AONOHAKO_REMOTE_STRICT_PROTOCOL` is a strict boolean; it defaults to `true`
  outside `dev` so remote responses without `X-Aonohako-Protocol-Version` are
  rejected in production remote fleets
- non-dev deployments also reject `0` for pending queue, global and
  per-principal upload, global stream, and per-principal stream caps so
  unlimited queue, upload, or open-stream settings stay development-only
- `AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE=0` is accepted on every
  deployment target and disables the process-local fixed-window rate limiter.
  This is intended for trusted Cloud Run or self-hosted runners whose
  concurrency and fleet capacity are bounded at the deployment layer
- `AONOHAKO_ALLOW_REQUEST_NETWORK` is strict boolean configuration and defaults
  to `true` only for `dev`; outside `dev`, client-supplied `enable_network=true`
  is rejected unless this is explicitly enabled for a dedicated runner policy,
  and startup requires `AONOHAKO_NETWORK_EGRESS_ISOLATED=true`; Cloud Run
  embedded helpers reject the opt-in even with that assertion
- `AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE` is strict boolean configuration and
  defaults to `true` only for `dev`; outside `dev`, request-supplied
  `runtime_profile` is rejected unless an upstream trusted control plane has
  mapped the problem to an operator-owned runtime tuning profile
- `AONOHAKO_PROBLEM_RUNTIME_PROFILES` maps `problem_id` values to bounded
  runtime tuning profile names and is validated at startup so unknown profile
  names fail closed
- `AONOHAKO_TRUSTED_RUNNER_INGRESS=true` is required for non-dev
  `embedded + helper` runners, forcing operators to assert a trusted/private
  ingress or platform-auth boundary before starting a root helper parent
- `cloudrun` always requires `AONOHAKO_WORK_ROOT`
- `selfhosted + embedded + helper` requires `AONOHAKO_WORK_ROOT`
- `selfhosted + embedded + helper` requires `AONOHAKO_CGROUP_PARENT`; startup
  validates and probes its delegated CPU, memory, and pids controllers
- every required work root must already exist, be a directory, be owned by the
  current server UID, not be group/world writable, and accept a probe
  directory create/remove cycle
- production `embedded + helper` requires
  `AONOHAKO_REQUIRE_WORK_ROOT_TMPFS=true` and verifies through
  `/proc/self/mountinfo` that the required work root is the exact tmpfs mount
  point, not a subdirectory on a shared tmpfs
- production `embedded + helper` requires
  `AONOHAKO_WORK_ROOT_MAX_BYTES` between 1 and 1 GiB and verifies through
  `statfs` that the entire work-root filesystem fits the configured ceiling
- production `embedded + helper` requires
  `AONOHAKO_WORK_ROOT_MAX_FILES` between 1 and 1048576 and verifies through
  `statfs` that the entire work-root filesystem fits the inode ceiling
- the dedicated Cloud Run communication runner permits
  `AONOHAKO_WORK_ROOT_MAX_FILES` up to 4194304 because its 32 GiB instance
  advertises about 4.1 million tmpfs inodes; ordinary runners retain the
  1048576 ceiling, while the single communication session still enforces the
  8192-entry scan limit independently on each of its 65 workspaces
- `embedded + helper` requires the process to be running as root
- `embedded + helper` also requires `AONOHAKO_MAX_ACTIVE_RUNS=1` so helper
  executions do not overlap under the shared sandbox UID
- communication-capable embedded helpers reserve UID/GID `65531` for the
  manager and `64937..65000` for participants; both Cloud Run helpers and
  self-hosted cgroup helpers fail startup if those identities are assigned,
  active, or own objects on the runtime image filesystem
- Cloud Run advertises that capability only when the dedicated service sets
  `AONOHAKO_COMMUNICATION_ENABLED=true`; capable Cloud Run services also require
  a positive memory budget, an explicit CPU count matching `GOMAXPROCS`, and a
  positive end-to-end communication wall budget
- `/compile` and `/execute` streams are capped globally, and outside `dev` they
  are also capped per principal. Bearer auth uses a token fingerprint as the
  principal key; platform auth uses the upstream principal header such as
  `X-Aonohako-Principal`. Platform auth ignores generic forwarded identity
  headers. With `AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET`, the application
  verifies the principal signature itself over method, request URI, principal,
  timestamp, and body digest; outside `dev`, startup rejects platform auth
  unless that signing secret is configured.
- By default outside `dev`, `/compile` and `/execute` requests are also capped
  per principal in a process-local fixed one-minute window before they enter
  the run queue. Trusted runners may set
  `AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE=0` when deployment-layer
  concurrency and fleet limits provide the capacity boundary.
- `aonohako-selftest deployment-contract` reports the active execution shape,
  named contract, whether that contract is implemented, effective and missing
  local capabilities, auth posture, queue/stream limits, cgroup parent presence,
  and `AONOHAKO_REQUIRE_WORK_ROOT_TMPFS` /
  `AONOHAKO_WORK_ROOT_MAX_BYTES` / `AONOHAKO_WORK_ROOT_MAX_FILES` state as JSON
  for deployment checks.

`/livez` is a process-liveness signal and remains healthy while the server can
serve HTTP. `/readyz` rechecks the mandatory dedicated work-root identity,
permissions and configured filesystem bounds, plus delegated cgroup
controllers and parent emptiness. It fails with `503` if those runtime
preconditions disappear. `/healthz` is retained as a readiness alias for
existing deployments.

Recommended Cloud Run deployment baseline:

- `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`
- `AONOHAKO_EXECUTION_TRANSPORT=embedded`
- `AONOHAKO_SANDBOX_BACKEND=helper`
- `AONOHAKO_API_BEARER_TOKEN` set to a strong secret, unless
  `AONOHAKO_INBOUND_AUTH=platform` is set because Cloud Run IAM, mTLS, private
  ingress, or a gateway enforces inbound authentication; use
  `AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET` for platform auth outside `dev`.
- second-generation execution environment
- service concurrency `1`
- bounded in-memory volume mounted at `AONOHAKO_WORK_ROOT`, optionally asserted
  with `AONOHAKO_REQUIRE_WORK_ROOT_TMPFS=true` and
  `AONOHAKO_WORK_ROOT_MAX_BYTES` / `AONOHAKO_WORK_ROOT_MAX_FILES`
- container memory sized above the work-root byte budget plus runtime headroom,
  because Cloud Run/no-cgroup runners rely on the outer container limit as the
  final OOM boundary
- separate runner service account with minimal IAM permissions
- Direct VPC egress with `all-traffic`
- firewall-denied outbound traffic except for explicitly allowed destinations

For a Cloud Run API/control-plane service that forwards `/compile` and
`/execute` to a separate runner, use `cloudrun + remote` with Cloud Run
identity-token authentication, the same bounded `AONOHAKO_WORK_ROOT`
requirement, and a private HTTPS `AONOHAKO_REMOTE_RUNNER_URL`.

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
- the Cloud Run runtime does not depend on child cgroup creation; self-hosted
  helpers may opt into it with `AONOHAKO_CGROUP_PARENT`
- `communication-v1` therefore uses per-process helper limits and process-group
  cleanup on Cloud Run, while the outer instance supplies aggregate memory,
  CPU, PID, and final-kill behavior; self-hosted communication retains its
  aggregate cgroup
- communication targets use a server-selected native-binary profile, cannot
  create processes or threads, and receive separate immutable artifact inodes;
  the one-GiB Cloud Run work root is statically admitted with 8 MiB per
  participant, 128 MiB for the manager, and a 20% reserve
- the runtime does not depend on mount-based filesystem isolation
- the runtime does not assume Landlock availability
- Cloud Run marker env vars alone do not switch security policy; the deployment
  target is explicit to avoid accidental partial hardening
- the helper backend intentionally serializes active requests because ordinary
  executions across requests reuse UID/GID `65532`; the fixed `65532`/`65531`
  split only separates contestant and trusted-interactor roles within one
  interactive request

## Self-Hosted Scale Path

`selfhosted + embedded + helper` is supported, but it deliberately keeps one
active execution per instance. The helper backend drops targets to a shared
sandbox UID across requests and depends on a dedicated work root plus immutable
submitted files, so startup rejects `AONOHAKO_MAX_ACTIVE_RUNS` values other
than `1`. Interactive peers are the narrow exception: contestant and trusted
interactor use distinct fixed identities and server-owned, role-traversable
workspace roots.

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
Catalog `shared_installs` may be referenced by profiles, languages, or other
shared blocks; dependencies are validated as acyclic and each shared block is
expanded only once per generated image.

Production profiles currently group languages like this:

| Profile | Languages |
| --- | --- |
| `type-a` | `aheui`, `apecode`, `apl`, `awk`, `bc`, `befunge`, `bf`, `bqn`, `elixir`, `erlang`, `forth`, `gforth`, `gleam`, `golfscript`, `haskell`, `idris2`, `j`, `janet`, `lisp`, `lolcode`, `lua`, `malbolge`, `mercury`, `ocaml`, `perl`, `php`, `picolisp`, `plain`, `prolog`, `pypy`, `r`, `racket`, `raku`, `ruby`, `scheme`, `sed`, `smalltalk`, `sml`, `sqlite`, `tcl`, `uiua`, `wasm`, `whitespace` |
| `type-b` | `clojure`, `coffeescript`, `deno`, `elm`, `graphql`, `groovy`, `haxe`, `java`, `javascript`, `purescript`, `rescript`, `scala`, `typescript` |
| `type-c` | `ada`, `asm`, `c3`, `classic-basic`, `cobol`, `crystal`, `cython`, `d`, `delphi`, `fortran`, `freebasic`, `gnucobol`, `go`, `hare`, `mojo`, `nasm`, `nim`, `objective-c`, `objective-cpp`, `objectpascal`, `odin`, `pascal`, `qbasic`, `rust`, `vala`, `vlang`, `zerolang`, `zig` |
| `type-d` | `kotlin`, `kotlin-jvm` |
| `type-e` | `csharp`, `fsharp`, `vbnet` |
| `type-f` | `uhmlang` |
| `type-g` | `julia` |
| `type-h` | `swift` |
| `type-i` | `c`, `cpp`, `java`, `plain`, `pypy`, `python` |
| `type-j` | `agda`, `coq`, `rocq`, `tla`, `why3` |
| `type-k` | `dart` |
| `type-l` | `python` |
| `type-m` | `duckdb`, `gdl`, `octave` |
| `type-n` | `systemverilog`, `verilog`, `vhdl` |
| `type-o` | `cuda-ocelot` |
| `type-p` | `carbon`, `vb6` |
| `type-q` | `dafny` |
| `type-r` | `isabelle` |
| `type-s` | `lean4` |

The `MALBOLGE` profile uses `.mal` as its canonical extension and accepts
`.mb` for compatibility. Compile strips ASCII whitespace, validates every
remaining byte against the position-dependent reference opcode translation,
and enforces the fixed 59049-word memory limit before passing source artifacts
through. The bundled interpreter follows the original public-domain
interpreter's practical I/O mapping (`<` writes and `/` reads, with EOF represented
as 59048), while malformed non-graphical runtime memory terminates safely
instead of reproducing the reference C implementation's undefined access or
busy loop.

CI mode expands the same catalog into one image per language so each smoke job
validates a single runtime in isolation. A dedicated CI summary job builds the
production profiles in a parallel matrix and runs
`scripts/report_toolchain_versions.sh` once per profile. The generated fragment
contains the compiler/runtime version table and a language-specific compile
options table so CI summaries show both the installed toolchain and the flags or
compile pipeline used by the service. Each matrix leg tries to create a
`docker save` archive and SHA256 sidecar in workflows that enable archive
export. The current CI workflow skips that export to conserve runner storage and
writes a profile-bound archive diagnostic JSON instead.
It then uploads its summary fragment, Syft SBOM JSON, Grype JSON scan, and image
archive diagnostic as artifacts. Syft emits the published SPDX document and a
temporary native catalog from one image traversal; Grype consumes that catalog
instead of exporting the same Docker image a second time. The native catalog
preserves the immutable image metadata used by Grype and is deleted before
artifact upload. Syft and Grype operational failures fail the
profile matrix leg instead of being replaced by successful-looking JSON
sentinels. The smaller root-backed Python runtime additionally rejects fixable
High or Critical findings, while the heterogeneous production profiles retain
their reports for profile-specific distro and bundled-toolchain upgrade review.
The workflow prunes the active Buildx cache, Go caches, scanner temp/cache
directories, and the local image before the Grype catalog scan. The summary
verifier also fails closed on missing or
semantically incomplete SPDX/Grype JSON, failed toolchain version probes,
missing required runtime probes, malformed archive diagnostics, an incomplete
production-profile inventory, and missing or digest-mismatched archives. The
expected profile and per-profile language inventories come directly from the
production runtime matrix. Every profile summary, SPDX document, and Grype
report must identify the exact `aonohako-ci-prod:<profile>` image; SPDX evidence
must be a populated SPDX 2.3 document produced by the pinned Syft version, and
Grype evidence must contain the pinned scanner descriptor, image source, distro
metadata, and structurally valid matches. A per-profile provenance record binds
the summary and both scanner reports by SHA256 to the same immutable Docker
image ID, which must also match Grype's source metadata. The consolidated
summary must exactly match a fresh aggregation of all verified profile
fragments, including versions, compile options, and profile attribution.

A final CI summary job downloads those artifacts, concatenates the per-profile
reports into one GitHub Actions summary, and republishes the summaries,
SPDX/Grype evidence, and archives or archive diagnostics as a single bundle
artifact. Artifact-download failure or an empty profile directory set fails
that job. Its sorted manifest is generated from exactly that upload inventory,
and consolidated `SHA256SUMS` paths and per-archive sidecars use canonical paths
relative to the bundle root. Because archive export is skipped in the default
CI workflow, `SHA256SUMS` is empty unless a workflow variant produces real
`.docker.tar.gz` archives; profiles with archive diagnostic JSON do not provide
promotion-ready image bytes from that CI run.

Debian-based production profiles now use a digest-pinned
`debian:trixie-slim` base, which raises the baseline Python, PyPy, and GCC
versions seen by both production and CI runtime images while keeping base-image
drift explicit in review. The Go builder image and non-Debian profile bases are
also digest pinned. Python judge libraries are pinned in the catalog so rebuilds
stay reproducible. Deployments that need site-specific Python helpers can pass
a custom package directory at image build time; its contents are copied into
`/usr/local/lib/aonohako/python`, which is exported as `PYTHONPATH`.
Catalog profile, language, and shared-install names are restricted to safe
identifiers, duplicate languages within a profile are rejected, and the runtime
builder refuses any catalog `base_image` that is not pinned by SHA256 digest.

The runtime Docker image is also hardened to reduce the readable surface for the
sandbox UID. Non-essential metadata and package-manager paths are made
root-only, including identity metadata such as `/etc/passwd`, `/etc/group`, and
package database paths, and package-manager module entrypoint directories such
as Python `pip` and Node `npm`, while shared libraries and language runtimes
remain readable so the interpreter or binary can still start normally.
Runtime-mounted host files such as `/etc/hostname` and `/etc/hosts` are not
image-hardened and remain part of the mount isolation backlog.

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
- Syft SBOM generation in CI for both the root-backed Python runtime image used
  by sandbox regression tests and every production runtime profile artifact
- fail-closed Grype execution in CI for the sandbox runtime image and every
  production runtime profile, with scanner errors failing the corresponding job
  while reports are retained for diagnosis; the sandbox runtime also rejects
  fixable High or Critical findings
- a fail-closed production profile artifact verification step that requires the
  SBOM JSON, Grype JSON, and toolchain summary to be semantically complete;
  requires either an image archive with consistent sidecar and consolidated
  `SHA256SUMS` entries or a valid profile-bound skip diagnostic; and requires
  the expected production-profile inventory and sorted manifest to match the
  uploaded bundle exactly, including the verified SPDX and Grype evidence
- repository policy checks that require Dockerfile base images to be
  digest-pinned or routed through digest-pinned build arguments; the policy
  strips one leading UTF-8 BOM and rejects heredoc instructions rather than
  interpreting heredoc bodies as Dockerfile stages
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
