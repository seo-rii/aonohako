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

## Reserved future backend

`embedded + container` remains reserved for a future self-hosted backend. It is
not implemented today.

If that backend is added later, it should only be enabled after it can provide
all of the following at the same time:

- per-run writable root or tmpfs, not shared mutable scratch
- stronger ownership separation than a shared sandbox UID
- the same immutable-submission guarantees as the helper backend
- the same fail-closed startup validation used by current production shapes

Until then, `remote` transport plus a single-slot runner pool is the intended
self-hosted scaling path.
