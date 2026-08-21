# Self-Hosted Topologies

`aonohako` supports self-hosted deployments, but the safe topology is different
from the Cloud Run production baseline.

This document focuses on how to run the service outside Cloud Run without
relaxing the helper sandbox contract.

## Supported shapes today

### Local or VM/container runner with embedded helper

Use this when the same process is expected to execute submissions directly:

- `AONOHAKO_DEPLOYMENT_TARGET=selfhosted`
- `AONOHAKO_EXECUTION_TRANSPORT=embedded`
- `AONOHAKO_SANDBOX_BACKEND=helper`
- `AONOHAKO_WORK_ROOT=/work`
- `AONOHAKO_REQUIRE_WORK_ROOT_TMPFS=true`
- `AONOHAKO_WORK_ROOT_MAX_BYTES=1073741824`
- `AONOHAKO_WORK_ROOT_MAX_FILES=131072`
- `AONOHAKO_CGROUP_PARENT=/sys/fs/cgroup/aonohako`
- `AONOHAKO_MAX_ACTIVE_RUNS=1`
- `AONOHAKO_TRUSTED_RUNNER_INGRESS=true`
- `AONOHAKO_API_BEARER_TOKEN` set to a strong secret, or
  `AONOHAKO_INBOUND_AUTH=platform` when private ingress, mTLS, or a gateway
  authenticates inbound calls
- `AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET` when using platform auth outside
  `dev`
- root parent process

This shape is supported for:

- local debugging
- a dedicated single-tenant runner VM
- a single active runner container behind a queue
- `communication-v1`, when the cgroup parent is configured and the host reserves
  numeric UID/GID 65531 for the trusted manager and 64937–65000 for participants

It is intentionally serialized. The helper backend drops the target process to a
shared sandbox UID and relies on a dedicated work root plus immutable submitted
files. Running more than one active helper-backed execution in the same process
would weaken ownership isolation, so startup rejects values other than `1`.
Within one interactive request only, the contestant and trusted interactor use
fixed distinct UID/GID pairs. Each root stays server-owned with mode `0710` and
group traverse permission for its selected role; this does not provide
per-request UID allocation for increasing runner concurrency.

Within one communication request, every participant instead receives a distinct
UID/GID and writable workspace while all participant workspaces hard-link the
same read-only artifacts. Only the manager inherits the participant pipes and
private test-data paths. The helper seccomp policy keeps network, process
creation, and ptrace unavailable to the native participant programs. The
aggregate cgroup owns the manager and all participant subgroups so cancellation
and resource-limit cleanup kill the whole process tree. These guarantees assume
a dedicated runner where the reserved numeric identities are not assigned to
host accounts; communication capability must not be exposed from a shared host.
Startup fails closed if those identities appear in `/etc/passwd`, `/etc/group`,
an existing process credential, or ownership metadata on the runner image's root
filesystem. The direct-image self-test applies the same ownership check.

### Non-root control plane with remote execution

Use this when the local service should stay non-root and must not build or run
untrusted submissions itself:

- `AONOHAKO_DEPLOYMENT_TARGET=dev`
- `AONOHAKO_EXECUTION_TRANSPORT=remote`
- `AONOHAKO_SANDBOX_BACKEND=none`
- `AONOHAKO_REMOTE_RUNNER_URL=https://runner.internal`
- inbound authentication at the gateway or application layer before public
  traffic can reach `/compile` or `/execute`

This is the recommended self-hosted shape for higher throughput. The local
server stays non-root and forwards both `/compile` and `/execute` to a separate
runner pool.

## Recommended high-throughput topology

For self-hosted production outside Cloud Run, prefer horizontal scale over
multi-slot helper execution:

1. Run one or more API/control-plane instances in `dev + remote + none`.
2. Run a separate pool of runner instances in
   `selfhosted + embedded + helper`.
3. Keep each runner instance at `AONOHAKO_MAX_ACTIVE_RUNS=1`.
4. Give every runner instance its own dedicated bounded tmpfs
   `AONOHAKO_WORK_ROOT` and delegated `AONOHAKO_CGROUP_PARENT`. Startup requires
   byte and inode ceilings for the whole work-root filesystem and validates the
   cgroup v2 CPU, memory, and pids controllers.
5. Scale throughput by adding more runner instances, not by increasing helper
   slots inside one process.

This keeps the same invariants as the Cloud Run baseline:

- one active untrusted execution per helper-backed instance
- root parent, sandbox UID child
- fixed UID/GID `65532` contestant and `65531` trusted-interactor roles for
  interactive requests
- dedicated writable work root
- immutable submitted files
- no shared mutable scratch between concurrent submissions in the same process
- cgroup-backed aggregate CPU, memory, pids, and process cleanup for every run
- optional outbound network only on dedicated self-hosted runners when
  `enable_network=true` is explicitly requested

## Why the helper backend stays single-slot

The current helper sandbox does not create per-run user IDs, mount namespaces,
or per-run containers. It hardens one target process tree with:

- `setrlimit`
- `PR_SET_NO_NEW_PRIVS=1`
- seccomp
- fd cleanup
- process-group cleanup
- runtime image permission hardening

That model is compatible with Cloud Run and with self-hosted root-backed
instances, but it is not designed for multiple simultaneous helper-backed runs
inside one process. The correct way to increase capacity is more runner
instances.

In code, this shape is named the `embedded-helper-process-hardening` security
contract. It records the guarantees that exist today and the boundaries that do
not exist yet:

| Present today | Still missing |
| --- | --- |
| root parent with dropped UID child and per-run cgroup | private mount namespace |
| `setrlimit` and workspace accounting | read-only rootfs |
| `PR_SET_NO_NEW_PRIVS` and seccomp denylist | masked `/proc` |
| network syscall gate | per-request UID allocation or user namespace |
| fd and cgroup cleanup plus fixed interactive role separation | seccomp allowlists and post-start `execve()` blocking |
| immutable submissions and symlink-safe output capture | mount-isolated private runtime state |

## Required cgroup guardrail

`embedded + container` remains reserved for a future self-hosted backend. It is
not implemented today.

The helper backend requires per-run cgroup v2 memory, pids, and CPU bandwidth
limits when deployed as `selfhosted + embedded + helper`.
`AONOHAKO_CGROUP_PARENT` must point at a writable delegated parent cgroup.
This is not a full container backend: it does not add a mount namespace, masked
`/proc`, per-run UID, or seccomp allowlist. It gives the kernel a run-level
memory/pids boundary and one-vCPU bandwidth guardrail, and makes the run cgroup
the authoritative process cleanup source.

The standalone cgroup preflight in `internal/isolation/cgroup` checks that:

- the intended root is mounted as `cgroup2`
- `cgroup.controllers` exists
- `cgroup.subtree_control` exists
- `cpu`, `memory`, and `pids` controllers are available
- the optional `io` controller is reported when present

This check should run before configuring `AONOHAKO_CGROUP_PARENT`, and the
future container backend should use the same controls as a startup gate before
adding mount and UID isolation.

Operators can run the same check explicitly on a candidate runner host:

```bash
aonohako-selftest cgroup-preflight
```

The command prints the preflight result as JSON and exits non-zero when required
cgroup v2 controls are unavailable.

On a privileged runner host, the communication end-to-end test can exercise the
same delegated cgroup with 64 participant processes:

```bash
AONOHAKO_COMMUNICATION_TEST_CGROUP=/sys/fs/cgroup/aonohako \
  go test ./internal/execute -run TestCommunicationEndToEndWithDelegatedCgroup -count=1 -v
```

The test compiles its participant and manager fixtures before checking the host
gate, and skips unless the environment variable names a writable delegated
cgroup and the test process is running as root.

## Mount namespace preflight

The reserved container backend will need private mount namespaces, read-only
bind remounts, and bounded writable tmpfs work areas. That backend is not
implemented yet, but a self-hosted runner host can be checked for those kernel
primitives before rollout planning:

```bash
aonohako-selftest mount-preflight
```

The command starts a child process so the parent selftest process is not moved
into a new namespace. The child verifies `unshare(CLONE_NEWNS)`, private mount
propagation, a bounded tmpfs mount, a procfs mount with `hidepid=2`, and a
read-only bind remount. It prints JSON and exits non-zero when the
host/container runtime does not permit those operations. A successful preflight
is only a prerequisite signal; the current
helper backend still does not provide mount namespace, read-only rootfs, or
masked `/proc` isolation.

When `AONOHAKO_CGROUP_PARENT` is set, startup performs the mutating validation
that a real runner needs: it verifies that the selected parent is under a
cgroup v2 mount and has the required controllers and `cgroup.subtree_control`,
rejects a group/world-writable parent, requires an empty `cgroup.procs`, writes
`+cpu +memory +pids` to `cgroup.subtree_control`, and verifies a probe
run-group create/remove cycle through the same `memory.max`,
`memory.swap.max`, `memory.oom.group`, `pids.max`, and `cpu.max` writes used by
real runs.
The compile, execute, and SPJ helper paths then use this write contract for one
run cgroup:

- create a sanitized run group name under the selected parent
- write positive `memory.max` and `pids.max` values
- write `memory.swap.max=0` when the kernel exposes it
- write `memory.oom.group=1`
- write `cpu.max=100000 100000` to cap sandbox CPU bandwidth at one vCPU
- move the target process by writing its PID to `cgroup.procs`
- kill any remaining run-cgroup members with `cgroup.kill` when the kernel
  exposes it, then remove the run cgroup without recursive deletion, using a
  short retry window for process cleanup races

The same package also defines the read contract used by the cgroup watchdog,
final post-exit classification, and the future isolated backend:

- `memory.current`
- `memory.peak` when the kernel exposes it
- `memory.events`, especially `oom`, `oom_kill`, and `oom_group_kill`
- `pids.current`
- `cpu.stat`
- `pids.events`

Execute and compile read these files before removing the run cgroup, so
kernel-side `memory.max` OOM kills and `pids.max` events still classify as
memory or process limits even when `cmd.Wait()` wins the race against the
watchdog tick.

Without `AONOHAKO_CGROUP_PARENT`, compile cleanup still depends on process
group kill plus best-effort descendant and sandbox-UID sweeps. Most compiler
paths keep that model tight, but `swiftc`, `hare`, and `isabelle` need limited
process-group operations for toolchain compatibility. Public runners that
support those compile targets should route them to cgroup-enabled self-hosted
workers or disable them on no-cgroup workers.

If that backend is added later, it should only be enabled after it can provide
all of the following at the same time:

- per-run writable root or tmpfs, not shared mutable scratch
- stronger ownership separation than a shared sandbox UID
- the same immutable-submission guarantees as the helper backend
- the same fail-closed startup validation used by current production shapes

Until then, `remote` transport plus a single-slot runner pool is the intended
self-hosted scaling path.
