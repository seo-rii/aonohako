# Communication V2 competitive matches

`communication-v2` is advertised by the explicitly enabled and isolated
runner shapes that support `communication-v1` when an immutable resident image
is configured. V1 keeps its shared participant program and
`manager-result-v1` result contract, including its default configuration.

Set `AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT` to `sha256:<digest>` of the
actual immutable deployed image, in the same revision update which pins that
image. Deployment must read back and verify both the image and environment
value. Mutable tags and the old build recipe fingerprint stamps cannot supply
this binding. With the new setting absent or invalid, V2 is not advertised.

The runner reads and hashes its actual executable (`/proc/self/exe` on Linux).
Its resident runtime fingerprint is the lowercase SHA-256 of the UTF-8 bytes
`aonohako-competitive-runtime-v1\0`, the configured image digest, `\0`, the
resident executable's lowercase SHA-256, and a final `\0`. `GET /capabilities`
returns `communication_v2.runtime_fingerprint`, `runner_sha256`, and
`image_digest`. The trusted caller freezes these values in the cohort snapshot.

V2 executes exactly two independently materialized native participant programs
and one native manager. Each participant has a separate workspace, UID,
read-only compiled artifacts, pipe pair, CPU allowance, and memory limit. The
manager retains its separate trusted identity. The runner admits the combined
memory and workspace budget before starting any submitted program.

## Request

Use `programs` for the three programs and this communication specification:

```json
{
  "version": 2,
  "participant_program_ids": ["seat0", "seat1"],
  "manager_program_id": "manager",
  "participant_count": 2,
  "result_protocol": "match-result-v1",
  "input_url": "https://official-data.example/test.in",
  "input_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "runtime_fingerprint": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "match_seed": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

The two seat IDs and manager ID must differ. The singular
`participant_program_id` is prohibited. Every program supplies `source_sha256`,
a lowercase SHA-256 provenance value for the trusted caller's compiled source
recipe. The runner echoes this value; it does not claim to have compiled or
verified those source bytes during execution.

Each `binaries` entry supplies `sha256` of its decoded bytes. V2 accepts inline
`data_b64` only and checks every artifact digest before materialization. It
rejects URL artifacts to prevent mutable downloads from changing a pinned
match. The request's source recipe, artifact names and digests must also be
pinned in the caller's immutable tournament snapshot.

`input_sha256` is required and binds the actual materialized official fixture.
The runner hashes inline or downloaded bytes before any submitted program is
started. A mismatch is an unscored `judge_error` with `error_code:
"input_integrity"` and `retriable:false`; fixing the immutable configuration
is required before that match can run. The digest is echoed in `match`.

`runtime_fingerprint` is required and must match the fingerprint calculated
by the resident runner. A mismatch or unavailable identity rejects the request
before launching any target, returning unscored `judge_error`,
`error_code:"runtime_fingerprint"`, and `retriable:false`. Match responses echo
the actual resident identity, including on a mismatch; the caller's claimed
fingerprint is not used as an attestation.

`match_seed` is optional and, when present, must be 64 lowercase hexadecimal
characters. It is sent only to the manager. The runner echoes the seed in the
match response. Tournament replay must use the same snapshot and seed.

## Trusted manager arguments and result

The native manager receives this exact `argv` layout:

| Index | Value |
| --- | --- |
| 0 | Manager executable |
| 1 | Private official input path |
| 2 | Private official answer path |
| 3 | Result pipe path |
| 4 | `2` |
| 5 | Seat 0 participant stdout pipe path |
| 6 | Seat 0 participant stdin pipe path |
| 7 | Seat 1 participant stdout pipe path |
| 8 | Seat 1 participant stdin pipe path |
| 9 | Match seed, or empty string |

Each participant receives only its executable path and seat number `0` or `1`.
Opponent artifacts, source provenance, input/answer paths, and the match seed
are never passed to it.

The manager writes exactly one JSON object to the result pipe and exits zero:

```json
{"version":1,"wins":[7,13],"games":20}
```

The two win counts must be nonnegative integers totaling exactly 20. Unknown
fields, trailing objects, another version, or another game count are rejected.
The manager adapter must preserve the official game rules and whole-test
forfeit semantics for illegal game actions.

## Response and failure attribution

The execution result contains `match` with `version:1`, `games:20`, ordered
participant identities and artifact digests, per-seat execution statistics,
the actual selected `entry_point` within each participant's artifact workspace,
`outcome`, `retriable`, and optional `wins`. No scalar submission score is
returned. The trusted tournament caller computes ratings and the cohort score.

- `completed`: a valid manager result; `wins` is the manager's result.
- `forfeit`: one participant independently failed before a final manager
  result; the opponent receives all 20 wins. The coordinator then stops the
  remaining processes. Their cancellation is never counted as a second fault.
- `judge_error`: manager, sandbox, admission, request, or protocol failure,
  or two independently observed participant failures. It is unscored,
  omits `wins`, and sets `retriable:true`. An immutable fixture digest mismatch
  or runtime identity mismatch instead supplies its error code and sets
  `retriable:false`.

A valid manager result completed before participant pipe shutdown takes
precedence over later teardown signals. A manager failure which happened
before a participant pipe failure cannot be converted into a player forfeit.
An independent nonzero manager exit or manager resource fault always remains
unscored, even when pipe EOF was observed first. Genuine contestant resource
faults and nonzero exits cannot be hidden by a manager result reported before
the contestant's supervisor finished its final accounting.
After a genuine player fault, the manager's coordinator-induced cancellation
does not invalidate the winner. That exemption requires an observed coordinator
cancellation timestamp preceding the manager's completion and no independent
manager resource fault or independently fired manager wall deadline. An empty
or truncated result may use that exemption only if parsing finished after the
coordinator cancellation. A completed invalid protocol remains unscored.
Transport shutdown can establish a second player fault only when observed
before coordinator cancellation. A participant's own wall deadline firing
before cancellation remains independent even if its supervisor finishes
later. Genuine final CPU, memory, nonzero exit, and target signal faults remain
independently attributed even when final accounting is reported afterward.

Completed and forfeited matches have execution status `Accepted` to identify
a successfully recorded match. That status is not a full-score submission
verdict. Callers must require the structured match result and perform the
original tournament aggregation.
