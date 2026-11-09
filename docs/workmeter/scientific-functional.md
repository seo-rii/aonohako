# Scientific and functional runtimes diagnostic work measurement

This change registers `scientific-functional` as an explicit opt-in workmeter family. Set
`AONOHAKO_WORKMETER_FAMILIES=scientific-functional` and request `work_measurement: true`
for an ordinary single-program run. Registration does not enable measurement by
default or certify every runtime in the family. The common C/C++ integration PR
must be present first; this PR adds family selection, boundary tests and these
qualification requirements.

These profiles span native interpreters, JITs and trusted wrapper entry points; family membership is only an explicit diagnostic selection policy. SQLite exec wrappers, DuckDB/GDL helpers and optional Octave plotting children require complete process-tree handling. Runtime-specific memory, thread and signal behavior remains to be qualified.

The default dispatch profile preserves runtime selection. The
`glibc-2.41-sse2-v1` profile remains restricted to the separately qualified C/C++
experiment. Existing sandbox capabilities are not broadened: support for summed
child accounting does not grant a submission permission to create children.
Incomplete trees, invalid segments and unsupported accounting conditions must
remain explicit measurement failures rather than plausible partial totals.

Measured work is diagnostic software instruction/REP demand, not CPU milliseconds.
All executed runtime and library code contributes; kernel work is outside that
counter. Engine/client memory is included in measured process RSS. Existing
CPU/wall/memory limits still describe the instrumented run and are not evidence
that uninstrumented verdicts are identical. This PR introduces no score-to-time
conversion or trusted enforcement claim.

## Source profiles

`ALGOL68`, `RACKET`, `SCHEME`, `CHEZ_SCHEME`, `GUILE`, `GDL`, `OCTAVE`, `SMLNJ`, `HAXE`, `LISP`, `PICOLISP`, `SQLITE`, `JULIA`, `RAKU`, `PROLOG`, `FACTOR`, `R`, `SMALLTALK`, `GST`, `DUCKDB`, `BQN`, `J`, `UIUA`, `JANET`, `FORTH`, `GFORTH`.

Accepted unambiguous runtime forms: `algol68`, `bqn`, `chez-scheme`, `duckdb`, `factor`, `forth`, `gdl`, `guile`, `haxe`, `j`, `janet`, `julia`, `lisp`, `octave`, `picolisp`, `prolog`, `r`, `racket`, `raku`, `scheme`, `smalltalk`, `smlnj`, `sqlite`, `uiua`.
Canonical source profiles take precedence over runtime aliases. Generic `binary`
remains rejected because it loses source-language provenance.

## Qualification

Selection/configuration tests check representative profiles, other-family
isolation, disabled configuration and rejection of the C/C++-specific dispatch
profile. These are integration-policy tests, not runtime qualification.

Before enabling a runtime operationally, record a complete metered execution of
its actual image-owned command with unchanged output and exit behavior; include
threads/signals, startup and any permitted fork/exec path. Preserve every process
segment and verify that no child work is omitted or double-counted. Repeated
workload measurements, cost, invalid-result rate and limit-boundary behavior must
be reported separately. Representative smoke evidence will be recorded alongside
the PR validation; untested profiles remain unqualified.
