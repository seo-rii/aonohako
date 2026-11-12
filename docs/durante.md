# Diagnostic software work measurement

An explicitly requested ordinary supported-language run can execute under Durante and return
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

The reusable engine lives in the private `seo-rii/durante` repository. A small
source snapshot in `third_party/durante` is bound to an exact upstream commit
and file hashes by `UPSTREAM.json`; no cross-repository credential is required at
image build or runtime. The official DynamoRIO 11.3.0 SDK download is SHA-256
pinned by the vendored fetcher, and its license is retained in the runtime image.
The fetcher also builds the release/debug runtime cores from hash-pinned source
with Durante's process-lifetime and descriptor-range fixes. The SDK supplies the
matching headers, extensions and launcher; a stock core is not the v3 profile.

`docker/durante.Dockerfile` is an explicit overlay on a reviewed execution
image. Supply an immutable `RUNTIME_IMAGE` digest and select target `runtime`.
It installs the new runner plus the engine/client under `/opt/durante`, without
modifying the existing language image build definitions. The current client
supports Linux x86-64 only. `third_party/durante/UPSTREAM.json` pins the private
upstream commit and every vendored source hash; the overlay verifies that manifest
before downloading the separately SHA-256-pinned engine SDK. CI needs no private
cross-repository checkout token.

The launcher fixes `-vm_size 32M -vmheap_size 32M` and reports engine profile
`dynamorio-11.3-durante-v3-vm32m`. Before launch, installation validation checks
the runtime manifest's source/patch pins and the actual release-core digest.
Missing or mismatched provenance refuses measurement. This is a trusted local
installation check, not attestation against the host administrator or submission.
Reports record the source commit, pinned runtime source/patch identities and the
actual SHA-256 digests of the launcher, client and release core in `installation`.
The runner checks the installation again after the execution tree stops.
`unchanged_before_after: true` means these observations matched; a changed or
unavailable installation discards all totals. This does not prove that an
administrator did not temporarily replace a file between the checks. Keep the
installation immutable and operator-owned for the lifetime of the service.
Old v1/v2 time models and reports cannot be applied to v3 observations.
Upstream's large default virtual reservations can fail
before main under the ordinary C/C++ address-space bound. These options reduce
the engine's reservation budgets; no student CPU, RSS or address-space limit is
increased. Long-running code-cache and large-runtime behavior still need separate
qualification, and request bodies cannot override these engine options.

| Environment variable | Meaning |
|---|---|
| `AONOHAKO_DURANTE_FAMILIES` | Comma-separated enabled family IDs; empty disables measurement. Registered IDs: `cpp`, `cpython`, `pypy`, `native`, `jvm`, `dotnet`, `javascript-wasm`, `beam`, `scripting`, `scientific-functional`, `specialized`. JVM execution is rejected pending qualification in aonohako's sandbox. |
| `AONOHAKO_DURANTE_DRRUN` | Absolute trusted launcher path; default `/opt/durante/dynamorio/bin64/drrun`. |
| `AONOHAKO_DURANTE_CLIENT` | Absolute trusted client path; default `/opt/durante/lib/libdurante.so`. |
| `AONOHAKO_DURANTE_DISPATCH_PROFILE` | `default`, or the C/C++ diagnostic profile `glibc-2.41-sse2-v1`. |
| `AONOHAKO_DURANTE_RESOURCE_POLICY` | Optional strict JSON operator policy for separate raw CPU/wall/RSS diagnostic limits; disabled when absent. |

For example, configure `AONOHAKO_DURANTE_FAMILIES=cpp` and add
`"work_measurement": true` to a normal run request with `"lang": "CPP20"`.
Canonical C and C++ profiles are selected by their source compilation kind.
**Bare `binary` is ambiguous and is rejected**, so enabling C/C++ cannot silently
turn on measurement for every other compiled language. Existing clients that
send only `binary` must supply the actual source-language profile for this mode.
New language families are independently registered by sibling files in
`internal/durante`, each with explicit source profiles/runtime aliases.

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

Selected fields of a successful lifecycle aggregate resemble:

```json
{
  "schema": 1,
  "client_version": 1,
  "metric": "bb_entry_app_instructions",
  "scope": "process_tree_user_code",
  "rep_bytes_enabled": true,
  "engine_profile": "dynamorio-11.3-durante-v3-vm32m",
  "authority": "diagnostic_only",
  "integrity_verified": false,
  "measurement_contract": {
    "version": 1,
    "backend": "dynamorio",
    "counter_integrity": "application_mutable",
    "trusted_verdict_eligible": false,
    "instruction_semantics": "basic_block_entry",
    "simd_weighting": "one_per_instruction",
    "cpu_time_conversion": null
  },
  "rep_telemetry_status": "movs_stos_only",
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

The full session report also includes the required `installation` identities.
The Go collector constructs `measurement_contract` itself after reading raw
Durante lifecycle events. It does not copy trust claims from the target. The
contract is also present on unavailable results. Remote observations with missing,
unknown, duplicate or promoted contracts are rejected; an absent false boolean
is not an explicit false claim. Matching a contract establishes compatibility,
not security or eligibility for a student verdict.

`rep_telemetry_status` is independent of instruction completeness: `disabled`
means REP counting was off; `movs_stos_only` covers the requested MOVS/STOS byte
extension; `partial` retains positive `unsupported_rep_entries`; `unavailable`
exposes no totals. A complete observation may contain partial byte telemetry or
explicit `signals_observed` quality. It remains diagnostic and cannot be used as
a trusted time result. The standalone reference-policy calculator still rejects
partial REP coverage and interrupted observations.

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
Trusted resource stops, cancellation and incomplete execution override a
target-written complete stream: all instruction and REP totals become unavailable.
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

Build the overlay target `durante-validation` from the same source as `runtime`,
then run it as root with networking disabled. Its default entrypoint executes:

```text
durante-execute.test -test.v -test.run ^TestDuranteSandboxIntegration$ -test.timeout 90s
```

`AONOHAKO_TEST_DURANTE=1` makes missing root or missing installed artifacts a
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

Set this JSON as `AONOHAKO_DURANTE_RESOURCE_POLICY`. Values are illustrative,
not inferred overhead multipliers. `memory_budget_mb` must match the container's
actual allocation. Startup rejects a policy unless the maximum active run count
multiplied by its maximum RSS, plus the rounded-up work-root size and explicit
server reserve, fits that budget. This check covers envelope requests; use a
dedicated diagnostic runner rather than sharing it with unrelated workloads or
ordinary requests with larger allocations. RSS sampling can overshoot between
polls; the reserve is headroom, not a guarantee against container OOM.

An ordinary supported-language request may then include:

```json
{
  "work_measurement": true,
  "limits": {"time_ms": 1000, "wall_time_ms": 2000, "memory_mb": 128},
  "work_measurement_limits": {"cpu_time_ms": 4000, "wall_time_ms": 8000, "memory_mb": 512}
}
```

All three actual limits are explicit, must cover the nominal limits, and must fit
operator caps. CPU/wall are bounded to 600,000 ms and RSS to 4,096 MiB. Actual wall
must be at least actual CPU. The envelope accepts registered compatible families,
including stock CPython and PyPy; JVM execution remains rejected pending
qualification in aonohako's sandbox. Family registration alone is not runtime qualification.
The original request's nominal limits remain immutable. A private execution copy
supplies actual limits to the CPU watchdog, wall timer, RSS checks, final resource
checks, runtime memory settings and derived address-space limit. Startup CPU
normalization and short-run sampling are bypassed. Remote timeouts reserve the
actual wall allowance. `ignore_tle` cannot enable partial scoring after an actual
resource abort.

An unsuccessful envelope execution also clears work totals while retaining
installation/profile metadata. Its response remains `INIT_FAIL` with diagnostic
execution state; even a completed observation does not return student AC/TLE or
normalized CPU time. Ordinary requests with no measurement option retain their
existing execution and verdict path.

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

`internal/durante.TimePolicy` is a standalone diagnostic calculator, with no
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
see [Memory policy evidence](durante-memory-policy.md).

## Consolidated runtime integration

The common integration now contains the runtime registrations, fixtures and
historical evidence from the former family PRs #86–#95. Every family uses the
same Durante native client and bounded collector. Configuration remains opt-in;
no family is enabled automatically. See the family documents in
[`durante/`](durante/) for recorded compatibility and coverage gaps.

Python uses the installed stock CPython/PyPy executable, including its executed
native extensions and shared libraries. The earlier PR #84 rebuilt CPython with
LLVM-IR instrumentation and used separate PyPy JIT hooks; those partial counters
are superseded by Durante for this integration. Historical results are not
reinterpreted as Durante measurements. The reusable hashlib/zlib scaling corpus
and fresh verification procedure are described in
[Python qualification](durante/python-qualification.md).

Resource envelopes use the same raw CPU/wall/RSS protection for all admitted
families and bind complete reports to the request's exact runtime family. The
C/C++ glibc dispatch profile remains C/C++ only. The pinned client preserves full
application state across counting calls, including the state needed by JVM fault
handlers. Upstream JVM startup and runtime checks do not qualify aonohako's
runtime image, fixed reservation budgets or sandbox: JVM metadata stays
registered, but measurement remains refused until those checks pass here.
Invalid, killed or incomplete accounting never yields a usable total.
Other unqualified runtime paths remain documented and require their actual
image/sandbox tests before operator enablement. Consolidation does not grant
additional child-process or network permissions.

All envelope responses retain diagnostic-only authority and never grant student
AC/TLE or credited memory. Stable instruction counts cannot establish adversarial
counter integrity, calibrated milliseconds, or an instrumentation-free RSS peak.
