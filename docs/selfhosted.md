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
- `AONOHAKO_MAX_ACTIVE_RUNS=1`
- `AONOHAKO_TRUSTED_RUNNER_INGRESS=true`
- `AONOHAKO_API_BEARER_TOKEN` set to a strong secret, or
  `AONOHAKO_INBOUND_AUTH=platform` when private ingress, mTLS, or a gateway
  authenticates inbound calls
- `AONOHAKO_TRUSTED_PLATFORM_HEADERS=true` when using
  `AONOHAKO_INBOUND_AUTH=platform`
- root parent process

This shape is supported for:

- local debugging
- a dedicated single-tenant runner VM
- a single active runner container behind a queue

It is intentionally serialized. The helper backend drops the target process to a
shared sandbox UID and relies on a dedicated work root plus immutable submitted
files. Running more than one active helper-backed execution in the same process
would weaken ownership isolation, so startup rejects values other than `1`.

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
4. Give every runner instance its own dedicated `AONOHAKO_WORK_ROOT`.
5. Scale throughput by adding more runner instances, not by increasing helper
   slots inside one process.

This keeps the same invariants as the Cloud Run baseline:

- one active untrusted execution per helper-backed instance
- root parent, sandbox UID child
- dedicated writable work root
- immutable submitted files
- no shared mutable scratch between concurrent submissions in the same process
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
| root parent with dropped UID child | per-run cgroup |
| `setrlimit` and workspace accounting | private mount namespace |
| `PR_SET_NO_NEW_PRIVS` and seccomp denylist | read-only rootfs |
| network syscall gate | masked `/proc` |
| fd cleanup and process-group cleanup | per-run UID or user namespace |
| immutable submissions and symlink-safe output capture | child-process accounting, seccomp allowlists, and post-start `execve()` blocking |

## Optional cgroup guardrail

`embedded + container` remains reserved for a future self-hosted backend. It is
not implemented today.

The helper backend can optionally add per-run cgroup v2 memory and pids limits
when the runner is deployed as `selfhosted + embedded + helper` and
`AONOHAKO_CGROUP_PARENT` points at a writable parent cgroup. This is not a full
container backend: it does not add a mount namespace, masked `/proc`, per-run
UID, or seccomp allowlist. It does give the kernel a run-level memory/pids
boundary that is stronger than RSS polling alone.

The non-mutating cgroup preflight in `internal/isolation/cgroup` checks that:

- the intended root is mounted as `cgroup2`
- `cgroup.controllers` exists
- `cgroup.subtree_control` exists
- `cpu`, `memory`, and `pids` controllers are available
- the optional `io` controller is reported when present

This check is still useful before enabling `AONOHAKO_CGROUP_PARENT`, and the
future container backend should use the same controls as a startup gate before
adding mount and UID isolation.

Operators can run the same check explicitly on a candidate runner host:

```bash
aonohako-selftest cgroup-preflight
```

The command prints the preflight result as JSON and exits non-zero when required
cgroup v2 controls are unavailable.

When `AONOHAKO_CGROUP_PARENT` is set, startup validates that the selected parent
is under a cgroup v2 mount and has the required controllers and
`cgroup.subtree_control`. The compile, execute, and SPJ helper paths then use
this write contract for one run cgroup:

- create a sanitized run group name under the selected parent
- write positive `memory.max` and `pids.max` values
- write `memory.oom.group=1`
- write `cpu.max` only when both quota and period are configured
- move the target process by writing its PID to `cgroup.procs`

The same package also defines the read contract used by the optional cgroup
watchdog and the future isolated backend:

- `memory.current`
- `memory.peak` when the kernel exposes it
- `memory.events`, especially `oom`, `oom_kill`, and `oom_group_kill`
- `pids.current`
- `cpu.stat`
- `pids.events`

If that backend is added later, it should only be enabled after it can provide
all of the following at the same time:

- per-run writable root or tmpfs, not shared mutable scratch
- stronger ownership separation than a shared sandbox UID
- the same immutable-submission guarantees as the helper backend
- the same fail-closed startup validation used by current production shapes

Until then, `remote` transport plus a single-slot runner pool is the intended
self-hosted scaling path.
