# JVM diagnostic work measurement

This change registers `jvm` as an explicit opt-in durante family. Set
`AONOHAKO_DURANTE_FAMILIES=jvm` and request `work_measurement: true`
for an ordinary single-program run. Registration does not enable measurement by
default or certify every runtime in the family. The common C/C++ integration PR
must be present first; this PR adds family selection, boundary tests and these
qualification requirements.

Java versions, Kotlin/JVM, Scala, Groovy and Clojure share the JVM family. JIT compilation, GC, helper threads and runtime signal handling need representative tests. Kotlin native belongs to the native-binary family, not this family. Dedicated Java 25 executable selection and each profile's heap/stack options remain unchanged.

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

`JAVA`, `JAVA8`, `JAVA11`, `JAVA15`, `JAVA17`, `JAVA21`, `JAVA25`, `GROOVY`, `CLOJURE`, `SCALA`, `KOTLIN_JVM`, `KOTLIN_JVM8`, `KOTLIN_JVM11`, `KOTLIN_JVM17`, `KOTLIN_JVM21`, `KOTLIN_JAVA`, `KOTLIN_JAVA8`, `KOTLIN_JAVA11`, `KOTLIN_JAVA17`, `KOTLIN_JAVA21`.

Accepted unambiguous runtime forms: `clojure`, `groovy`, `java`, `java25`, `kotlin-jvm`, `scala`.
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

The frozen core `7039485` with `-vm_size 32M -vmheap_size 32M` still failed the OpenJDK 17.0.21 compatibility fixture before application output. The two earlier prototype runs also failed: JVM startup reported SIGSEGV in `StubCodeMark::StubCodeMark`, then aborted. The collector correctly returned invalid/incomplete. This family remains a draft integration candidate with a known runtime incompatibility; a selector test is not evidence of JVM support. Engine-only DynamoRIO succeeds with both the minimal fixture options and the existing aonohako option shape. Adding the counter crashes even with REP telemetry disabled. Rebuilding the previous single-process counter reproduces the same failure, so this is an inherited instrumentation incompatibility rather than proof of a new process-tree regression. No runtime flags were changed operationally. The final-core confirmation and retained prototype results are recorded in [jvm-local-smoke.json](jvm-local-smoke.json).

The fixed compatibility sources, compile/run commands, stdin and expected output
are checked in under [`internal/durante/testdata/jvm`](../../internal/durante/testdata/jvm/manifest.json). They require the indicated local runtimes; missing runtimes
are unavailable coverage, not passing measurements.

These local checks used the external core collector after every process exited.
They do not exercise aonohako sandbox integration; image-specific end-to-end
validation remains a separate requirement.
