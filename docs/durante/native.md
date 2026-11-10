# Other native binaries diagnostic work measurement

This change registers `native` as an explicit opt-in durante family. Set
`AONOHAKO_DURANTE_FAMILIES=native` and request `work_measurement: true`
for an ordinary single-program run. Registration does not enable measurement by
default or certify every runtime in the family. The common C/C++ integration PR
must be present first; this PR adds family selection, boundary tests and these
qualification requirements.

This family covers native artifacts outside C/C++, including Go, Rust, OCaml, Chapel, Pony, Mojo and Chicken Scheme. Native does not mean runtime-free: GC, preemption, threads and embedded libraries still affect execution. Canonical source profiles remain mandatory for generic binary artifacts; bare binary cannot establish the source family. Existing native wrapper arguments and environment settings are preserved.

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

`RUST`, `RUST2015`, `RUST2018`, `RUST2021`, `RUST2024`, `MOONBIT`, `CHAPEL`, `KOKA`, `PONY`, `ZEROLANG`, `GO`, `ZIG`, `KOTLIN`, `ASM`, `NASM`, `PASCAL`, `DELPHI`, `OBJECTPASCAL`, `NIM`, `ADA`, `ADA2012`, `ADA2022`, `COBOL`, `GNUCOBOL`, `CYTHON`, `DART`, `FORTRAN`, `FORTRAN95`, `FORTRAN2003`, `FORTRAN2008`, `FORTRAN2018`, `D`, `OBJC`, `OBJECTIVE_C`, `OBJCPP`, `OBJECTIVE_CPP`, `CRYSTAL`, `VALA`, `VLANG`, `ODIN`, `C3`, `HARE`, `CARBON`, `HASKELL`, `IDRIS2`, `SML`, `MLTON`, `SWIFT`, `MERCURY`, `GNU_PROLOG`, `INTERCAL`, `APECODE`, `OCAML`, `FREEBASIC`, `CLASSIC_BASIC`, `QBASIC`, `MOJO`, `CHICKEN_SCHEME`.

Accepted unambiguous runtime forms: `c3`, `chapel-binary`, `chicken-scheme`, `go-binary`, `mojo-binary`, `ocaml`, `pony-binary`.
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

## Local compatibility evidence

The frozen core `7039485` with `-vm_size 32M -vmheap_size 32M` completed two local runs of each listed fixture with original output/exit and complete process-tree accounting: `go`, `rust`. Exact versions, snapshot hashes, counts, quality flags and retained prototype results are in [native-local-smoke.json](native-local-smoke.json). These checks do not qualify every source profile or the final deployed image.
Go runs observed runtime signals and are labeled `signals_observed`; Rust runs were uninterrupted. A complete Go report must not be described as exact retired instructions.

The fixed compatibility sources, compile/run commands, stdin and expected output
are checked in under [`internal/durante/testdata/native`](../../internal/durante/testdata/native/manifest.json). They require the indicated local runtimes; missing runtimes
are unavailable coverage, not passing measurements.

These local checks used the external core collector after every process exited.
They do not exercise aonohako sandbox integration; image-specific end-to-end
validation remains a separate requirement.
