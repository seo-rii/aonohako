# Architecture

## Overview

`aonohako` is the low-level compile and execute sandbox. It runs untrusted
code in isolated temporary directories with resource limits. It exposes two
SSE endpoints and a healthcheck.

```
                ┌────────────────────────────────────────┐
                │             aonohako (8080)              │
                │                                        │
  POST /compile │  ┌──────────┐     ┌─────────────────┐  │
 ──────────────>│  │ Profile  │────>│  Compile Service │  │
 <── SSE ──────│  │ Resolver │     │  (gcc/rustc/...) │  │
                │  └──────────┘     └─────────────────┘  │
                │                                        │
  POST /execute │  ┌──────────┐     ┌─────────────────┐  │
 ──────────────>│  │  Queue   │────>│ Execute Service  │  │
 <── SSE ──────│  │  Manager │     │  prlimit+taskset │  │
                │  └──────────┘     │  ┌────────────┐  │  │
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
4. Build command with `prlimit` and `taskset` wrappers
5. Start process with:
   - Isolated environment (HOME, TMPDIR scoped to workdir)
   - Thread limit environment variables
   - Network proxy blocking (if `enable_network: false`)
   - Process group isolation (`Setpgid: true`)
6. Stream image events from sidecar files during execution
7. Wait for process completion or timeout (SIGKILL on TLE)
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
| File size | `prlimit --fsize` | 32 MB |
| CPU affinity | `taskset -c 0` | Single core |
| Wall clock | Go context | Exact `time_ms` timeout |
| Threads | Environment | GOMAXPROCS=1, OMP/MKL/OpenBLAS=1 |
| Process group | `Setpgid` + SIGKILL | Kills entire group on timeout |

## Runtime Images

Runtime images are generated from [`runtime-images.yml`](../runtime-images.yml):

| Image | Languages | Purpose |
|---|---|---|
| `type-a` | `plain`, `python` | Production runtime group for native binaries plus Python `numpy` support |
| `type-b` | `java` | Production Java 17 runtime group |
| `ci-<lang>` | one language each | CI-only smoke validation image |
