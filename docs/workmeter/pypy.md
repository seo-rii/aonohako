# PyPy diagnostic work measurement

This change registers `pypy` as an explicit opt-in workmeter family. Set
`AONOHAKO_WORKMETER_FAMILIES=pypy` and request `work_measurement: true`
for an ordinary single-program run. Registration does not enable measurement by
default or certify every runtime in the family. The common C/C++ integration PR
must be present first; this PR adds family selection, boundary tests and these
qualification requirements.

PYPY3 is separate from CPython. JIT-generated native code, interpreter startup and garbage collection are part of the measured process. Reproducible source and input do not promise identical JIT/GC work. Runtime signals and executable-memory behavior require direct qualification.

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

`PYPY3`.

Accepted unambiguous runtime forms: `pypy`.
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

The fixed compatibility sources, compile/run commands, stdin and expected output
are checked in under [`internal/workmeter/testdata/pypy`](../../internal/workmeter/testdata/pypy/manifest.json). They require the indicated local runtimes; missing runtimes
are unavailable coverage, not passing measurements.

## Core CI interpreter evidence

The [completed core CI run](https://github.com/seo-rii/workmeter/actions/runs/42613918219)
at `e457570` passed all 92 compact-VM checks without skips. Its PyPy
`print(sum(range(100000)))` smoke printed `4999950000`, exited 0 and returned
valid, complete accounting for one process/image. The sanitized receipt is
[pypy-core-ci.json](pypy-core-ci.json).

The receipt did not record the interpreter version or resolved executable path.
This interpreter smoke had REP telemetry disabled, while aonohako enables it.
It validates a simple core invocation; the actual image-owned command, sandbox/API
integration and PyPy JIT-loop coverage require separate checks. It does not
resolve the child-process compatibility gap below or establish score stability.

## Child-process compatibility gap

A separate core diagnostic using CPython `subprocess.run(["/bin/true"])` with
its default `close_fds` behavior timed out at 30 seconds. The cause remains
unresolved; the arithmetic fixture above does not qualify this child path.
This was not a PyPy reproduction, so PyPy child compatibility remains untested
rather than known equivalent. Keep Python child-process measurement unqualified
until the exact permitted image-owned fork/exec/spawn paths preserve output,
exit behavior and complete accounting. The existing sandbox process permissions
remain unchanged.
