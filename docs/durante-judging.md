# Work-based judging (virtual milliseconds)

Status: **operator-gated, opt-in per request.** Default images and requests do
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
enabled only for families and runtimes the operator has qualified, and only when
a request asks for it. It does not touch ordinary requests.

## When it applies

A request opts in with `work_measurement_judge: true`, which requires the same
fields as the reference-time path:

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
- Judge mode reuses the raw resource envelope. Only an ordinary single-program
  request is accepted; SPJ, interactive, communication, pipeline, entry and
  multi-step requests are rejected, as the envelope already requires.
- `work_measurement_judge` requires `work_measurement_time: true` and
  `work_measurement_limits`. Without a time model there is no millisecond value
  to compare, so the request is rejected before execution.
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

(or the v2 MOVS/STOS form). The comparison is exact in nanoseconds:

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

## Verdict rules

The verdict is computed only after the diagnostic execution state and the time
estimate are final. Original output comparison is unchanged.

| Envelope state | Output | Time estimate | Verdict |
|---|---|---|---|
| `complete` | matches | available, `estimated_ns ≤ limit` | **Accepted** |
| `complete` | matches | available, `estimated_ns > limit` | **Time Limit Exceeded** |
| `complete` | mismatch | (not evaluated) | **Wrong Answer** |
| `complete` | matches | unavailable | **Runtime Error** |
| `resource_exhausted` | — | — | **Time Limit Exceeded** |
| `incomplete` / `failed` / `cancelled` | — | — | **Runtime Error** |

- **Wrong Answer** uses the existing output oracle (stdout or the single file
  output), before any time comparison. A nonzero exit on an output match is a
  runtime error at the ordinary layer and produces `Runtime Error` here too.
- **Time estimate unavailable → Runtime Error.** A complete, output-matching run
  whose counts cannot be converted — signals observed, partial REP coverage,
  numeric overflow, or an incompatible report — is reported as `Runtime Error`
  with the eligibility reason, not silently accepted and not called TLE. This is
  the deliberate choice for this rollout: an unjudgeable observation is a system
  condition to retry or investigate, never a free `Accepted`. Signal-driven
  runtimes such as Go are therefore unjudgeable today; enable judge mode only for
  families whose counts convert (C/C++ first).
- **`resource_exhausted` → Time Limit Exceeded.** The raw envelope is a safety
  net set well above the nominal limit's expected work. Hitting it means the
  submission ran far past any plausible accepted solution even with generous
  actual limits, so TLE is correct. `termination_source` is preserved. Memory is
  **not** judged in judge mode — instrumented RSS includes the engine and code
  cache — so the operator sets `work_measurement_limits.memory_mb` generously; a
  raw memory stop also lands here as TLE rather than a credited MLE. Problems
  that need a real memory verdict must not use judge mode yet.
- **`incomplete` / `failed` / `cancelled` → Runtime Error.** Launch failure,
  client/collection failure, a forced stop, or cancellation cannot yield a
  trusted count, so the verdict is a (retryable) `Runtime Error`, never AC/TLE.

The response carries `work_measurement_judgment` with the chosen verdict, the
reason, the virtual-millisecond estimate, the integrity decision, and the raw
diagnostic execution block. Unlike the pure diagnostic envelope, judge mode sets
`status` to the real verdict (`Accepted`, `Wrong Answer`, `Time Limit Exceeded`
or `Runtime Error`) and may set `score`.

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
   Fix per-CPU runtime options (hash seed, GC/JIT thread counts) as well.
3. Run judge mode in shadow: enable it on a mirror of real submissions, compare
   its verdict with the existing CPU-time verdict, and measure the
   disagreement rate before it decides anything.
4. Enable it per problem set, starting with C/C++, `integrity_mode: off` for
   cooperative settings and `plausibility` where submissions may be adversarial.
5. Keep raw CPU/wall/memory limits active as independent safety nets; a work
   verdict never removes them.

Memory judging, online (mid-run) work budgets, interactive/communication judge
modes, and runtimes whose counts do not convert (signal-driven, e.g. Go) are out
of scope for this path and remain documented limitations.
