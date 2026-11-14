# CPython diagnostic work measurement

This change registers `cpython` as an explicit opt-in durante family. Set
`AONOHAKO_DURANTE_FAMILIES=cpython` and request `work_measurement: true`
for an ordinary single-program run. Registration does not enable measurement by
default or certify every runtime in the family. The consolidated integration includes this family selection, boundary tests and qualification requirements.

PYTHON3 is separate from PyPy and from Python-backed esoteric-language wrappers. The existing stdlib/installed-library policy, isolated startup flags, bytecode fallback and native extensions remain part of the measured process. Startup, runtime and extension work are included; this does not measure Python bytecodes alone.

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

`PYTHON3`.

Accepted unambiguous runtime forms: `python`.
Canonical source profiles take precedence over runtime aliases. Generic `binary`
remains rejected because it loses source-language provenance.

## Relationship to the earlier Python experiment

[PR #84](https://github.com/seo-rii/aonohako/pull/84) is a separate experiment
using CPython LLVM IR counters and PyPy JIT counters. Its partial interpreter counters are superseded by the common Durante integration; historical experimental evidence remains distinct. This family PR wraps the whole interpreter executable with the
process-tree binary counter, so executed interpreter, JIT-generated native code
and shared-library/native-extension code contribute to its diagnostic work.
Kernel work is outside this counter. The two approaches have different coverage,
cost and trust limits. Both retain diagnostic counters separate from production
milliseconds and TLE policy.

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

The frozen core `7039485` with `-vm_size 32M -vmheap_size 32M` completed two local runs of each listed fixture with original output/exit and complete process-tree accounting: `cpython`. Exact versions, snapshot hashes, counts, quality flags and retained prototype results are in [cpython-local-smoke.json](cpython-local-smoke.json). These checks do not qualify every source profile or the final deployed image.

The fixed compatibility sources, compile/run commands, stdin and expected output
are checked in under [`internal/durante/testdata/cpython`](../../internal/durante/testdata/cpython/manifest.json). They require the indicated local runtimes; missing runtimes
are unavailable coverage, not passing measurements.

These local checks used the external core collector after every process exited.
They do not exercise aonohako sandbox integration; image-specific end-to-end
validation remains a separate requirement.

## Core CI interpreter evidence

The [completed core CI run](https://github.com/seo-rii/durante/actions/runs/42613918219)
at `e457570` passed all 92 compact-VM checks without skips. Its CPython
`print(sum(range(100000)))` smoke printed `4999950000`, exited 0 and returned
valid, complete accounting for one process/image. The sanitized receipt is
[cpython-core-ci.json](cpython-core-ci.json).

The receipt did not record the interpreter version or resolved executable path.
This interpreter smoke had REP telemetry disabled, while aonohako enables it.
It validates a simple core invocation; the actual image-owned command, sandbox/API
integration and representative JIT workload coverage require separate checks. It does not
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
