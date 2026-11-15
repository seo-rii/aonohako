# Effective runtime options for diagnostic time models

The schema 2 time-model registry additionally binds the effective runtime launch
and memory envelope. For example, default Node tuning with 256 MiB produces
`--max-old-space-size=153 --max-semi-space-size=4`; at 512 MiB it produces
`--max-old-space-size=307 --max-semi-space-size=8`. At 512 MiB, a selected 75%
old-space setting instead produces `--max-old-space-size=384`. Those executions
need different schema 2 bindings even within the same immutable image.
Python/PyPy `stdlib` and `installed` library modes also select different wrappers.

This is configuration matching for **diagnostic** conversion. It provides no new
coefficients, qualification evidence, runtime attestation, or credited verdict.
The shared model remains independent of its bindings: multiple languages and
multiple effective option sets may reference the same model/version when the
operator has evidence for each application.

## Supported launchers and compatibility

Schema 2 supports canonical source profiles whose execution launcher is
`binary`, `go-binary`, `mojo-binary`, `chicken-scheme`, `c3`, `pony-binary`,
`python`, `pypy`, or `javascript` (Node). Normal family admission, image, engine,
dispatch, report-quality and exact arithmetic checks still apply. The source
must resolve to a registered measurement family. This includes C/C++, Rust,
CPython/PyPy and Node profiles, subject to their existing family availability.
Other launchers, including JVM, Deno, Bun, Wasmtime and Ruby, cannot have schema 2
bindings. They are rejected explicitly; no default or legacy binding is tried.
Adding a launcher requires accounting for its complete runtime-option and
environment preparation, not merely adding its family to an allowlist.

Schema 1 registries and the original single C/C++ `TIME_POLICY` retain their
previous behavior. They are **unbound with respect to runtime options**. Their
estimates omit `runtime_options_id`, and existing `qualification_id` text does
not promote them to options-bound execution. Keep their settings fixed to their
evidence; migration to schema 2 is explicit. This change does not automatically
protect existing schema 1 configurations against option differences.

## Generate a descriptor

Run the reference CLI with the same operator environment as the intended runner:

```sh
durante-reference describe-runtime-options --request request.json
```

This loads the normal configuration, including its backend preconditions,
runtime tuning variables and named profiles. It does not execute a program,
materialize artifacts or upload a request, and it works before time models are
registered. The input is a `RunRequest` JSON file. The CLI applies the same HTTP policy
selection for runtime profiles and Python libraries, including problem mappings
and operator defaults, before describing the request. Use the intended
`problem_id` and any explicitly selected `runtime_profile` or
`python_library_mode`. Request-policy restrictions are the same as for execution.
For remote operation, both the frontend and this inspection must use the
intended downstream runner's tuning and named-profile settings.

For an ordinary raw-envelope preview, a minimal example is:

```json
{
  "lang": "JAVASCRIPT",
  "limits": {"time_ms": 100, "memory_mb": 64},
  "work_measurement_limits": {
    "cpu_time_ms": 3000,
    "wall_time_ms": 10000,
    "memory_mb": 512
  }
}
```

The preview needs neither binaries nor time-conversion flags. It describes the
512 MiB runtime, rather than the nominal 64 MiB limit. For interactive requests it
returns contestant and interactor descriptions, applying the existing interactor
limit/default rules. Communication descriptions preserve participant seats and
derive the manager's actual dedicated limits. These are the same role requests
used in admission and execution. Time/raw budget values retain their existing
validation when a request is actually submitted.

The output has `schema: 1`, `authority: "diagnostic_only"`, and a `runtime_options`
array. Each entry contains its role/program/optional participant index,
`source_language`, a full `runtime_options` object, and `runtime_options_id`.
There is no `passed` or qualification assertion.

## Descriptor and binding contract

`runtime_options` has exactly these fields:

| Field | Meaning |
| --- | --- |
| `version` | `1`, defining the placeholder and canonicalization contract below. |
| `argv` | Ordered launcher/flag/wrapper arguments from the real command builder. |
| `env` | Effective environment entries, unique and sorted by variable name. |
| `memory_mb` | Actual positive execution memory cap, after raw-envelope/role overrides. |

The program operand is `/__aonohako_program__`. Workspace paths in environment
values use `/__aonohako_workspace__`. These are descriptor tokens, not executable
filesystem locations. Target filenames, contents, entry selection, stdin and
communication input/answer/FD/seat/seed arguments are program inputs outside this
runtime-configuration identity. Runtime flags, Python wrapper text, fixed
trusted-sitecustomize path, environment values and memory cap are preserved.
The launcher token (`node`, `python3`, etc.) and its fixed `PATH` are interpreted
within the independently configured immutable image; the frontend does not
resolve a downstream executable using its own filesystem. The Durante wrapper
itself is covered separately by the existing engine/dispatch/installation
contract. Dispatch-specific `GLIBC_TUNABLES` overrides are included in the
descriptor using the same function as actual execution.

The sandbox passes environment entries directly to `execve`; descriptor generation
requires unique variable names and rejects duplicates instead of inferring
first- or last-wins behavior. Named profiles and default tuning that generate identical commands/environments have
identical descriptors. Irrelevant compile-only tuning does not create a new
runtime identity. Descriptor version 1 uses UTF-8 Go `encoding/json` serialization
of fields in the order `version`, `argv`, `env`, `memory_mb`, with no added
whitespace. Its ID is `runtime-options-v1:sha256:<lowercase hex SHA-256>`. Operators
should copy generated descriptors instead of constructing hashes manually.

Use `schema: 2` in `AONOHAKO_DURANTE_TIME_MODELS` and add the full generated
`runtime_options` object to **every** profile binding. The existing model entries
and coefficient formula are unchanged. The binding still requires canonical
source, family, immutable image, engine, dispatch, model/version and an operator
qualification reference. A descriptor can be reused across bindings when the
actual runtime options match, but never creates source or image approval itself.
The registry's existing 64 KiB encoded size limit still applies.

The unique selection key is source/image/engine/dispatch/options ID. Multiple
options may select the same shared model; two model bindings for the exact same
key are rejected as ambiguous. A schema 1 binding cannot contain runtime options;
a schema 2 binding cannot omit them. Missing, invalid or unmatched options reject
time conversion before local execution or the frontend's remote upload.

## Result and remote behavior

A successful schema 2 `time_estimate` additionally contains `runtime_options_id`.
This also applies to per-role `time_budget.estimate`. The runner uses the same
selected tuning snapshot in admission, execution and final conversion. For
ordinary conversion the raw resource envelope is applied before descriptor
selection; peer conversion uses each real role request. Signals, unsupported
REP observations, incomplete exits and other existing unavailable conditions
are unchanged and do not produce estimates.

The frontend independently recomputes descriptor selection and the exact
estimate using its configured downstream settings. A downstream estimate with a
missing or different options ID is rejected, including an older runner's
unbound result. A downstream configuration mismatch may therefore be detected
after that runner executed the request; no incorrect conversion is accepted.
The ID is not an attestation that the returned observation was produced with
those settings. Raw observations and diagnostic authority retain their existing
limitations. Both sides must explicitly migrate to schema 2; a legacy frontend
and legacy registry continue their legacy behavior.
