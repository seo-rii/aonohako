# Diagnostic software work measurement

An explicitly requested ordinary C/C++ run can execute under Workmeter and return
`work_measurement` alongside its ordinary verdict, CPU, wall and memory values.
The work metric counts application basic blocks across process images. It is a
separate diagnostic unit, not a conversion to milliseconds or a new TLE rule.
The ordinary CPU and memory values of an instrumented run include instrumentation
cost. Do not compare its verdict near a resource limit as if it were uninstrumented.

Both operator configuration and an individual request are required. Default
images and requests do not enable this path. Communication, interactive runs,
SPJs, pipelines, Entry modes and multi-step requests are explicitly rejected for
now. An instrumented request executes once; short-case repeat sampling is bypassed
so one counter report does not silently stand in for several executions.

## Image and configuration

The reusable engine lives in the private `seo-rii/workmeter` repository. A small
source snapshot in `third_party/workmeter` is bound to an exact upstream commit
and file hashes by `UPSTREAM.json`; no cross-repository credential is required at
image build or runtime. The official DynamoRIO 11.3.0 SDK download is SHA-256
pinned by the vendored fetcher, and its license is retained in the runtime image.

`docker/workmeter.Dockerfile` is an explicit overlay on a reviewed execution
image. Supply an immutable `RUNTIME_IMAGE` digest and select target `runtime`.
It installs the new runner plus the engine/client under `/opt/workmeter`, without
modifying the existing language image build definitions. The current client
supports Linux x86-64 only. `third_party/workmeter/UPSTREAM.json` pins the private
upstream commit and every vendored source hash; the overlay verifies that manifest
before downloading the separately SHA-256-pinned engine SDK. CI needs no private
cross-repository checkout token.

The launcher fixes `-vm_size 32M -vmheap_size 32M` and reports engine profile
`dynamorio-11.3-vm32m-v1`. Upstream's large default virtual reservations can fail
before main under the ordinary C/C++ address-space bound. These options reduce
the engine's reservation budgets; no student CPU, RSS or address-space limit is
increased. Long-running code-cache and large-runtime behavior still need separate
qualification, and request bodies cannot override these engine options.

| Environment variable | Meaning |
|---|---|
| `AONOHAKO_WORKMETER_FAMILIES` | Comma-separated enabled family IDs; empty disables measurement. This PR registers `cpp`. |
| `AONOHAKO_WORKMETER_DRRUN` | Absolute trusted launcher path; default `/opt/workmeter/dynamorio/bin64/drrun`. |
| `AONOHAKO_WORKMETER_CLIENT` | Absolute trusted client path; default `/opt/workmeter/lib/libworkmeter.so`. |
| `AONOHAKO_WORKMETER_DISPATCH_PROFILE` | `default`, or the C/C++ diagnostic profile `glibc-2.41-sse2-v1`. |
| `AONOHAKO_WORKMETER_RESOURCE_POLICY` | Optional strict JSON operator policy for separate raw CPU/wall/RSS diagnostic limits; disabled when absent. |

For example, configure `AONOHAKO_WORKMETER_FAMILIES=cpp` and add
`"work_measurement": true` to a normal run request with `"lang": "CPP20"`.
Canonical C and C++ profiles are selected by their source compilation kind.
**Bare `binary` is ambiguous and is rejected**, so enabling C/C++ cannot silently
turn on measurement for every other compiled language. Existing clients that
send only `binary` must supply the actual source-language profile for this mode.
New language families are independently registered by sibling files in
`internal/workmeter`, each with explicit source profiles/runtime aliases.

## Library dispatch control

Raw instruction counts depend on which library implementation runs. In the
completed Cloud Run experiment, identical public memcpy workloads differed by
roughly 9,205 times under default runtime selection. A controlled glibc profile
reduced that spread to about 1.00000133 times in four new Intel instances. These
were work-count results, not reductions by that factor in CPU time; other CPU
families and libraries still require qualification.

`glibc-2.41-sse2-v1` carries the exact reviewed CPU-feature masks and copy thresholds
from that experiment. It requires host glibc 2.41, checked using the fixed trusted
`/usr/bin/getconf` executable, and is rejected for non-C/C++ families. The response
records the profile and host version. This host check does **not** attest the
embedded libc or actual selected functions of a statically linked submission;
`target_libc_attested` therefore remains false. The profile is an explicit
controlled-environment diagnostic, not a universal instruction-cost model.

## Result contract

A successful lifecycle aggregate resembles:

```json
{
  "schema": 1,
  "client_version": 1,
  "metric": "bb_entry_app_instructions",
  "scope": "process_tree_user_code",
  "rep_bytes_enabled": true,
  "engine_profile": "dynamorio-11.3-vm32m-v1",
  "authority": "diagnostic_only",
  "complete": true,
  "quality": "uninterrupted",
  "instructions": 123456,
  "rep_bytes": 8192,
  "rep_entries": 1,
  "unsupported_rep_entries": 0,
  "processes": 1,
  "images": 1,
  "signals_observed": 0,
  "family": "cpp",
  "dispatch_profile": "default",
  "target_libc_attested": false
}
```

`instructions` is basic-block-entry application work, including loaded libraries
and generated code. Engine/client instructions and kernel work are outside that
count. REP MOVS/STOS requested bytes are a separate dimension; SIMD and ordinary
memory operations have different costs. The execution API does not convert these
counts into credited milliseconds. A standalone, explicit reference-policy
calculator is described below; it is not a judgement policy.

`quality: signals_observed` records interrupted execution. Charging a whole block
at its entry can include an unexecuted tail when a signal arrives. Lifecycle
completeness therefore never means exact hardware-retired instructions. A normal
nonzero application exit may have a complete count; its existing runtime-error
verdict still applies.

A missing, killed, truncated, unsupported or inconsistent process image produces
`complete: false` with a bounded structural reason and **no aggregate instruction
or REP total**. It never becomes a valid zero. Original execution status is
preserved independently; a missing diagnostic report does not turn TLE into AC.
Report files remain in the private per-run workspace and are removed with it.

## Process trees and sandbox boundaries

The engine follows supported children and exec images. Parent and child counters
start independently, so the child's inherited pre-fork totals are not charged
again. Successful exec checkpoints terminate the old image; a failed exec uses
only the eventual cumulative end record. The Go collector verifies sequence,
lineage, process/image closure and uint64 overflow before summing.

The caller must finish/terminate the existing sandbox tree before collection.
The bounded collector admits at most 256 process files, 1 MiB per file, 16 MiB in
total, 4,096 events per process and 3,071 bytes per event. Directory-relative
no-follow opens reject symlinks, nonregular files and hardlinks; malformed or
changing reports fail closed. Record contents and counters are still target
controlled and **are not a tamper-resistant enforcement boundary**.

The sandbox chooses permissions and resource limits from the original runtime,
then adds the instrumentation wrapper. C/C++ still cannot create child processes;
network and privileged operations stay denied. Process aggregation is available
for runtimes whose existing sandbox policy permits children. Unsupported process
creation forms remain incomplete rather than silently dropping a child. Enabling
instrumentation does not grant fork, exec-related exceptions or additional memory.

## Validation

Focused tests cover configuration gates, exact parent/child and exec aggregation,
failed-exec deduplication, signal quality, missing children, missing thread ends,
overflow, malformed JSON and unsafe report files. The root integration test uses
a precompiled trusted C fixture through the real sandbox and checks stdin/output,
normal accounting, fork/network denial, unmetered execution and forced termination.

Build the overlay target `workmeter-validation` from the same source as `runtime`,
then run it as root with networking disabled. Its default entrypoint executes:

```text
workmeter-execute.test -test.v -test.run ^TestWorkmeterSandboxIntegration$ -test.timeout 90s
```

`AONOHAKO_TEST_WORKMETER=1` makes missing root or missing installed artifacts a
failure. Ordinary local unit runs explicitly skip this image-dependent test.
The integration image includes its compiler-produced fixture; runtime images do
not need a compiler or Python just to run the meter or collector.

## Separate actual resource envelope

The legacy request above retains its existing resource/verdict behavior. To
experiment beyond nominal limits without treating instrumentation cost as student
TLE/MLE, a dedicated diagnostic runner can additionally configure:

```json
{"max_limits":{"cpu_time_ms":5000,"wall_time_ms":20000,"memory_mb":512},"memory_budget_mb":4096,"reserved_memory_mb":512}
```

Set this JSON as `AONOHAKO_WORKMETER_RESOURCE_POLICY`. Values are illustrative,
not inferred overhead multipliers. `memory_budget_mb` must match the container's
actual allocation. Startup rejects a policy unless the maximum active run count
multiplied by its maximum RSS, plus the rounded-up work-root size and explicit
server reserve, fits that budget. This check covers envelope requests; use a
dedicated diagnostic runner rather than sharing it with unrelated workloads or
ordinary requests with larger allocations. RSS sampling can overshoot between
polls; the reserve is headroom, not a guarantee against container OOM.

An ordinary C/C++ request may then include:

```json
{
  "work_measurement": true,
  "limits": {"time_ms": 1000, "wall_time_ms": 2000, "memory_mb": 128},
  "work_measurement_limits": {"cpu_time_ms": 4000, "wall_time_ms": 8000, "memory_mb": 512}
}
```

All three actual limits are explicit, must cover the nominal limits, and must fit
operator caps. CPU/wall are bounded to 600,000 ms and RSS to 4,096 MiB. Actual wall
must be at least actual CPU. This first envelope supports ordinary C/C++ only.
The original request's nominal limits remain immutable. A private execution copy
supplies actual limits to the CPU watchdog, wall timer, RSS checks, final resource
checks, runtime memory settings and derived address-space limit. Startup CPU
normalization and short-run sampling are bypassed. Remote timeouts reserve the
actual wall allowance. `ignore_tle` cannot enable partial scoring after an actual
resource abort.

CPU and wall ceilings include instrumentation. RLIMIT_CPU remains the existing
coarse last-resort kernel guard; RSS is monitored rather than enforced atomically
by a per-run kernel cap on Cloud Run. The existing derived RLIMIT_AS is an address
space guard, not a physical-memory measurement. The container memory limit remains
the final shared OOM boundary. No CPU or memory overhead is subtracted.

Every envelope response has status `Container Initialization Failed`
(the existing `RunStatusInitFail` constant) to prevent legacy consumers from
mistaking diagnostic completion for an authorized student verdict. Its
`work_measurement_execution` records `authority: diagnostic_only`, the nominal
and actual limits, and one of `complete`, `incomplete`, `resource_exhausted`,
`cancelled`, or `failed`. A complete, normally exited and uninterrupted run has
`output_matches`; failed or killed runs do not. `termination_source` preserves
which underlying resource check stopped execution. Raw CPU/RSS remain in the
ordinary measurement fields, with no normalization metadata or score. This is an
experimental wire contract, not a replacement production judge.

A complete counter report is not sufficient to override a forced termination.
The remote control plane rejects ordinary verdicts from an older downstream
runner that ignores the envelope, as well as mismatched envelope metadata. No
trusted credited-time or credited-memory policy is enabled by this change.

## Explicit reference-time calculator

`internal/workmeter.TimePolicy` is a standalone diagnostic calculator, with no
API or service default. It requires a model ID/version, reference ID, exact
runtime identity, family, engine/dispatch profile, and positive integer weights:

```
estimated_ns = ceil((instructions * instruction_ns_numerator
                   + rep_bytes * rep_byte_ns_numerator) / ns_denominator)
```

The combined numerator uses exact wide arithmetic before rounding. Results that
exceed signed 64-bit nanoseconds are rejected; milliseconds round upward.
Incompatible runtime/profile, incomplete counts, signals, unsupported REP, or
missing counters yield an error and no estimate. Successful results still have
`authority: diagnostic_only`. Model/reference identities and weights are retained
with the estimate for audit.

There is deliberately no default coefficient based on the observed 3.1× CPU
cost or the two-submission experiment. A representative reference workload set
and held-out validation are needed before choosing production coefficients, and
the application-writable counter's trust boundary must be resolved before using
it to authorize AC/TLE. The fixed host glibc check does not attest target libc.

For the memory experiments and the demonstrated limitation of native replay,
see [Memory policy evidence](workmeter-memory-policy.md).
