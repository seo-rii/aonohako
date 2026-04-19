# Architecture

## Overview

`aonohako` is the low-level compile and execute sandbox. It runs untrusted
code in isolated temporary directories with resource limits. It exposes two
SSE endpoints and a healthcheck. The supported production security target is a
Cloud Run deployment using the hardened runtime images. Local non-root runs are
kept as a development path and do not claim the same filesystem isolation.

```
                ┌────────────────────────────────────────┐
                │             aonohako (8080)              │
                │                                        │
  POST /compile │  ┌──────────┐     ┌─────────────────┐  │
 ──────────────>│  │ Profile  │────>│  Compile Service │  │
 <── SSE ──────│  │ Resolver │     │  (gcc/rustc/...) │  │
                │  └──────────┘     └─────────────────┘  │
                │                                        │
  POST /execute │  ┌──────────┐     ┌────────────────────────────┐  │
 ──────────────>│  │  Queue   │────>│ Execute Service             │  │
 <── SSE ──────│  │  Manager │     │  prlimit+taskset+runner     │  │
                │  └──────────┘     │  ┌────────────┐  │  │
                │                   │  │ helper     │  │  │
                │                   │  │ hardening  │  │  │
                │                   │  │ cleanup    │  │  │
                │                   │  └────────────┘  │  │
                │                   │  ┌────────────┐  │  │
                │                   │  │ Comparator │  │  │
                │                   │  └────────────┘  │  │
                │                   │  ┌────────────┐  │  │
                │                   │  │    SPJ     │  │  │
                │                   │  └────────────┘  │  │
                │                   └─────────────────┘  │
                └────────────────────────────────────────┘
```

## Packages

| Package | Responsibility |
|---|---|
| `cmd/server` | HTTP server entry point |
| `internal/api` | HTTP routing, request decoding, SSE setup, queue gating |
| `internal/compile` | Language-specific build drivers and artifact collection |
| `internal/config` | Environment variable parsing with defaults |
| `internal/execute` | Sandboxed process execution, output comparison, SPJ |
| `internal/model` | Shared request/response types and status constants |
| `internal/profiles` | Compile/run language profile registry |
| `internal/queue` | Bounded FIFO concurrency queue with permit-based flow |
| `internal/security` | Thread-limit and workspace-scoped environment setup |
| `internal/sse` | Thread-safe SSE writer with heartbeat |
| `internal/util` | Base64, path validation, file materialization |

## Compile Flow

1. Decode `CompileRequest` → resolve language profile
2. Create temp workdir, write source files
3. Execute compiler (gcc, g++, rustc, javac, etc.) with 60s timeout
4. Collect output artifacts (binaries, .class files, .pyc, etc.)
5. Return `CompileResponse` with base64-encoded artifacts
6. Clean up temp workdir

## Execute Flow

1. Decode `RunRequest` → acquire queue permit (429 on overflow)
2. Create temp workdir with sandbox subdirectories
3. Write binaries and set permissions
4. Build helper request with command, env, limits, network flag, and thread cap
5. Start process with:
   - `prlimit` CPU / address-space / open-file / file-size limits
   - `RLIMIT_NPROC` thread/process cap
   - Thread limit environment variables
   - Process group isolation (`Setpgid: true`)
   - Immutable submitted files plus writable workspace directories
   - A low-privilege child credential when the parent runs as root
   - `PR_SET_NO_NEW_PRIVS`, seccomp, and fd cleanup in the helper
6. Stream image events from sidecar files during execution
7. Wait for process completion or timeout, then kill the whole process group
8. Compare output (built-in diff or SPJ)
9. Capture file outputs and sidecar outputs
10. Return `RunResponse` with verdict and metrics

## Queue System

The queue provides bounded concurrency for `/execute`:

```
┌─────────────────────────────────────────────────┐
│                Queue Manager                     │
│                                                  │
│  Active Slots (AONOHAKO_MAX_ACTIVE_RUNS)         │
│  ┌──────┐ ┌──────┐ ┌──────┐                     │
│  │ Run1 │ │ Run2 │ │ Run3 │  ← executing now    │
│  └──────┘ └──────┘ └──────┘                     │
│                                                  │
│  Pending Queue (AONOHAKO_MAX_PENDING_QUEUE)      │
│  ┌──────┐ ┌──────┐                              │
│  │ Wait │ │ Wait │  ← blocked on Permit.Wait()  │
│  └──────┘ └──────┘                              │
│                                                  │
│  New request when pending full → 429 queue_full  │
└─────────────────────────────────────────────────┘
```

- Permits are granted in FIFO order
- Releasing a permit immediately unblocks the next waiter
- Context cancellation removes the waiter from the queue

## Resource Enforcement

| Layer | Mechanism | Limits |
|---|---|---|
| CPU time | `prlimit --cpu` | `ceil(time_ms/1000) + 1` seconds |
| Address space | `prlimit --as` | `memory_mb + 64` MB (min 256 MB) |
| Open files | `prlimit --nofile` | 64 |
| Threads / processes | `prlimit --nproc` | 512 |
| File size | `prlimit --fsize` | 32 MB |
| Filesystem view | Runtime image permissions + workspace root | Cloud Run image is the trusted boundary |
| Existing submission files | Read-only file mode | Cannot overwrite original files |
| Writable paths | Per-run workspace dirs | New files only in `box/` and cache/tmp/home sidecars |
| Devices | Cloud Run device restrictions + runtime image permissions | No host device nodes such as `/dev/kmsg` |
| Network | Seccomp socket filtering + deployment-level egress policy | Disabled unless explicitly allowed |
| Wall clock | `CLOCK_MONOTONIC` + Go context | Exact `time_ms` timeout and `wall_time_ms` reporting |
| Reported CPU time | Linux process CPU clock | `cpu_time_ms` |
| Output capture | In-memory capped buffers | `limits.output_bytes`, default 64 KiB, hard cap 8 MiB |
| Threads | Environment + `RLIMIT_NPROC` | GOMAXPROCS=1, OMP/MKL/OpenBLAS=1, max 512 tasks |
| Process group | `Setpgid` + SIGKILL | Kills entire group on timeout |

## Cloud Run Notes

Cloud Run cannot rely on arbitrary in-container mount operations for sandboxing,
so `aonohako` does not attempt child cgroup creation or in-container bind-mount
management there.

- Use Cloud Run second generation.
- Set service concurrency to `1`.
- Mount a bounded in-memory volume and point `AONOHAKO_WORK_ROOT` at it.
- Keep the service account minimal because untrusted code can access the Cloud
  Run metadata server if deployment networking allows it.
- Force outbound traffic through Direct VPC egress with `all-traffic` and deny
  outbound access by firewall policy except for explicitly allowed targets.
- Treat the runtime image plus Cloud Run instance boundary as the filesystem
  trust boundary for production.

## Local Development Mode

When `aonohako` runs outside a root-owned runtime container, it still applies
CPU, memory, output, file-size, fd, seccomp, and process-group controls, but it
does not claim the same filesystem isolation as the Cloud Run deployment mode.

- Use local non-root runs for development and smoke tests.
- Use the runtime image on Cloud Run when you need the intended security
  boundary.

## Runtime Images

Runtime images are generated from [`runtime-images.yml`](../runtime-images.yml):

| Image | Languages | Purpose |
|---|---|---|
| `type-a` | `plain`, `python` | Production runtime group for native binaries plus Python `numpy` support |
| `type-b` | `java` | Production Java 17 runtime group |
| `ci-<lang>` | one language each | CI-only smoke validation image |
