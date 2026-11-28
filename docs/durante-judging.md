# Work-based judging (virtual milliseconds)

Status: **operator-gated, native by default.** Default images and requests do
not enable it. This is the first path that turns a Durante diagnostic
observation into an actual `Accepted` / `Wrong Answer` / `Time Limit Exceeded`
verdict instead of always returning `Container Initialization Failed`. It exists
because Cloud Run does not fix the processor model, so scheduled CPU time is not
a stable TLE criterion across instances. A work count, converted to virtual
milliseconds with an operator model, is stable across instances for a qualified
runtime.

Everything in [Durante integration](durante.md) still holds: counts are
`application_mutable`, the contract stays `diagnostic_only`, and nothing here
changes the raw-CPU, memory or integrity properties of the meter. Judge mode is
a verdict *policy* layered on the existing envelope and time conversion. It is
enabled only for families and runtimes the operator has qualified. A request can
ask for it explicitly, or the operator can select registered batch cases automatically.

## Restricting a dedicated judge to qualified cases

Set `AONOHAKO_DURANTE_JUDGE_CASES` on both the control plane and worker to
restrict explicit Durante judging to a finite list of tested deadlines.
Each entry binds the organizer's problem ID, exact stdin bytes, source
language, nominal CPU and wall deadlines, nominal memory limit and effective
work-clock model. For example, an entry for empty stdin has this shape (replace the
illustrative model digest with the binding from your calibration receipt).

```json
{
  "schema": 2,
  "cases": [
    {
      "problem_id": "contest/final/a",
      "source_language": "CPP20",
      "stdin_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "time_ms": 250,
      "wall_time_ms": 1000,
      "memory_mb": 256,
      "model_binding": "work-clock-v1:sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    }
  ]
}
```

Obtain the binding from `work_measurement_execution.clock.model_binding` in a trusted clock
receipt for the deployment's schema-2 model and effective runtime options.
The deployment validates every entry at startup. Schema 2 accepts up to 256 distinct cases
within 64 KiB. An explicit `{"schema":2,"cases":[]}` keeps the controller
running with zero eligible cases. Omitting the registry has different
semantics for explicit judge requests. Schema 1 requires at least one case. The raw resource policy must grant more CPU and wall time than
each registered CPU deadline and cover the registered wall and memory limits.
Set `wall_time_ms` to the value used in the qualified request. Zero matches a
request using the default wall guard. The raw envelope includes instrumented
memory and replaces these three limits during measurement. A matching entry
alone does not establish how much RSS belongs to the uninstrumented program.
When the selected metered peak (`memory_kb`) exceeds the registered
`memory_mb × 1024`, a scoped deciding judge withholds the competitive verdict,
including an otherwise valid AC, WA, virtual TLE or contestant runtime error.
A missing or zero peak also leaves the verdict unavailable.
It returns `Container Initialization Failed` without a score and retains the
clock, output check when available, peak RSS and termination source. An updated
controller checks the original receipt and score from a sealed worker before
withholding its verdict. The service performs one execution. An observed
nonzero exit within the nominal limit can still return a runtime error. A raw
watchdog stop retains its original cause.

The instrumented peak includes the runtime and the target. A native run can
fit its limit while the combined peak exceeds it. Sampling can also miss a
brief peak. Even a positive value at or below the limit cannot certify native
memory usage. Independent memory qualification remains necessary. Submission
output and workspace limits still apply. [Paired native and Durante GCE measurements](durante/gce-v13-memory-boundary/README.md)

This scope supports standalone CPP20 batch requests with one inline static
x86-64 executable, inline input and an ordinary expected stdout comparison.
The organizer supplies the expected output. The registry does not key on the
submission's ELF hash, so qualification should exercise independent solutions,
including wrong answers and solutions that cross the clock limit. Explicit
judge requests outside the configured scope are rejected. Ordinary requests
retain their native verdict by default, and an absent registry preserves the existing
operator opt-in behavior. Register only cases actually qualified for that
specific model and deadline.

### Automatically judge registered batch submissions

Set `AONOHAKO_DURANTE_JUDGE_AUTO=on` with `AONOHAKO_DURANTE_JUDGE_MODE=on`
and a validated schema-2 `AONOHAKO_DURANTE_JUDGE_CASES` registry to apply the scope
to ordinary requests on `/execute`. Submit the usual problem ID, contestant
ELF, input, expected stdout and nominal limits. A match runs once with the
operator's raw resource envelope and the registered deciding clock. The
response carries the same `work_measurement_judgment` and clock receipt as
an explicit judge request. A clock failure follows the explicit judgment
rules; it never silently reruns the submission under another clock.

Unregistered inputs, other nominal CPU, wall or memory limits, URL-backed artifacts, `ignore_tle`
and other ineligible requests follow the native path. Explicit measurement,
shadow and peer requests retain their existing modes. If automatic observation
is also enabled, it applies to eligible native submissions. The default for
`AONOHAKO_DURANTE_JUDGE_AUTO` is off.

Schema 1 remains available for explicitly requested judging. It records only
the nominal CPU deadline. The updated controller requires schema 2 when
automatic judging is enabled and reports a configuration error at startup if
given schema 1. Preserve the original model binding when adding the wall and
memory limits from the qualification measurements.

For the frozen GCE v12/v13 pilots, generate the settings for an updated
controller built for the matching engine. Run this from the repository root.

```sh
python3 -B docs/durante/gce-qualified-judge/controller_env.py --engine v13
```

The v12 and v13 controller exports now contain zero eligible cases. Correct
C++20 submissions for each former pilot input repeatedly passed native judging
and timed out under the live clock. Rebuild the controller with empty-registry
support and apply a freshly generated environment to retire old entries.
Earlier controllers reject an empty registry at startup. Each sealed worker
keeps its historical schema-1 settings behind the controller.
[Range minimum](durante/gce-original-range-min-boundary/README.md) ·
[Shortest paths](durante/gce-shortest-paths-boundary/README.md) ·
[Inversions](durante/gce-original-inversion-boundary/README.md) ·
[Mixed-version HTTP verification](durante/gce-qualified-judge/withdrawal/README.md)

Use `--engine v12` for the v12 worker. The helper leaves automatic judging off.
Enable it on the updated controller and forward to the matching sealed worker
with its original schema-1 settings, automatic judging off and ingress
restricted to that controller. The sealed server's automatic path predates
nominal memory and wall checks. Public requests routed directly through that
path could select Durante for unqualified limits. An updated worker configured
with schema 2 also checks the forwarded limits.

Enable automatic selection at the public control plane. Its worker needs the
matching judge configuration and case registry to verify a forwarded decision.
The worker needs automatic selection only when it also serves direct ordinary
requests. Upgrade forwarding control planes before enabling automatic selection
on a shared worker. Current controllers mark native forwards. An embedded
worker with trusted runner ingress preserves their selected route even when
its own registry differs.

The mixed-version HTTP test routes ordinary requests from the new controller
to the worker used in the original pinned GCE pilot. Its three then-registered cases
produced AC for the fast solution, WA for the incorrect solution and an
in-flight Durante TLE for the slow solution. Six default or unregistered
requests used native judging, and a worker without the scope refused the
forward. The test uses the same frozen model as the GCE report.
[Qualification and eligible deadlines](durante/gce-qualified-judge/README.md) ·
[Sixteen HTTP receipts](durante/scoped-auto-http/receipts.json.gz) ·
[Reproducible HTTP test](../scripts/verify_durante_scoped_auto.py)

## Automatic observations alongside native grading

Set `AONOHAKO_DURANTE_OBSERVE=on` to collect work for supported submissions
using ordinary requests to `/execute`. The operator enables the measured
families in `AONOHAKO_DURANTE_FAMILIES` and supplies
`AONOHAKO_DURANTE_RESOURCE_POLICY`. Its `max_limits` bound one additional
contestant execution. The native run still sets the verdict, score, CPU time,
wall time and peak memory. End-to-end request latency includes the additional
execution, and only the primary produces streamed events. This mode can run
without a time model or a per-submission measurement flag.

The optional `work_measurement_observation` field reports one of these outcomes.

| Result | Evidence |
|---|---|
| `state: complete`, `output_check: native_output` | The native contestant finished with AC or WA. The replay produced the same judged output bytes before truncation. For standalone SPJ the primary ran the checker once, and the replay measured only the contestant. |
| `state: complete`, `output_check: expected_output` | Measured native CPU exceeded the limit on an ordinary stdout case. The replay completed within its separate raw envelope and passed the normal comparison against a supplied nonempty answer. The original verdict remains TLE. |
| `state: unavailable` | The reason identifies a skipped or incomplete observation. The enclosing response carries the native result. |

Complete observations include `report` with the family's diagnostic work
counters and installation identity. When using `stdin_url`, the primary
and replay share one snapshot and the receipt includes its SHA-256. The
runtime comparison requires a matching configured processor and dispatch
profile, optional latency counter and effective runtime determinism options.
A submission that produces different judged bytes during ordinary repeats
or in the replay gets an unavailable observation.

Configure the control plane and execution worker with matching observation
settings and raw budgets for remote execution. A control plane receiving a
native result from an older worker can return it with an unavailable observation.
A malformed optional receipt also leaves a valid primary intact.
Explicit measurement, time, shadow and judge requests keep their existing
protocols. The automatic path is off by default.

Automatic replay requires a contestant with a registered, enabled source
language and resolved artifacts. Standalone interactive jobs can collect a
contestant observation after the native exchange. Communication jobs support
explicit per-role observations. Standalone replays skip network-enabled
submissions, sidecar artifacts and entry modes. Pipeline V1 and legacy
two-step jobs have per-stage observations. JVM measurement still requires sandbox
qualification. Native wall-time or memory stops do not establish that a
contestant finished or exhausted CPU. Their optional observation is unavailable.

The HTTP verification script exercises direct and forwarded requests in an
offline validation container. It compiles real submissions, checks native
results, validates Durante receipts and verifies that the SPJ executes once.
[Verification harness](../scripts/verify_durante_observation.py)

### Standalone interactive observations

With automatic observation enabled, the native contestant and interactor
finish their exchange and establish the verdict. The worker then replays
the contestant with a private copy of the delivered dialogue. The same
bounded recording and byte comparison apply to
[Pipeline V1 interactive stages](#pipeline-v1-interactive-observations).
A normal contestant exit and a matching replay can produce counters for
both an accepted exchange and an exchange the interactor judged wrong.
If the interactor ends the session while the contestant is still running,
the native verdict is available with an unavailable contestant observation.

The version 2 `work_measurement_observation` uses `state: interactive`.
An executed contestant has one row identified by `step_id: contestant`
and `program_id: contestant`. A complete row carries the diagnostic report
and `output_check: interactive_transcript`. Use the native `contestant`
row in the response's top-level `steps` to pair that report with contestant
CPU time. The top-level CPU time also includes the interactor.

For remote workers, update the controller first so each forwarded request
carries its replay budget. A zero budget suppresses replay even on a worker
with observation enabled. A direct request without the forwarding header
uses the worker's local observation setting.
[Standalone interactive HTTP verification](../scripts/verify_durante_interactive.py)

### Pipeline V1 batch observations

On an embedded helper worker, including one behind a negotiated remote
control plane, the same operator setting collects one
diagnostic replay for each eligible completed batch stage, up to the
pipeline's two-stage limit. The final diff or special judge runs on the
native artifacts. After native judging stops, the worker reconstructs the
stage input from its resolved resources and immutable native handoffs, applies
the configured raw limits, and compares the replay's full captured stdout
and any declared participant file with the native bytes. The final judge runs once. A replay cannot supply input to another stage.

The response carries a version 2 `work_measurement_observation` with state
`pipeline`. Its `steps` are in execution order. Each row identifies its
`step_id` and `program_id`. A complete batch row has `output_check: native_output`
and a diagnostic report. An ineligible, incomplete or differing replay has an
unavailable row. An unstarted stage has no row. Resource bytes, output hashes,
artifacts and checker messages stay private. Optional replays add to response
latency. The enclosing verdict, score and native time refer to the primary run.

Each native `steps[]` row includes `cpu_time_ns` when precise timing is
available, plus `raw_cpu_time_ns` when normalization records that counter.
The top-level precise total requires corresponding readings from every executed
stage. The existing per-stage millisecond rounding is preserved. Consumers
can join the diagnostic rows to the native timings by step and program ID.
Configure observation on both the control plane and its authenticated worker
to receive verified per-stage reports through the remote API. A forwarded
request bounds the total time for optional replays. Turning observation off on
the controller suppresses replays even when the worker has observation enabled.
[Pipeline V1 request and response contract](pipeline-v1.md)

### Pipeline V1 interactive observations

For an interactive stage, the worker can replay the contestant alone after
native judging finishes. During the native session it records successfully
delivered pipe writes and EOF in a request-private buffer. The buffer is
limited to 1 MiB across both directions and 4,096 data writes. The replay
uses a live pipe. A scripted peer supplies the recorded judge prompts in
order and compares the contestant's responses as it reads them. Each later
prompt waits for the recorded responses from the preceding turn. The worker
checks the full contestant stdout and any declared participant file. The
original interactor and final judge each run once.

A complete contestant observation has `output_check: interactive_transcript`.
It covers an accepted exchange and a cleanly completed exchange that the
interactor judged wrong. Both require a normal contestant exit, an
unambiguous captured exchange and a matching replay. Overlapping writes,
partial deliveries, peer cancellation, exceeded bounds and differing output
leave the row unavailable. The public result contains the submitted program
IDs and diagnostic counters. The recorded conversation stays private.
During replay the worker checks the contestant's stdout pipe before and
after issuing a prompt. Already queued answers or a preceding reply that
fills the pipe make the ordering ambiguous and leave the row unavailable.
The native stage's `cpu_time_by_role` contains separate `contestant` and
`interactor` CPU measurements, each with its own program ID, exit status and
available nanosecond counters. The stage total includes both roles. Pair the
contestant receipt with its own native CPU measurement when evaluating timing
models. The raw replay does not reproduce native wall-clock timing.

### Legacy two-step observations

With automatic observation enabled, a `programs` and `steps` request can
return a version 2 receipt with `state: steps`. Its rows carry the original
`step_id` and `program_id` in execution order. The worker finishes the native
handoff and final diff or SPJ before replaying any stage. Each eligible row
compares the full native stdout and, when used, the selected output file.
A verified row can accompany a final Wrong Answer. A failed handoff has an
unavailable row, and an unstarted consumer has no row.

The replay uses the original inline input, assembled stdin parts or private
handoff. For direct `stdin_url`, the worker keeps up to 8 MiB of the bytes
read during the native download. A larger or incomplete stream leaves
that stage unavailable while the native run uses its normal download limit.
Network-enabled stages and final stages using sidecars or `ignore_tle` skip
observation. Results retain the existing legacy output and timing semantics.

On a remote control plane the optional replay budget travels with the original
request. An older worker can still return its native verdict, with a version
2 unavailable observation on a controller expecting stage evidence. Update
controllers before enabling observations on newer workers so controllers pass
their configured budget. [HTTP qualification harness](../scripts/verify_durante_legacy_steps.py)

## When it applies

An ordinary request opts in with `work_measurement_judge: true`. This example
supplies explicit raw safety limits.

```json
{
  "lang": "CPP20",
  "work_measurement": true,
  "work_measurement_judge": true,
  "work_measurement_time": true,
  "limits": {"time_ms": 1000, "wall_time_ms": 2000, "memory_mb": 128},
  "work_measurement_limits": {"cpu_time_ms": 8000, "wall_time_ms": 16000, "memory_mb": 512}
}
```

- The operator must enable `AONOHAKO_DURANTE_JUDGE_MODE=on`, list the judged
  families in `AONOHAKO_DURANTE_JUDGE_FAMILIES`, enable the family, and supply a
  resource policy, a time model (`AONOHAKO_DURANTE_TIME_POLICY` or
  `AONOHAKO_DURANTE_TIME_MODELS`) and a runtime image, exactly as reference-time
  conversion already requires.
- Judge mode also requires `AONOHAKO_DURANTE_CPUID_PROFILE`. Without a fixed
  virtual processor the count follows each instance's CPU, which is the
  instability judge mode exists to remove, so startup rejects judge mode
  without it. The model must be qualified under the same profile (its
  `cpuid_profile` binding), and the observation must carry the matching
  receipt; otherwise the estimate is unavailable and the verdict is
  `Runtime Error`.
- Judging `cpython`, `pypy` or `javascript-wasm` also requires
  `AONOHAKO_DURANTE_RUNTIME_DETERMINISM=v1`. Without it, the per-process hash
  seed of CPython and PyPy and V8's background compilation move counts between
  identical runs ([measurements](durante-runtime-options.md#runtime-determinism)),
  so startup rejects those judged families. The model
  must be a schema 2 binding of the pinned descriptor, and the observation
  must carry `runtime_determinism`; otherwise the estimate is unavailable and
  the verdict is `Runtime Error`.
- The `work_measurement_judge` flag uses the ordinary single-program resource
  envelope. Interactive and communication requests can select a deciding clock
  for each role. [Live role clocks](#live-clocks-for-interactive-and-communication-roles)
- `work_measurement_judge` requires `work_measurement_time: true` and
  a matching time model. Terminal-only judgments also require explicit
  `work_measurement_limits`.
- The family must be both measurement-enabled and judge-enabled. A
  `work_measurement_judge` request for a family that is measured but not listed
  in `AONOHAKO_DURANTE_JUDGE_FAMILIES`, or when judge mode is off, is rejected at
  admission (`Container Initialization Failed` with the reason), rather than
  silently downgraded to a diagnostic response.

The instrumented target executes once, under the raw envelope limits, so
instrumentation overhead (about 3x CPU and extra RSS) is not charged to the
student. Short-case repeat sampling and startup CPU normalization are bypassed:
the verdict comes from the work count, not from scheduled CPU time.

## The `nominal` time limit is virtual milliseconds

`limits.time_ms` is the problem's time limit **in virtual milliseconds for the
configured runtime**, not host CPU milliseconds. The operator's time model
converts the work count to nanoseconds:

```
estimated_ns = ceil((instructions * instruction_ns_numerator
                   + rep_bytes    * rep_byte_ns_numerator) / ns_denominator)
```

(or the separate MOVS/STOS formula). The latency model substitutes
`latency_bound.cycles_x16 * latency_x16_ns_numerator` for the instruction term. The comparison is exact in nanoseconds:

```
over_time = estimated_ns > limits.time_ms * 1_000_000      // strict >
```

Equality is not an exceedance. The rounded `milliseconds` field is reported but
never used for the comparison. Because the unit is defined by the model, the
problem's limit, the model, the engine profile, the dispatch profile, the
runtime image and the compiler are one bound bundle: changing any of them
requires re-deriving limits or re-qualifying the model. The model is not
calibrated yet (see [reference-time qualification](durante-reference.md)); until
it is, judge mode must not be enabled in production.

## Online Durante clock

For an ordinary `work_measurement_judge` request, the worker enforces the
problem's nominal time limit during execution when the operator has enabled
deciding verdicts, selected integrity mode `off`, and bound an instruction/REP
time model to the effective runtime options (registry schema 2). The worker
applies the same weights and denominator used in the final estimate. It
compares the published lower bound directly against
`limits.time_ms * 1,000,000 * ns_denominator` before rounding. An exact tie
can continue. The model may use distinct MOVS and STOS weights.

Omitting `work_measurement_limits` uses `AONOHAKO_DURANTE_RESOURCE_POLICY.max_limits`
for CPU, wall time and memory. A request can supply a smaller explicit envelope
within these caps. The model must match the effective raw memory and runtime
options. Both raw time ceilings must exceed `limits.time_ms`. Operators should
allow enough headroom for instrumentation overhead, idle programs and blocked
pipes. The controller forwards the effective limits to the worker and verifies
them against `work_measurement_execution.raw_limits` in the response.

A verified terminal crossing can exceed the range of a signed 64-bit
nanosecond estimate. The ordinary and role clocks record the crossing and
`numeric_overflow`, and omit the estimate. Role clocks preserve the verdict
rules for nonzero exit codes and trusted checker failures.

Schema 1 and legacy time policies, latency-bound models, shadow collection and
plausibility checks continue to use their terminal observations.

| Outcome | Execution evidence |
|---|---|
| The virtual clock crossed during execution | `budget_exceeded`, source `durante_work_clock`, TLE with an observed prefix and no complete counts or total time estimate. |
| The program exited over budget between samples | `budget_exceeded`, source `durante_work_clock_final` with a complete report after all three live counts match the terminal counts. |
| The program finished within budget | The existing output and eligibility checks decide the verdict using the complete terminal report. |
| A raw CPU, wall or memory watchdog fired | `resource_exhausted` with the watchdog's termination source and an unscored result. |

The clock receipt identifies the metric, nanosecond budget and resolved model
binding. The remote controller sends this binding before execution and checks
the returned component counters against its own model. The worker rejects a
different binding. Operators must qualify models for the current engine
profile and runtime image. The client batches work publications, and detection,
signalling and reaping take time; a stop can overshoot its budget.

An ordinary diagnostic request can instead set `"work_measurement_clock":
{"version": 1, "budget": 2000000}` together with `work_measurement: true` and
`work_measurement_limits`. This budget counts instructions plus requested MOVS
and STOS bytes directly. An exhausted diagnostic budget supplies execution
evidence and an unscored diagnostic result. Judge requests always derive their
clock from the operator's model. Each clock covers one owned role and its process tree. An interactive or
communication request can select the role clocks below.

The HTTP harness grades a real compiled C++ program through embedded and
forwarded execution using test-only coefficients. It checks normal completion,
a busy loop, a sleeping process and a mismatched downstream model. On v12 and v13
it also uses the same finite workload with a handled signal or a REP CMPS
instruction to verify a raw crossing and an unavailable model-weighted clock.
A weighted crossing already observed before the signal or instruction still
describes a valid execution prefix.
Both the work clock and peer-clock HTTP verifiers enable the v1 latency bound by
default. Run them again with `--no-latency-bound` to exercise the ordinary
instruction and REP clock. Each verifier checks the selected mode in completed
measurement reports.
[Work clock HTTP verification](../scripts/verify_durante_work_clock.py)

### Live clocks for interactive and communication roles

Set `work_measurement: true` and `work_measurement_peer_time.version: 2`.
Supply one `time_ms` virtual budget for each role. Interactive roles are
`contestant` and `interactor`; communication roles are `participant` with a
zero-based `participant_index`, and `manager`. The program ID must match the
request, including when the same executable runs in multiple seats.

```json
{
  "work_measurement": true,
  "work_measurement_peer_time": {
    "version": 2,
    "budgets": [
      {"role": "contestant", "program_id": "contestant", "time_ms": 100},
      {"role": "interactor", "program_id": "interactor", "time_ms": 500}
    ]
  }
}
```

The worker resolves the schema 2 model for each role's actual language,
image, dispatch, virtual processor and runtime options before launch.
Every role must be enabled for deciding judgments with integrity mode
`off` and an instruction/REP model. Give the role's ordinary CPU and wall
limits enough headroom to run the instrumented process. Both must exceed
that role's virtual budget and fit the operator's resource policy. Interactive
requests can set an explicit wall limit on each role. Communication uses the
runner's wall policy, with each online clock's wall allowance set to its
role's raw CPU limit in milliseconds. Admission checks the sum of role memory
limits against the runner's capacity at its configured concurrency.

Each `work_measurements[].time_budget` receipt includes the virtual limit,
model binding, raw safety limits, raw usage and role clock evidence.
A proven contestant cutoff can decide TLE. An interactor or manager clock
failure leaves the result unscored. An interactive AC requires both roles to
complete within budget. A verified interactor WA can stand when the coordinator
subsequently cancels the contestant. In a competitive match an independent
participant fault can forfeit that seat under the match protocol. A peer
cancelled by the coordinator gets a separate reason; its cancellation
cannot establish a budget crossing. The controller verifies the per-role
evidence when a worker executes the exchange remotely. Version 1 remains
available for post-exit diagnostic comparisons.

[Peer-clock HTTP verification](../scripts/verify_durante_peer_clocks.py)

### Standalone special judges

A standalone SPJ can request version 2 with `contestant` and `checker`
as both the role and program IDs. Give each role a virtual `time_ms` budget.
The contestant runs under the request's raw limits and the checker under
`spj.limits`. Their wall and CPU limits must each exceed their virtual limit.
They run in sequence, so admission reserves the larger of their two memory
allowances. Both programs need a declared source language with a qualified
model. The checker's runtime descriptor includes its input, output and answer
file operands.

The worker runs the checker once, after the contestant exits successfully
within budget and supplies its judged output. A verified contestant cutoff
gives TLE and the checker receipt records `role_not_started`. A checker cutoff,
raw watchdog stop or invalid score returns an unscored initialization failure.
A completed checker supplies its normal acceptance, rejection or partial score.
The controller verifies the two receipts and checks any emitted score against
the parsed checker score. The checker files and its raw output stay on the worker.
[Special-judge HTTP verification](../scripts/verify_durante_spj_clocks.py)

### Pipeline V1 and legacy two-step grading

To opt in, set `work_measurement` and supply a version 2 budget for every
stage process. Use the declared step ID and program ID for each contestant and
interactor. A final special judge uses role and program ID `checker` and omits
`step_id`. A built-in diff runs without a process or a clock. For example,
these budgets cover two sequential uses of the same solver and one final SPJ.

```json
{
  "work_measurement": true,
  "work_measurement_peer_time": {
    "version": 2,
    "budgets": [
      {"step_id": "prepare", "role": "contestant", "program_id": "solver", "time_ms": 100},
      {"step_id": "finish", "role": "contestant", "program_id": "solver", "time_ms": 500},
      {"role": "checker", "program_id": "checker", "time_ms": 250}
    ]
  }
}
```

Each role keeps its step's effective raw CPU, wall and memory ceilings. Set the
raw time ceilings generously enough to accommodate instrumentation. Admission
checks the model, raw ceilings and peak concurrent memory before launching the
first stage. An interactive pair shares one stage and needs memory for both
roles simultaneously.

The stage that exhausts its virtual budget reports the responsible role.
Execution stops before writing the next handoff or invoking the final checker.
Later roles have `termination_source: stage_not_started` and no measured
CPU, work progress or time estimate. The controller checks the ordered stage
receipts against the executed steps. Pipeline V1 keeps the contents of its
resources, handoffs, checker files and diagnostic output private.
[Stage-clock HTTP verification](../scripts/verify_durante_stage_clocks.py)

## Verdict rules

The verdict uses the finalized execution state and, for completed observations,
the output comparison and time estimate.

| Envelope state | Output | Time estimate | Verdict |
|---|---|---|---|
| `complete` | matches | available, `estimated_ns ≤ limit` | **Accepted** |
| `complete` | matches | available, `estimated_ns > limit` | **Time Limit Exceeded** |
| `complete` | mismatch | (not evaluated) | **Wrong Answer** |
| `complete` | matches | unavailable | **Runtime Error** |
| `budget_exceeded` | — | validated clock crossing | **Time Limit Exceeded** |
| Durante session preparation fails before launch | — | — | **Container Initialization Failed**, unscored, without a judgment receipt |
| `resource_exhausted` | — | — | **Container Initialization Failed**, unscored |
| `incomplete`, clean exit with an unavailable weighted clock | — | unavailable | **Container Initialization Failed**, unscored |
| Other `incomplete`, `failed` or `cancelled` | — | — | **Runtime Error** |

- **Worker setup failure.** A missing Durante launcher, client or patched runtime
  returns the worker's preparation error. An updated controller also withholds
  a legacy worker's generic failed runtime verdict when it carries no meter
  report, exit status, CPU sample or termination source.
- **Wrong Answer** uses the existing output oracle (stdout or the single file
  output), before any time comparison. A nonzero exit on an output match is a
  runtime error at the ordinary layer and produces `Runtime Error` here too.
- **Terminal-only time estimate unavailable → Runtime Error.** A complete, output-matching run
  whose counts cannot be converted — signals observed, partial REP coverage,
  numeric overflow, or an incompatible report — is reported as `Runtime Error`
  with the eligibility reason, not silently accepted and not called TLE. This is
  the deliberate choice for this rollout: an unjudgeable observation is a system
  condition to retry or investigate, never a free `Accepted`. Signal-driven
  runtimes such as Go are therefore unjudgeable today; enable judge mode only for
  families whose counts convert (C/C++ first).
- **Raw resource exhaustion.** A raw CPU, wall or memory stop ends measurement
  before Durante can produce a virtual time. Judge mode returns the existing
  unscored `Container Initialization Failed` status with
  `work_measurement_judgment.state = "resource_exhausted"`. The termination
  source identifies the resource to review. Shadow comparisons record this
  case as unavailable. A virtual TLE requires a validated work-clock crossing
  or an eligible, over-budget terminal estimate.
- **Weighted clock unavailable on clean exit.** A program can handle a signal or
  finish after CMPS/SCAS with exit code zero. When the worker cannot reconcile
  its weighted clock with the terminal counts, it reports an unscored
  initialization failure with termination source `durante_work_clock_final_unavailable`. If the program continues running, its independent
  resource watchdogs still apply.
- **Other incomplete, failed or cancelled executions.** Launch failure,
  client/collection failure, a forced stop, or cancellation cannot yield a
  trusted count, so the verdict is a (retryable) `Runtime Error`, never AC/TLE.

The response carries `work_measurement_judgment` with the chosen verdict, the
reason, the virtual-millisecond estimate, the integrity decision, and the raw
diagnostic execution block. Unlike the pure diagnostic envelope, judge mode sets
`status` to the real verdict (`Accepted`, `Wrong Answer`, `Time Limit Exceeded`
or `Runtime Error`) for judged runs. A raw resource stop or an unavailable
weighted clock on clean exit carries an unscored initialization failure. A
scoped deciding judge also requires a positive metered peak at or below
the registered nominal memory limit before assigning a competitive verdict.

## Counter integrity (threat model is a flag)

The counter lives in the submission's address space and can be altered by the
submission (see [SECURITY.md](https://github.com/seo-rii/durante/blob/main/SECURITY.md)).
A submission can only *lower* its own count to turn a TLE into an AC; raising it
only hurts itself. Judge mode selects how much that risk is mitigated with
`AONOHAKO_DURANTE_JUDGE_INTEGRITY`:

- **`off` (default).** Counts are trusted as reported. Suitable for cooperative
  settings (classes, practice, self-reported contests) where submissions are not
  adversarial against the meter. `integrity_mode: "off"`.
- **`plausibility`.** The instrumented run's own raw CPU time is used as an
  independent shadow. A truthful run spends raw CPU roughly proportional to its
  work (instrumentation inflates it, so raw CPU is an upper-bounded multiple of
  the virtual estimate). If the reported work converts to far less time than the
  CPU actually burned, the counts were probably tampered:

  ```
  implausible = raw_cpu_time_ns > integrity_ratio * estimated_ns
  ```

  `integrity_ratio` is `AONOHAKO_DURANTE_JUDGE_INTEGRITY_RATIO` (the maximum
  believable ratio of instrumented raw CPU nanoseconds to virtual nanoseconds,
  covering instrumentation overhead and host variance with margin). An
  implausible observation is **not** accepted: it becomes `Runtime Error` with
  `integrity_mode: "plausibility"` and `integrity_flagged: true`, for manual
  review or re-judging. This is a heuristic, not a proof: a syscall-heavy program
  (high system CPU, low user-space instructions) can be flagged although honest,
  so `plausibility` suits screening, not final adversarial grading. It never
  turns a legitimate pass into a different pass and never makes a tampered run
  accepted; its only effect is to withhold AC/TLE from an implausible run.

Neither mode makes the count a trusted verdict in the sense of
[SECURITY.md](https://github.com/seo-rii/durante/blob/main/SECURITY.md).
Adversarial, prize-bearing grading still needs an out-of-process counting
backend, which does not exist yet. `plausibility` only raises the cost of the
one profitable attack (under-counting) by requiring the CPU trace to agree.

## Shadow comparison

Before judge mode decides anything, run it in shadow on real submissions.
`AONOHAKO_DURANTE_JUDGE_MODE=shadow` configures the judge machinery (families,
integrity, CPUID profile, time model) but rejects `work_measurement_judge`
requests, so no client can obtain a deciding work verdict yet. `on` allows both.

A request opts in per testcase:

```json
{
  "lang": "CPP20",
  "work_measurement_shadow": true,
  "limits": {"time_ms": 1000, "wall_time_ms": 2000, "memory_mb": 128},
  "work_measurement_limits": {"cpu_time_ms": 8000, "wall_time_ms": 16000, "memory_mb": 512}
}
```

The service first runs the request exactly as an ordinary request: CPU-time
TLE, normalization and short-case sampling, memory, output judging,
stream events and hooks are unchanged. That result is the response's `status`,
`score` and resource values. It then runs a separate judge-mode execution of the
same input under `work_measurement_limits` (no logs or images are emitted) and
adds only `work_measurement_shadow`:

| Field | Meaning |
|---|---|
| `agreement` | `agree`, `disagree`, or `unavailable` |
| `primary_status`, `primary_cpu_time_ms` | The authoritative verdict and its CPU time |
| `shadow_status` | The judge-mode verdict for the same input |
| `stdin_sha256` | SHA-256 of the shared input when the primary fetched `stdin_url` and the shadow ran |
| `unavailable` | Why no comparison exists (mode off, family not judged, cancelled, remote result rejected) |
| `response` | The shadow execution's judgment, envelope, time estimate and Durante report, without stdout/stderr/sidecars |

The request must be an ordinary single-program request; `work_measurement`,
`work_measurement_time`, `work_measurement_judge` and peer budgets are rejected
with it, because the primary must stay unmetered. Operator-side gates (mode
`off`, a family not in `AONOHAKO_DURANTE_JUDGE_FAMILIES`) never reject the
request: the primary runs and the shadow is `unavailable`. The shadow costs one
extra instrumented execution (about 3x CPU) per opted-in testcase, sequentially
after the primary; sample submissions rather than shadowing everything. The runner fetches a `stdin_url` once when the primary opens it. Each short-case
repeat and the shadow reads an independent handle to the same bytes. With a URL
request an older remote runner's comparison is unavailable if it cannot identify
the shared input. Inline stdin is already part of the request.

A remote control plane validates the primary as an ordinary result and
recomputes the shadow comparison from the returned raw observation with its own
time model and judge policy. A downstream runner that ignores the option, or a
shadow block that fails recomputation, does not fail the response: the shadow
becomes `unavailable` with the reason and the validated primary verdict is
returned.

Use `go run ./cmd/durante-shadow-summary cases.jsonl` to summarize a sample.
Supply one JSON object per testcase with a stable `problem_id` (a string or
unsigned integer) and the control plane's final `/execute` result in
`response`. Standard input is also supported. Use the same observation period,
cohort and model for each comparison. The summary includes problem IDs, counts and the operator policy identities
used for CPU comparisons. Source code, submission IDs, program output and free-form reasons are
unnecessary.

For archived v5 time comparisons, set the optional row field `stdin_source` to
`"inline"` only when the original request confirms inline stdin (including
empty input). Use `"url"` for URL inputs and omit the field when the source is
unknown. The v5 runner fetched URLs separately for the two executions, so
the summary counts v5 time verdicts with URL or unknown input as unavailable.
Raw resource stops are still counted by cause. Current workers share one input
and their time comparisons do not require this field. When supplied, `"url"`
requires the current worker's shared SHA-256 receipt and `"inline"` requires
that receipt to be absent.

For the existing verdict-level counts, divide `primary_ac_shadow_virtual_tle` by
`comparable_primary_ac` to measure how often a completed, eligible shadow
changes a primary AC to TLE. Divide `primary_tle_shadow_ac` by
`comparable_primary_tle` for the reverse direction. Report zero denominators
as unavailable. `testcases`, `primary_ac` and `primary_tle` include the entire
sample. `raw_stops_by_source` counts raw resource stops, including legacy
responses labeled TLE. Also report `missing_shadow`, `unavailable` and `other`
alongside the disagreement rates so coverage is visible.

For CPU-limit accuracy, use each problem's `cpu_comparison.cohorts`. Each
cohort reports four counts. Divide `native_ac_shadow_tle` by
`native_ac_shadow_ac + native_ac_shadow_tle` for the false-TLE rate. Divide
`native_tle_shadow_ac` by `native_tle_shadow_ac + native_tle_shadow_tle` for
the false-AC rate. A zero denominator means the rate is unavailable.

Each cohort also reports `completed_native_time_accuracy`. Its three counts
show whether the shadow estimate was more than 20% below the native CPU time,
within ±20% (inclusive), or more than 20% above it. The comparison uses the
recorded native nanoseconds after any validated normalization and the recomputed
shadow estimate. It includes completed native AC runs even when the shadow
exceeded the nominal deadline. Their total equals `native_ac_shadow_ac +
native_ac_shadow_tle`. Native CPU stops can truncate the program and contribute
only to the verdict counts. A zero total gives no timing accuracy rate.

A native AC needs a normal exit, the same output comparison as the shadow
and positive measured CPU nanoseconds within the limit. A native TLE needs
a measured CPU stop and a reading strictly over the limit. A native output AC
carries no score. A native TLE may carry the binary output credit returned by
`ignore_tle`. A shadow AC must carry score 1 and a virtual TLE must omit the
score. A nonempty shared-input receipt must be a canonical SHA-256 digest,
including when the archive omits `stdin_source`. Each CPU reading must
agree with its rounded milliseconds. For normalized CPU, the raw reading
must reproduce the recorded conversion. The command recomputes the virtual
time from the model coefficients and observed work using the current engine's
estimator. It checks the canonical runtime image ID against its source family,
the fixed CPUID profile, and the effective runtime determinism of its launcher.
Older records can still contribute to verdict-level counts.

The SHA-256 cohort keys cover the displayed policy, including its weights,
reference, runtime options, installation, CPU features and dispatch profile,
as well as the native normalization reference when present. Use a consistent
native reference host for unnormalized cases. The archive alone cannot
establish which physical worker ran them. `cpu_comparison.excluded` counts
every remaining testcase once, including wall-time and signal stops, missing
precise timing, unavailable shadows and estimates the current estimator could
not verify. Report these exclusions with the rates to show coverage.

## Remote control plane

A judge-mode request forwarded to a downstream runner is independently
re-validated by the control plane: it recomputes the verdict from the returned
raw observation and its own configured time model and integrity policy, and
rejects a result whose verdict, estimate, integrity decision or envelope
metadata does not match, or that an older runner returned without the judgment
block. The downstream runner image and model must match the control plane's, as
with reference-time conversion.

## Rollout

1. Qualify a time model for the exact `CPP20@<image digest>` runtime with a held
   out error gate ([reference-time qualification](durante-reference.md)). Judge
   mode must stay off until a model passes.
2. Choose the CPUID profile (`x86-64-v2` needs only SSE4.2-era features; the
   client refuses to start on a host that lacks one, so a mismatch fails closed
   rather than miscounting) and collect the qualification corpus with it, so the model and
   the judged counts see the same virtual processor
   ([CPUID profiles](https://github.com/seo-rii/durante/blob/main/docs/CPUID_PROFILE.md)).
   For CPython, PyPy and Node, set `AONOHAKO_DURANTE_RUNTIME_DETERMINISM=v1` before
   collecting, so the qualified descriptor is the pinned one
   ([Runtime determinism](durante-runtime-options.md#runtime-determinism)).
3. Run judge mode in shadow (`AONOHAKO_DURANTE_JUDGE_MODE=shadow` and
   `work_measurement_shadow` on a sample of real submissions; see
   [Shadow comparison](#shadow-comparison)), and measure the disagreement rate
   against the existing CPU-time verdict before it decides anything.
4. Enable it per problem set, starting with C/C++, `integrity_mode: off` for
   cooperative settings and `plausibility` where submissions may be adversarial.
5. Keep raw CPU/wall/memory limits active as independent safety nets; a work
   verdict never removes them.

Memory judging and runtimes whose counts do not convert (signal-driven, e.g. Go) are out
of scope for this path and remain documented limitations.

## Time attribution by execution mode

Ordinary C++ shadow comparisons are the first qualification cohort. Interactive
and communication requests support two versions of `work_measurement_peer_time`.
Version 1 compares each eligible completed role's estimated time with its budget
after exit. Version 2 also checks the role's clock during execution. A verified
live cutoff carries a partial work count and no final time estimate.

| Mode | Submission time to compare | Operator or helper work |
|---|---|---|
| Interactive | Compare the contestant process tree against its own limit. | Attribute interactor work to its own budget. Record coordinator cancellation as incomplete. |
| Communication v1/v2 | Compare each participant seat separately, including seats executing the same program. Key the result by program ID and participant index. | Attribute the manager to its own budget. Keep the manager's score and match result separate from time diagnostics. |
| Special judge | Measure the contestant up to completion and compare with its contestant limit. | Use the SPJ's limit and workspace for checking. A checker failure belongs to the checker. |
| Pipeline | Match each executing participant to its step, program and step limit. Interactive steps also need a separate interactor observation. | Record artifact transfer and the final judge separately. Mark later, unstarted steps as such. |
| Multi-step | Compare each executed step with its effective step limit. A displayed total may sum completed sequential steps while retaining their individual outcomes. | Account for handoff and coordination separately. Omit a credited total if any required step lacks an estimate. |

All these clock paths require an operator-enabled, runtime-specific model on
both embedded and forwarded runs. Requests can still use native judging and
post-judgment observations during qualification.
