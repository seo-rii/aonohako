# Separate MOVS and STOS costs

The `weighted-rep-operations-nanoseconds-v2` diagnostic model assigns different
costs to MOVS copying and STOS filling. Both operations already have separate
byte counts in Durante reports. A combined REP byte coefficient cannot express
different costs for the two operations.

This change supplies a formula and its API validation. It does not supply
calibrated weights, qualify a runtime, or turn the result into an enforced time
limit. The existing `durante-reference fit` command still fits the two-weight
v1 model. Its earlier failed qualification is not evidence for v2 coefficients.

## Configuration

Set `method` explicitly to select v2, provide both positive operation weights,
and omit `rep_byte_ns_numerator` entirely. The shared instruction weight and
denominator must also be positive integers. This arithmetic-only example uses
the same identity fields as [the time policy](durante.md#explicit-reference-time-calculator):

```json
{
  "method": "weighted-rep-operations-nanoseconds-v2",
  "model_id": "separate-rep-arithmetic-example",
  "model_version": 2,
  "reference_id": "arithmetic-fixture-only",
  "runtime_id": "CPP20@<actual-overlay-image-digest>",
  "family": "cpp",
  "engine_profile": "dynamorio-11.3-durante-v5-vm32m",
  "dispatch_profile": "default",
  "instruction_ns_numerator": 2,
  "movs_byte_ns_numerator": 3,
  "stos_byte_ns_numerator": 5,
  "ns_denominator": 4
}
```

```
estimated_ns = ceil((instructions * instruction_ns_numerator
                   + movs_bytes * movs_byte_ns_numerator
                   + stos_bytes * stos_byte_ns_numerator) / ns_denominator)
```

The implementation sums exact integer products and rounds once. It never adds
the aggregate `rep_bytes` term on top of the two operation terms. For 1,234
instructions, 65 MOVS bytes and 66 STOS bytes, this example gives 749 ns.
The instruction count already includes the instrumented REP instruction entry.
Byte counters describe requested REP work; they do not assert actual memory
traffic or completed bytes after an interruption.

In `AONOHAKO_DURANTE_TIME_MODELS`, put `method` on the shared model and the three
weights plus denominator in its `coefficients` object. Runtime profile bindings
are unchanged. One v2 model can be referenced by several explicitly qualified
profiles, without duplicating its coefficients or implicitly enabling a language.

V2 requires an observed version-1 `rep_breakdown` whose byte and entry totals
match the aggregate counters. A legacy report without that field cannot be
converted, including when its combined REP count is zero. An explicitly observed
zero split is valid. Incomplete or interrupted accounting, unsupported REP,
profile mismatch and signed-nanosecond overflow still prevent conversion.

## Compatibility and qualification

An absent `method` continues to select `weighted-work-nanoseconds-v1`, with the
original positive `rep_byte_ns_numerator`. Explicit v1 is also accepted. Mixed
formula fields, unknown methods and explicit empty methods are rejected. The
parser rejects an irrelevant weight even when its JSON value is zero.

Time estimates carry the method and its selected weights. A remote control
plane recomputes the estimate with its own configured model; changing the
returned method, weights, or MOVS/STOS partition cannot silently change time.
Missing detail retains the raw diagnostic report with
`time_estimate_unavailable: observation_not_convertible`.

Use a new model version and reference record when changing formulas or weights.
Qualifying v2 requires a preregistered training set with enough independent
instruction, copying and filling work to identify all three weights, plus
untouched validation cases, repeated observations and per-runtime eligibility
checks. Neither the arithmetic examples nor a successful v1 fit fulfill those
requirements. SIMD, cache behavior, JIT compilation and garbage collection can
still affect the relationship between these counters and native CPU time.
