# Diagnostic software work measurement

An explicitly requested supported-language run can execute under Durante and return
`work_measurement` alongside its ordinary verdict, CPU, wall and memory values.
Interactive and communication-v1/v2 runs return separate role observations in
`work_measurements` instead.
A deployment can also collect counters by replaying eligible ordinary requests
after native grading. This path preserves the native contestant's reported
CPU time and verdict. [Automatic observations](durante-judging.md#automatic-observations-alongside-native-grading)

The opt-in, operator-gated [work-based judging](durante-judging.md) path turns a
completed C/C++-style observation into an actual AC/WA/TLE/RE verdict using
`limits.time_ms` as a virtual-millisecond limit; it is off by default.
The work metric counts application basic blocks across process images. Raw work
units remain separate from scheduled CPU time. An optional reference-time
estimate uses an explicit operator model and does not change TLE rules.
The ordinary CPU and memory values of an instrumented run include instrumentation
cost. Do not compare its verdict near a resource limit as if it were uninstrumented.

Both operator configuration and an individual request are required. Default
images and requests do not enable this path. Standalone special judges can request separate
contestant and checker clocks through peer-time version 2. Pipelines, Entry
modes and legacy multi-step requests retain native time authority. An instrumented request executes once;
short-case repeat sampling is bypassed
so one counter report does not silently stand in for several executions.

## Image and configuration

The reusable engine lives in the private `seo-rii/durante` repository. A small
source snapshot in `third_party/durante` is bound to an exact upstream commit
and file hashes by `UPSTREAM.json`; no cross-repository credential is required at
image build or runtime. The official DynamoRIO 11.3.0 SDK download is SHA-256
pinned by the vendored fetcher, and its license is retained in the runtime image.
The fetcher also builds the release/debug runtime cores from hash-pinned source
with Durante's process-lifetime, descriptor-range and code-replay fixes. The SDK
supplies the matching headers, extensions and launcher; a stock core is not the
v4 profile. The client and patched runtime share a private replay callback ABI;
update both from the pinned snapshot rather than reusing an older runtime core.

`docker/durante.Dockerfile` is an explicit overlay on a reviewed execution
image. Supply an immutable `RUNTIME_IMAGE` digest and select target `runtime`.
It installs the new runner plus the engine/client under `/opt/durante`, without
modifying the existing language image build definitions. The current client
supports Linux x86-64 only. `third_party/durante/UPSTREAM.json` pins the private
upstream commit and every vendored source hash; the overlay verifies that manifest
before downloading the separately SHA-256-pinned engine SDK. CI needs no private
cross-repository checkout token.

The launcher fixes `-disable_traces -vm_size 32M -vmheap_size 32M` and reports engine profile
`dynamorio-11.3-durante-v7-vm32m`. Before launch, installation validation checks
the runtime manifest's source, process-lifetime patch, code-replay patch and
actual release-core digest.
Missing or mismatched provenance refuses measurement. This is a trusted local
installation check, not attestation against the host administrator or submission.
Reports record the source commit, pinned runtime source/patch identities and the
actual SHA-256 digests of the launcher, client and release core in `installation`.
The runner checks the installation again after the execution tree stops.
`unchanged_before_after: true` means these observations matched; a changed or
unavailable installation discards all totals. This does not prove that an
administrator did not temporarily replace a file between the checks. Keep the
installation immutable and operator-owned for the lifetime of the service.
The installation identity includes `runtime_code_replay_patch_sha256` in
addition to `runtime_patch_sha256`. Old seven-field identities remain readable
and retain their original values, but do not satisfy current installation
validation. Engine profile v6 pins Durante `238193f` and supports the optional
version 1 static block latency counter. The installation identity and time
models bind to this version. Rebuild the runtime overlay and collect new
reference data before qualifying coefficients for v6.
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
| `AONOHAKO_DURANTE_TIME_POLICY` | Optional strict `TimePolicy` JSON with every model field and positive integer coefficient explicitly supplied. Enables requested C/C++ reference-time estimates with a resource policy. |
| `AONOHAKO_DURANTE_TIME_MODELS` | Optional strict schema-1 (legacy) or schema-2 (effective runtime options) registry of shared coefficient models and explicit execution profiles. Mutually exclusive with `AONOHAKO_DURANTE_TIME_POLICY`; family registration alone enables no profile. |
| `AONOHAKO_DURANTE_RUNTIME_IMAGE` | Actual immutable Durante overlay image identity, in `sha256:<64 lowercase hex digits>` form, supplied by the operator from deployment metadata. Required with either time setting, independently of the model's profile bindings. |
| `AONOHAKO_DURANTE_CPUID_PROFILE` | Empty (default) exposes host CPUID. `x86-64-v1` or `x86-64-v2` makes the client report one fixed virtual processor to every application CPUID/XGETBV, so library and runtime dispatch, and therefore counts, do not follow the Cloud Run instance's processor. Complete reports carry the client's `cpuid_profile` receipt; a missing or different receipt makes the observation unavailable. Requires an enabled family. See [CPUID profiles](https://github.com/seo-rii/durante/blob/main/docs/CPUID_PROFILE.md). |
| `AONOHAKO_DURANTE_LATENCY_BOUND` | `v1` enables the static block latency counter for measured executions. Empty or `off` disables it. A complete report from an enabled run carries `latency_bound: {"version":1,"cycles_x16":...}` for the process tree. The unit is one sixteenth of a modeled cycle. |
| `AONOHAKO_DURANTE_RUNTIME_DETERMINISM` | Empty or `off` (default) runs every launcher unchanged. `v1` pins runtime-internal nondeterminism of measured executions only: Node (`JAVASCRIPT`, and sources that run as JavaScript) gets `--predictable`, and CPython gets hash seed 0 (`-I` is replaced by `-s -P` and every `PYTHON*` variable except `PYTHONHASHSEED=0` is removed, because isolated mode ignores `PYTHONHASHSEED`); PyPy gets `PYTHONHASHSEED=0`, which it reads even under `-I`. Native targets and other launchers are unchanged. The options are part of the effective runtime-options descriptor, complete reports for a pinned launcher carry `runtime_determinism`, and an estimate requires the report to match the configuration. Schema 1 time models cannot bind a pinned launcher; use schema 2. Required by judge mode for `cpython`, `pypy` and `javascript-wasm`. Requires an enabled family. See [Runtime determinism](durante-runtime-options.md#runtime-determinism). |
| `AONOHAKO_DURANTE_JUDGE_MODE` | `on` enables work-based judging (virtual-millisecond verdicts) and shadow comparisons; `shadow` enables only non-deciding `work_measurement_shadow` comparisons; empty or `off` disables both. See [work-based judging](durante-judging.md). |
| `AONOHAKO_DURANTE_JUDGE_FAMILIES` | Comma-separated families a judge-mode request may ask to judge. Each must be an enabled, qualified measurement family. |
| `AONOHAKO_DURANTE_JUDGE_INTEGRITY` | `off` (default) trusts the counts; `plausibility` additionally requires the instrumented run's raw CPU time to be consistent with the converted work. |
| `AONOHAKO_DURANTE_JUDGE_INTEGRITY_RATIO` | With `plausibility`, the maximum believable ratio of instrumented raw CPU nanoseconds to converted virtual nanoseconds; a higher observed ratio withholds the verdict. |

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
  "engine_profile": "dynamorio-11.3-durante-v7-vm32m",
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
  "rep_breakdown": {
    "version": 1,
    "movs_bytes": 8192,
    "stos_bytes": 0,
    "movs_entries": 1,
    "stos_entries": 0
  },
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
a trusted time result. The reference-time estimator rejects
partial REP coverage and interrupted observations.

`rep_breakdown` is optional version-1 detail of the existing totals, with
`movs_bytes`, `stos_bytes`, `movs_entries` and `stos_entries` as unsigned 64-bit
integers. The two byte counts sum exactly to `rep_bytes`; the two entry counts
sum exactly to `rep_entries`. Bytes are the selected repeat count (RCX or ECX)
times the element width at REP entry, not necessarily completed memory traffic.
Zero-count entries still add
one entry of their kind. A positive kind's byte count needs a positive entry
count. Unsupported REP instructions remain in `unsupported_rep_entries` and
are not attributed to either kind.

The current client advertises `rep_breakdown_version: 1` in each raw image start
and supplies all four flat `rep_movs_*`/`rep_stos_*` terminal counters. The Go
collector validates each selected end or successful exec checkpoint, then sums
the image/process tree. Failed-exec checkpoints are provisional and discarded.
Missing/mixed capabilities, incomplete selected counters, inconsistent sums or
overflow make the observation unavailable. Legacy raw streams with no capability
remain readable, with `rep_breakdown` absent rather than invented zero values.
When detail is present, every field is required; unknown versions, nulls and
noncanonical nested keys are rejected. REP-disabled observations have explicit
zero detail if the client advertises the capability. Incomplete, cancelled or
resource-stopped results omit the detail along with all other totals.

Ordinary, interactor and communication-role reports use this same optional
object. Forwarding and reference datasets preserve it without combining peers.
The existing reference-time formula still uses aggregate `rep_bytes`; adding
MOVS/STOS observations does not introduce new coefficients or qualify a model.

`instructions` is basic-block-entry application work, including loaded libraries
and generated code. Engine/client instructions and kernel work are outside that
count. REP MOVS/STOS requested bytes are a separate dimension; SIMD and ordinary
memory operations have different costs. The execution API does not convert these
counts into credited milliseconds. An explicit reference policy can produce a
separate diagnostic time estimate, described below; it is not a judgement policy.

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

## Interactive and communication observations

Ordinary communication v1/v2 responses include `communication_cpu_by_role`
after the worker runs the match processes. Seats appear in participant-index
order, followed by the manager. Each row carries the program ID, native CPU
limit in milliseconds, sandbox execution status, start and interruption flags,
and CPU usage. Two seats running the same program have different indices.
Preflight failures can return before role accounting exists.

Use `cpu_time_ns` for a role's precise reading when present. With CPU
normalization enabled, `raw_cpu_time_ns` records CPU time before calibration.
The legacy top-level `cpu_time_ms` sums the roles' individually rounded
milliseconds. A precise total requires a sample from every started role.
Interrupted roles can have measured CPU time even if they produced no complete
Durante report. The coordinator still determines the score, forfeit or judge
error. On a forwarded match an older worker can return millisecond totals
alone. A controller that cannot verify the additional role counters retains
the original outcome and omits the new timing details.

When a request also asks for Durante, join `work_measurements` to these
rows by role, program ID and participant index. In that mode the CPU
readings include instrumentation overhead. Ordinary matches record their
own CPU usage in a single execution of each role.

Set the top-level `work_measurement` flag to `true` to instrument both the
contestant and interactor, or every communication participant and the manager.
Each role uses its own Durante session and report directory. Interactive runs
require enabled, compatible source-language profiles in both `lang` and
`interactor.lang`. Communication retains its native-only execution contract and
requires a source-language profile such as `CPP20` in every referenced
`programs[].lang`; its top-level `lang` stays empty. Bare `binary` is rejected for
any measured role. Each family must be enabled, and the existing JVM qualification
gate and dispatch-profile restrictions still apply.

The response's ordered `work_measurements` array uses these identities:

| Mode | Role order | `program_id` | `participant_index` |
|---|---|---|---|
| Interactive | `contestant`, `interactor` | Same as the role | Absent |
| Communication v1 | All `participant` entries, then `manager` | Requested participant/manager program ID | Zero-based participant index; absent for manager |
| Communication v2 | Two `participant` entries, then `manager` | IDs in `participant_program_ids` order, then manager ID | Seat 0 or 1; absent for manager |

For example, one communication role has this shape; `report` contains the full
report contract described above:

```jsonc
{
  "role": "participant",
  "program_id": "solver",
  "participant_index": 0,
  "execution_status": "OK",
  "exit_code": 0,
  "report": { /* complete Durante report, including diagnostic contract */ }
}
```

`execution_status` records the sandbox's actual `OK`, resource failure, runtime
failure, or initialization failure before coordinator verdict attribution.
`termination_source` is present when a resource check or interruption supplies
one. A normal nonzero target exit can retain a complete diagnostic count; a
resource stop, cancellation, signal, or incomplete stream cannot. A completed
peer's report remains available when another peer fails. With observation alone
or a version 1 peer time budget, an accepted interactive
or match result can contain an unavailable participant report when the
interactor or manager finished first and the coordinator stopped that participant.
Version 2 interactive acceptance requires both roles to complete within budget.

These reports are separate observations, with no cross-role total. The singular
`work_measurement` field is absent for these modes. Remote forwarding checks the
role identities, source-language families, raw execution outcomes, and each
diagnostic contract. A pre-execution initialization failure may omit the array;
an executed composite request returns every role, including unavailable reports.
An early communication rejection that retains its existing runtime-error verdict
returns ordered unstarted roles with `execution_status: "Container Initialization Failed"`,
no exit code, and no report. This preserves the original admission or setup reason
without claiming that those programs ran.
The counts remain `diagnostic_only` with `integrity_verified: false` and
`trusted_verdict_eligible: false`.

The bool option uses the existing per-role CPU, wall, and memory limits;
instrumentation overhead is included. The separate `work_measurement_limits`
raw envelope remains restricted to ordinary single-program requests and is
rejected with interactive or communication requests. It does not silently expand
every peer's resource allowance.

### Per-role diagnostic time budgets

Peer time version 2 also supports online enforcement of operator-bound role
budgets. [Live role clocks](durante-judging.md#live-clocks-for-interactive-and-communication-roles)

Interactive and communication v1/v2 requests can additionally set
`work_measurement_peer_time`. It requires `work_measurement: true` and a version-1
budget for **every actual role**, identified by `role`, `program_id`, and the
participant's zero-based `participant_index`. Budget order is irrelevant;
communication-v1 seats running the same program still need separate budgets.
Each `time_ms` is an integer from 1 through 600000. Duplicated, missing, or extra
role identities are rejected. For an interactive request, add:

```json
{
  "work_measurement": true,
  "work_measurement_peer_time": {
    "version": 1,
    "budgets": [
      {"role": "contestant", "program_id": "contestant", "time_ms": 1000},
      {"role": "interactor", "program_id": "interactor", "time_ms": 2000}
    ]
  }
}
```

For communication, use `role: "participant"` with the requested program ID and
seat index for each participant, followed by a `role: "manager"` budget with its
program ID and no index. For example, two v1 entries may both have
`program_id: "solver"`, distinguished by indices 0 and 1. Non-participant roles
must omit `participant_index`.

Every role must have a selectable operator time policy for its declared source
profile, deployment image, and dispatch before execution or remote upload starts.
The [shared-model registry](#shared-models-and-approved-execution-profiles) lets
different approved source profiles select one model without duplicating weights.
Supported formulas use instructions with aggregate REP, instructions with separate MOVS/STOS costs, or the v1 latency counter with aggregate REP. A legacy single
policy is sufficient when every role uses its one approved source profile.
Requests supply budgets only; they cannot supply or override model coefficients.

After execution, each `work_measurements[]` entry gains a `time_budget` object:

```jsonc
{
  "version": 1,
  "authority": "diagnostic_only",
  "budget_ms": 1000,
  "estimate": { /* complete TimeEstimate, including model, coefficients and ns/ms */ },
  "exceeded": false
}
```

The comparison is strictly `estimate.nanoseconds > budget_ms * 1000000`;
equality does not exceed the budget. Only complete, normally exited,
uninterrupted observations with current installation/profile identity and
observed REP detail are convertible. A normal nonzero exit may retain an
estimate while its original verdict remains a failure. Coordinator stops,
cancellation and unstarted roles instead have `unavailable:
"execution_not_complete"`; absent or incompatible reports have
`observation_unavailable` or `incompatible_observation`. Signals, partial REP,
legacy missing REP detail, and arithmetic overflow yield
`observation_not_convertible`. Unavailable results omit both `estimate` and
`exceeded`. Other completed roles retain their own estimates when one role fails.
A pre-execution initialization failure may still omit the entire role array.

Version 1 compares the estimate after exit and leaves the native verdict,
score and match outcome in place. Version 2 can enforce each role's virtual
budget during execution. Both versions use the interactive or communication
request's per-role CPU, wall and memory safety limits, which include
instrumentation overhead. Standalone SPJs can use version 2 with roles
`contestant` and `checker` and two sequential raw limits.
[SPJ grading and clock receipts](durante-judging.md#standalone-special-judges)

The remote control plane independently selects each role's model and recomputes
the full result. It rejects different budgets, coefficients, estimates, reasons,
unsolicited results, and a downstream runner that ignores the requested option.
Configure the actual downstream execution image on both sides. Without this
option, `time_budget` is absent and the existing peer behavior is unchanged.
The integration fixtures use synthetic arithmetic weights only; they do not
supply calibrated production coefficients.

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
overflow, malformed JSON and unsafe report files. Root integration tests use
precompiled C fixtures through the real sandbox. They check ordinary accounting,
interactive AC/WA and final-reply EOF, communication streams larger than pipe
capacity, participant forfeits, manager failure, resource stops, cancellation,
and per-role report retention. Unmetered behavior and remote verdict/accounting
forwarding have separate regression coverage.

Build the overlay target `durante-validation` from the same source as `runtime`,
then run it as root with networking disabled. Its default entrypoint executes:

```text
durante-execute.test -test.v -test.run ^TestDurante(Sandbox|ResourceEnvelope|TimeEstimate|Judge|Interactive|Communication)Integration$ -test.timeout 240s
```

After tagging that build `aonohako-durante-validation:local`, pass its actual image
ID to the competitive fixture. Use the validation image's ID, not its base
`RUNTIME_IMAGE` digest:

```bash
validation_image_id=$(docker image inspect --format '{{.Id}}' aonohako-durante-validation:local)
docker run --rm --user 0 --network none --read-only --memory 2g --pids-limit 256 \
  --tmpfs /tmp:rw,exec,mode=1777,size=268435456 \
  --tmpfs /work:rw,exec,mode=0755,size=268435456 \
  -e AONOHAKO_WORK_ROOT=/work \
  -e AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT="$validation_image_id" \
  -e AONOHAKO_DURANTE_RUNTIME_IMAGE="$validation_image_id" \
  "$validation_image_id"
```

`AONOHAKO_TEST_DURANTE=1` makes missing root or missing installed artifacts a
failure. The communication fixture also requires the image fingerprint. Ordinary
local unit runs explicitly skip these image-dependent tests.
The integration image includes its compiler-produced fixtures; runtime images do
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

The following envelope applies to ordinary supported-language requests.
Interactive and communication requests use their own per-role safety limits
for observation and for live clocks.

```json
{
  "work_measurement": true,
  "limits": {"time_ms": 1000, "wall_time_ms": 2000, "memory_mb": 128},
  "work_measurement_limits": {"cpu_time_ms": 4000, "wall_time_ms": 8000, "memory_mb": 512}
}
```

An explicit envelope includes all three actual limits. The
[deciding online judge clock](durante-judging.md#online-durante-clock) also accepts
the operator maximum as its default envelope. Actual limits must cover the
nominal limits and fit operator caps. CPU/wall are bounded to 600,000 ms and RSS to 4,096 MiB. Actual wall
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
`cancelled`, or `failed`. A complete, normally exited run has
`output_matches`; failed or killed runs do not. `termination_source` preserves
which underlying resource check stopped execution. Raw CPU/RSS remain in the
ordinary measurement fields, with no normalization metadata or score. This is an
experimental wire contract, not a replacement production judge.

A complete counter report is not sufficient to override a forced termination.
The remote control plane rejects ordinary verdicts from an older downstream
runner that ignores the envelope, as well as mismatched envelope metadata. No
trusted credited-time or credited-memory policy is enabled by this change.

## Explicit reference-time calculator

`work_measurement_time: true` asks the execution API to apply the operator's
`internal/durante.TimePolicy` to a completed diagnostic observation. This option
requires `work_measurement: true` and an effective raw envelope. Diagnostic
requests supply `work_measurement_limits` explicitly. Time conversion accepts
ordinary registered source profiles explicitly covered by the operator's time
configuration. The legacy single policy retains its C/C++ scope. The shared-model
registry below can approve compatible families such as CPython and PyPy. JVM
execution remains unavailable pending sandbox qualification. Ambiguous `binary`,
SPJs, pipelines, entry modes, and steps do not support time conversion.
Interactive, communication and standalone SPJ runs use the separate
[`work_measurement_peer_time` option](#per-role-diagnostic-time-budgets) rather
than this ordinary envelope flag. Without this flag, the existing measurement admission
rules remain unchanged.

For the existing single-policy format, the operator must enable `cpp`, configure
a valid resource policy, and supply both `AONOHAKO_DURANTE_TIME_POLICY` and
`AONOHAKO_DURANTE_RUNTIME_IMAGE`. The time
policy's engine and dispatch profiles must match the measurement configuration.
Its `runtime_id` must exactly equal `<canonical source-language ID>@<image digest>`;
for example, `CPP20@sha256:<64 lowercase hex digits>`. A model for `CPP20` does
not also admit `C11` or `CPP17`. Source aliases resolve to their canonical profile
before this comparison. Existing single-policy configuration and response JSON
remain compatible.

### Shared models and approved execution profiles

Alternatively, set `AONOHAKO_DURANTE_TIME_MODELS` and leave
`AONOHAKO_DURANTE_TIME_POLICY` empty. A model stores one set of exact coefficients;
each profile selects a model ID/version and separately names its canonical source,
family, image, engine and dispatch. Several source languages can use the same model
without copying or fitting a separate set of coefficients for each language.

The following registry illustrates the shape only. **Its coefficients and
qualification IDs are synthetic, not a calibrated cross-language model.** Replace
the image placeholders with immutable image digests and qualification IDs with
records supporting each intended execution profile before configuring estimates.

```json
{
  "schema": 1,
  "models": [{
    "model_id": "shared-arithmetic-example",
    "model_version": 1,
    "reference_id": "arithmetic-fixture-only",
    "method": "weighted-work-nanoseconds-v1",
    "coefficients": {
      "instruction_ns_numerator": 1,
      "rep_byte_ns_numerator": 1,
      "ns_denominator": 1
    }
  }],
  "profiles": [{
    "source_language": "CPP20",
    "family": "cpp",
    "runtime_image": "<actual-overlay-image-digest>",
    "engine_profile": "dynamorio-11.3-durante-v7-vm32m",
    "dispatch_profile": "default",
    "model_id": "shared-arithmetic-example",
    "model_version": 1,
    "qualification_id": "synthetic-cpp20-example"
  }, {
    "source_language": "PYTHON3",
    "family": "cpython",
    "runtime_image": "<actual-overlay-image-digest>",
    "engine_profile": "dynamorio-11.3-durante-v7-vm32m",
    "dispatch_profile": "default",
    "model_id": "shared-arithmetic-example",
    "model_version": 1,
    "qualification_id": "synthetic-python3-example"
  }]
}
```

All fields are required except model `method`, whose absence means
`weighted-work-nanoseconds-v1`, and binding `cpuid_profile`. An explicitly empty
method is rejected. `cpuid_profile` names the virtual processor the binding's
qualification evidence used; a binding without it applies only while host
CPUID is visible, and a binding with it applies only when
`AONOHAKO_DURANTE_CPUID_PROFILE` is the same profile. The legacy single
`AONOHAKO_DURANTE_TIME_POLICY` accepts the same optional field with the same
rule. Estimates record the profile, and a report whose receipt differs is not
converted (`runtime_profile_mismatch`). The
registry accepts at most 32 models, 128 profiles
and 64 KiB of JSON. Unknown fields, duplicates, nulls, noncanonical source IDs,
unknown model versions and ambiguous profile bindings are rejected. Each model
ID/version is unique; each source/image/engine/dispatch/CPUID-profile combination
has one binding in schema 1. Schema 2 also includes the effective runtime-options ID in this key.
The optional `glibc-2.41-sse2-v1` dispatch profile retains its C/C++ restriction.

The source family must also be enabled in `AONOHAKO_DURANTE_FAMILIES`, and an
independent `AONOHAKO_DURANTE_RUNTIME_IMAGE` plus a valid resource policy are still
required. A registry may list other images or dispatch profiles, but only a binding
matching this execution image and configured dispatch is selectable. `profiles: []`
registers models without approving any execution. Adding a family or a model never
creates missing bindings. A `qualification_id` records an operator's approval; the
service does not verify that external record or infer CPU-time accuracy from it.
Adding a JVM binding cannot bypass its existing execution gate.

Schema 1 bindings match source, image, engine and dispatch only. They remain
unbound with respect to effective runtime options and must not be read as
qualification of every configuration within one image. Keep their runtime
profile, operator tuning, Python library mode and memory-derived heap settings
fixed to their evidence. Schema 2 additionally requires an exact generated
runtime-options descriptor for its explicitly supported launchers; mismatches
are rejected before local execution or remote upload, without fallback. See
[effective runtime options](durante-runtime-options.md) for migration, supported
launchers, operator inspection and the boundary of this configuration identity.
Neither format supplies qualification evidence or an enforceable live budget.

Selection resolves to the existing `TimePolicy` and `time_estimate` response shape,
including the selected canonical `SOURCE@image` runtime ID. The runner and remote
control plane each hold independent immutable configuration snapshots. Neither
request payloads nor returned estimates choose the model or its coefficients.

These shared coefficients express a common mapping from observed machine work.
They do not require two implementations of the same algorithm to have identical
counts: interpreter, runtime, library and generated-code work is included. A shared
work-score definition is distinct from predicting actual CPU nanoseconds; CPU-time
accuracy requires representative held-out validation. The existing CPP20 corpus
does not qualify other language profiles, and this registry adds no production
weights or credited-time verdict policy.

### Single-policy example and result semantics

Use the actual final Durante overlay image identity from deployment metadata.
This value is an operator assertion; the service does not discover or attest its
own container image. A remote control plane must configure the same downstream
runner image and model. Its own frontend image is not the execution runtime.
The control plane recomputes the estimate from the returned raw observation and
its own configured policy and rejects mismatched model, coefficients, values,
availability, or nominal-limit comparison.

The following policy template illustrates the required fields. Replace the
image placeholder with the same digest supplied in
`AONOHAKO_DURANTE_RUNTIME_IMAGE`. **These coefficients are for arithmetic examples
only; production coefficients have not been selected or calibrated.**

```json
{
  "model_id": "arithmetic-example",
  "model_version": 1,
  "reference_id": "arithmetic-fixture-only",
  "runtime_id": "CPP20@<actual-overlay-image-digest>",
  "family": "cpp",
  "engine_profile": "dynamorio-11.3-durante-v7-vm32m",
  "dispatch_profile": "default",
  "instruction_ns_numerator": 1,
  "rep_byte_ns_numerator": 1,
  "ns_denominator": 1
}
```

All fields are explicit, and the integer weights and denominator must be
positive. A request supplies the opt-in flag and execution limits, never model
coefficients. For example, add these fields to a normal `CPP20` request:

```json
{
  "lang": "CPP20",
  "work_measurement": true,
  "work_measurement_time": true,
  "limits": {"time_ms": 1000, "wall_time_ms": 2000, "memory_mb": 128},
  "work_measurement_limits": {"cpu_time_ms": 4000, "wall_time_ms": 8000, "memory_mb": 512}
}
```

The policy computes:

```
estimated_ns = ceil((instructions * instruction_ns_numerator
                   + rep_bytes * rep_byte_ns_numerator) / ns_denominator)
```

The combined numerator uses exact wide arithmetic before rounding. Results that
exceed signed 64-bit nanoseconds are rejected; milliseconds round upward.
An explicit `method: weighted-rep-operations-nanoseconds-v2` instead uses
separate positive MOVS and STOS byte weights and requires observed REP detail;
see [separate MOVS and STOS costs](durante-rep-model.md) for configuration and
compatibility. Omitting `method` preserves the formula above.
Incompatible runtime/profile, incomplete counts, signals, unsupported REP, or
missing counters yield an error and no estimate. Successful results still have
`authority: diagnostic_only`. Model/reference identities and weights are retained
with the estimate. For a report containing 123,456 instructions and 8,192 REP
bytes, the example coefficients produce 131,648 ns and 1 rounded-up ms. Selected
response fields would be:

```json
{
  "status": "Container Initialization Failed",
  "work_measurement_execution": {
    "authority": "diagnostic_only",
    "state": "complete",
    "time_estimate": {
      "instruction_ns_numerator": 1,
      "rep_byte_ns_numerator": 1,
      "ns_denominator": 1,
      "method": "weighted-work-nanoseconds-v1",
      "authority": "diagnostic_only",
      "model_id": "arithmetic-example",
      "model_version": 1,
      "reference_id": "arithmetic-fixture-only",
      "runtime_id": "CPP20@<actual-overlay-image-digest>",
      "family": "cpp",
      "engine_profile": "dynamorio-11.3-durante-v7-vm32m",
      "dispatch_profile": "default",
      "nanoseconds": 131648,
      "milliseconds": 1
    },
    "nominal_time_exceeded": false,
    "time_estimate_eligibility": {"version": 1, "eligible": true, "reason_codes": []}
  }
}
```

`nominal_time_exceeded` compares estimated nanoseconds directly with
`limits.time_ms * 1,000,000`, using strict `>`; equality is not an exceedance.
It does not use the rounded `milliseconds` field and does not select AC/TLE.
The response retains raw CPU/RSS, `diagnostic_only`, initialization-failure
status, and no score or CPU normalization.

When conversion is unavailable, `time_estimate` and `nominal_time_exceeded` are
absent and `time_estimate_unavailable` explains why:

| Value | Meaning |
|---|---|
| `execution_<state>` | The diagnostic execution did not complete; suffixes include `resource_exhausted`, `cancelled`, `failed`, and `incomplete`. |
| `observation_not_convertible` | The raw observation completed but the estimator rejected it, for example because of signals, partial REP coverage, or numeric overflow. |
| `incompatible_observation` | A complete execution lacks a compatible diagnostic report. |
| `incompatible_time_configuration` | The configured model or runtime identity cannot apply to this observation. |

A complete raw observation can therefore retain its counts and `output_matches`
while its time estimate is unavailable. Admission or initialization failure
before an envelope execution can omit `work_measurement_execution` entirely.
Requesting a time estimate without an applicable operator policy is rejected
before target execution. Omitting the flag preserves the previous response and
does not automatically apply a configured model.

### Structured conversion eligibility

When `work_measurement_time` is requested and an execution envelope exists,
`time_estimate_eligibility` provides an additional versioned explanation. The
existing `time_estimate_unavailable` values above remain unchanged. For example,
a complete report with signals and unsupported REP work, evaluated with a v2
model but lacking operation detail, keeps its raw counts and reports:

```json
{
  "time_estimate_unavailable": "observation_not_convertible",
  "time_estimate_eligibility": {
    "version": 1,
    "eligible": false,
    "reason_codes": ["signals_observed", "unsupported_rep_entries", "rep_breakdown_missing"]
  }
}
```

Successful conversion has `eligible: true` and an explicit empty reason array.
Ineligible conversion has `eligible: false` and at least one code; it never
returns an estimate or nominal-limit comparison. This explains eligibility for
the selected diagnostic model, not measurement trust or a student verdict.
Signal/partial-REP exclusions remain strict. A missing legacy REP breakdown
blocks v2 even when combined bytes are zero; an explicitly observed, consistent
zero breakdown passes that requirement. V1 retains its legacy-detail behavior.

Version 1 evaluates stages in this order:

1. Execution completion. A stopped execution returns its execution code alone;
   discarded or nonexistent counters are not analyzed as if still observed.
2. The execution API's ordinary diagnostic-report validation, then model
   selection. Failure returns `incompatible_observation` or
   `incompatible_time_configuration` alone.
3. The estimator's policy validity, runtime/family/engine/dispatch binding, then
   controlled-dispatch host libc. Each failed stage stops before later checks.
4. Raw report eligibility. All applicable report codes are emitted, once each,
   in the table order below. Missing fields are not guessed as zero.
5. Exact arithmetic. Only otherwise eligible observations reach calculation;
   final int64 overflow returns `numeric_overflow` alone.

| Code, in canonical order | Meaning |
|---|---|
| `execution_cancelled` | The execution context was cancelled. |
| `latency_bound_missing` | The selected timing method requires the version 1 latency counter. |
| `latency_bound_invalid` | The observed bound is incompatible with the instruction total or counter version. |
| `execution_resource_exhausted` | A raw resource limit stopped execution. |
| `execution_failed` | Execution failed before a convertible completed result. |
| `execution_incomplete` | Execution did not provide a completed diagnostic observation. |
| `execution_not_complete` | Defensive fallback for another noncomplete internal state; remote envelope validation still rejects unknown states. |
| `incompatible_observation` | The API's complete-report contract is not satisfied. |
| `incompatible_time_configuration` | No valid selected policy applies. |
| `runtime_profile_mismatch` | Runtime ID, family, engine or dispatch differs from the selected policy. |
| `host_libc_mismatch` | The controlled dispatch does not report the required host libc. |
| `report_contract_mismatch` | Schema, metric, scope, authority or diagnostic contract is incompatible. |
| `incomplete_accounting` | The report is incomplete or carries an incompleteness reason. |
| `signals_observed` | At least one signal was observed; handler work is not silently treated as uninterrupted work. |
| `invalid_accounting_quality` | The quality label contradicts the signal count. |
| `incomplete_process_tree` | Process/image counts do not describe a complete tree. |
| `rep_disabled` | REP collection was disabled. |
| `counters_missing` | At least one required instruction/REP counter is absent. |
| `invalid_counters` | Instructions are zero or positive REP bytes have no REP entry. |
| `unsupported_rep_entries` | Unsupported REP work is present, so full REP coverage is unavailable. |
| `rep_telemetry_mismatch` | REP status, enabled mode or optional breakdown contradicts its totals. |
| `rep_breakdown_missing` | V2 lacks an observed operation breakdown. |
| `numeric_overflow` | The final rounded time cannot fit int64 nanoseconds. |

The object has exactly `version`, `eligible` and `reason_codes`; all three are
required when present. Nulls, unknown codes, duplicates, reordered codes,
contradictory eligible/reason states and noncanonical field spellings are rejected.
Older remote runners may omit the whole object. Absence stays absent and means
the explanation was not supplied; it is not successful qualification. The remote
control plane still recomputes the estimate and legacy unavailable string with
its own model. If eligibility is present, its complete ordered contents must also
match recomputation. Removing it cannot bypass the previous validation.

No eligibility is emitted without the explicit time request, and raw corpus
collection/fitting rejects unsolicited conversion metadata. `TimePolicy.Estimate`
and `EstimateWithEligibility` share one gate and exact-arithmetic implementation;
reference collection reuses the raw `Report.TimeConversionEligibility` checks.

For actual API corpus collection, exact train-only fitting and held-out error
gates, see [runtime reference-time qualification](durante-reference.md). The tool
exports a candidate policy only after the entire full corpus passes; its CI
smoke collection does not qualify production coefficients. CPP20 remains the
default corpus; CPython, PyPy and Rust require an explicit language selection and
produce separate runtime datasets. Passing one dataset does not qualify a common
model across those runtimes.

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
