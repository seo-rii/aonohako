# Pipeline V1

Pipeline V1 makes one `/execute` request the owner of a testcase's complete,
atomic execution. It models immutable judge resources, private artifacts,
batch or interactive steps, and an explicit final judge. It does not infer a
handoff from combinations such as `interactor` plus legacy `steps`.

`GET /capabilities` advertises the supported version, deployment step limit,
executors, artifact sources and their byte limits, and final judges. A client
must not silently fall back when `pipeline-v1` is unavailable. The current
remote control-plane architecture has no authenticated downstream capability
negotiation, so remote transports neither advertise nor accept Pipeline V1;
embedded helper runners fail closed until such negotiation exists.

## Request contract

The initial deployment supports at most two steps, while the schema retains a
step list so that this policy can be raised without replacing the contract.

```json
{
  "problem_id": "example",
  "pipeline": {
    "version": 1,
    "resources": {
      "testcase": { "data_url": "https://judge.invalid/input" },
      "answer": { "data_url": "https://judge.invalid/answer" }
    },
    "programs": [
      { "id": "participant", "lang": "binary", "binaries": [] },
      { "id": "interactor", "lang": "binary", "binaries": [] }
    ],
    "steps": [
      {
        "id": "phase1",
        "executor": {
          "kind": "interactive",
          "participant_program_id": "participant",
          "interactor_program_id": "interactor",
          "interactor_limits": { "time_ms": 1000, "memory_mb": 256 },
          "interactor_answer": { "type": "resource", "id": "answer" }
        },
        "stdin": [{ "type": "resource", "id": "testcase" }],
        "outputs": [{
          "id": "phase2-input",
          "source": { "kind": "interactor_output" },
          "max_bytes": 67108864
        }],
        "limits": { "time_ms": 1000, "memory_mb": 1024 }
      },
      {
        "id": "phase2",
        "executor": { "kind": "batch", "program_id": "participant" },
        "stdin": [{ "type": "artifact", "id": "phase2-input" }],
        "limits": { "time_ms": 1000, "memory_mb": 1024 }
      }
    ],
    "final_judge": {
      "kind": "spj",
      "input": { "type": "resource", "id": "testcase" },
      "expected": { "type": "resource", "id": "answer" },
      "actual": { "type": "step_stdout", "step_id": "phase2" },
      "spj": { "lang": "binary", "binary": {} }
    }
  }
}
```

Resources accept inline `data_b64` or `data_url` and reject combining them;
an omitted inline value represents an empty resource. URL-backed resources are
fetched once before execution and then reused, including by the final judge.
Artifact references may only point to an earlier step. Artifact sources
are `participant_stdout`, `participant_file` with a validated relative path,
and the runner-owned standard `interactor_output`; arbitrary interactor paths
are never accepted. `participant_stdout` and `interactor_output` allow at most
64 MiB per artifact; `participant_file` matches the captured-file hard limit of
8 MiB. Interactive stdout truncation is an artifact failure, never a prefix
handoff.

An interactive executor may explicitly bind a resource as
`interactor_answer`; the runner materializes it as the trusted interactor's
third file argument. Omitting the reference preserves the legacy empty answer
file behavior.

The final judge independently names its `input`, `actual`, and `expected`
values. In particular, the input to the final SPJ is not derived from the last
step's stdin. Pipeline V1 currently rejects top-level `sidecar_outputs`, SPJ
sidecars, and `ignore_tle` because their cross-step result semantics are not
yet defined.

Before execution, the wire request is compiled into one internal
`executionPlan`: resource bytes are resolved, program references are bound,
and final-judge inputs are fixed before the first process starts. Step
executors consume that canonical plan rather than reinterpreting wire ids while
the pipeline is running.

## Isolation and lifetime invariants

- Every participant step receives a fresh process and workspace. Program
  binaries may be reused, but workspace state is not.
- Only declared artifacts cross step boundaries. Artifacts stay in a private,
runner-owned store, have a required bounded size, and are removed with the
pipeline. Declared artifact limits and stored artifact bytes also share a
request-wide 128 MiB ceiling.
- `interactor_output` captures only the standard output file created for the
  trusted interactor invocation. Non-regular files and links are rejected.
- Resources are immutable to participant processes. The final SPJ receives a
  separate clean workspace and does not share participant or interactor state.
- A failed step or artifact capture stops all later steps. Request cancellation
  terminates the active contestant and interactor through their shared context.
- Artifact contents and interactive transcripts are excluded from API and SSE
  stdout, stderr, failure reasons, and live hooks. Public failures use generic
  diagnostics while step status, verdict source, limits, truncation flags, and
  bounded metadata such as `handoff_bytes` remain available. Remote execution
  boundaries apply the same redaction defensively.

The existing network policy and request-wide binary/resource budgets apply to
all pipeline programs and the final SPJ. Interactive admission should use the
concurrent participant-plus-interactor peak; sequential batch and final-judge
peaks are compared rather than summed.
