# Entry block judging

The `ENTRY` compiler validates an immutable Entry 4.0.23 export without running
its blocks. The `entry` executor runs the bounded `entry-v1` interpreter inside
the ordinary sandbox. The production `type-b` image and `ci-entry` image install
Node and the image-owned runner. `/capabilities` advertises `entry-v1` only when
the embedded helper, Node, and runner assets are available.

## Submission and execution

Compile exactly one data source, `Main.entry.json`, with `lang: "ENTRY"` and
`version: "entry-v1"`. Its JSON envelope is:

```json
{
  "format": "entry-submission",
  "version": 1,
  "engineVersion": "4.0.23",
  "graderVersion": "entry-v1",
  "mode": "stdio",
  "allowedBlocks": ["when_run_button_click", "dialog", "text"],
  "project": { "scenes": [], "objects": [], "variables": [], "messages": [] }
}
```

Replace `project` with a valid Entry export containing exactly one scene and
1–100 sprite objects. The empty project above only illustrates the envelope.
`allowedBlocks` optionally restricts the supported palette; it must be nonempty
and contain only entries from [entry-supported-blocks.json](../scripts/entry-supported-blocks.json).
The application control plane owns this envelope and its mode/palette. It must
validate the learner's raw export and retain the exact submitted envelope for
rejudging. Eden also fixes the author-defined initial state of starter objects,
variables and lists before admission; the runtime never trusts a client-provided
final state.

Execute the returned JSON artifact as data with `lang: "entry"`,
`entry_point: "Main.entry.json"`, and `entry_mode: "stdio"` or `"state"` matching
the envelope. JavaScript submissions, extra artifacts, executable modes, custom
judges, interactors, communication, pipelines, file/sidecar outputs, network
access and ignored time limits are rejected for Entry execution. All program
blocks are interpreted as data; no submitted source is evaluated as JavaScript.

## Standard I/O mode

`stdin` or `stdin_url` supplies UTF-8 input. Each `ask_and_wait` consumes one
line; `get_canvas_input_value` returns that answer, or an empty string at EOF.
`dialog` emits one line, using Entry's Boolean and three-decimal dialog formatting
(an empty dialog is four spaces). `expected_stdout` or `expected_stdout_url`
is compared by the normal stdout comparator. Scripts run to completion with a
fixed random seed of 1. Exceeding instruction or logical tick limits produces
TLE; invalid operations produce RE.

## Stage-state mode

The input is a private scenario, independent of the submitted program:

```json
{
  "version": 1,
  "seed": 1,
  "ticks": 60,
  "input": "hello\n",
  "events": [
    { "tick": 0, "type": "keyDown", "key": 39 },
    { "tick": 5, "type": "keyUp", "key": 39 },
    { "tick": 10, "type": "click", "objectId": "sprite-id" }
  ],
  "initial": {
    "objects": { "sprite-id": { "x": 0, "y": 0 } },
    "variables": { "score-id": 0 },
    "lists": { "path-id": [] }
  }
}
```

`version`, uint32 `seed`, `ticks` (1–36,000), and `events` are required. Optional
`input` supplies answers. `initial` overrides existing object/variable/list IDs
using trusted testcase values. Events must be sorted by tick, lie in
`0..ticks-1`, and number at most 1,000. Key codes range from 0 to 255. No event or
initial value is read from the browser's final state.

The interpreter advances at 60 virtual ticks per second. It starts green-flag
scripts at tick 0, dispatches scenario events in order, starts message receivers
on the following tick, and advances ready scripts in object/script order. Loop
iterations yield a tick; waits use virtual time. Holding a key updates sensors,
and a new key-down transition starts its event scripts. At the requested final
tick, active scripts stop and their resulting state is compared. This fixed
scheduler is the `entry-v1` contract; browser wall-clock speed does not affect it.

Expected output is a strict partial state predicate:

```json
{
  "version": 1,
  "tolerance": 0.000001,
  "objects": { "sprite-id": { "x": 100, "y": 0, "visible": true } },
  "variables": { "score-id": 10 },
  "lists": { "path-id": ["01", 2, 3] }
}
```

Object fields are `x`, `y`, `direction`, `rotation`, `visible`, `size` (percent),
and `say`. At least one property, variable, or list must be checked. Lists match
fully and in order, including an explicitly empty list. Numeric absolute
tolerance defaults to `1e-6` and may be set to 0–1. Strings and Booleans match
exactly, so `"01"` and `1` differ. Unknown keys, duplicate/unsafe keys, nulls,
empty predicates and nonfinite/oversized values are rejected as configuration
errors. Missing properties or unequal values yield WA.

The expected predicate is parsed and compared in the Go host; it never enters
the Node process. State executions suppress stdout/stderr response bodies and
SSE log hooks, including wrong answers and runtime errors. The control plane
must likewise omit private testcase samples and redact transport diagnostics.

## Supported features and limits

The 63-block palette covers arithmetic, comparisons, control flow, strings,
variables/lists, sprite position/direction/rotation/visibility/size, questions,
dialogs, and green-flag/key/click/message events. Hardware, cloud variables,
extensions, custom functions, clones, multiple scenes, text objects, media
execution, collision detection and drawing are outside this version and are
rejected when used as blocks. Inline assets may remain in an export; the judge
does not load them or fetch asset URLs.

Limits include a 512 KiB project, a 600 KiB envelope, 128 KiB testcase input,
64 JSON nesting levels, 30,000 structure nodes, 10,000 blocks, 1,000 variables
and lists, 10,000 total list items, 256 concurrent scripts, 1,000,000 charged
instructions, 36,000 ticks, 65,536-character strings and 1 MiB output. The
sandbox's time/memory/output limits apply in addition. Scripts, IDs, references
and unsupported blocks are validated before execution. Numeric values are
finite and bounded to an absolute value of `1e15`.

## Verification

```sh
node --test scripts/entry-runtime.test.mjs
go test ./internal/entrypolicy ./internal/compile ./internal/execute ./internal/runvalidation ./internal/runtimepacks ./cmd/selftest
```

The Node tests include actual pinned Entry browser exports for movement,
arithmetic, strings, Boolean conversions, list values and rotation, as well as
scenario determinism, input/output, validation, loop limits and CLI behavior.
Go tests cover compiler/artifact routing, isolated requests, host state
predicates and resource verdicts. The runtime-image compile-execute selftest
exercises A+B in standard I/O mode and AC/WA with suppressed output in state
mode through the HTTP API. Privileged sandbox tests may skip in ordinary local
workspaces; image CI performs the installed runtime selftests.
