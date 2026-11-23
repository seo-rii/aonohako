# Instrumentation memory and native replay

Instrumented RSS includes the target, loader, language runtime, DynamoRIO and
Durante. A percentage or constant subtraction has not been qualified. A native
replay observes a **different execution's memory**; matching its output with the
instrumented output does not turn that observation into same-execution memory.
This document and its fixture make that boundary reproducible. They introduce no
credited-memory calculation, no replay in the API, and no changed MLE policy.

## Completed measurements

The earlier 2026-11-09 Cloud Run diagnostic used a small native broker that execed
before forking the measured target, avoiding its Python controller's inherited
resident image. Selected median final target RSS values were:

| Trusted C fixture | Native MiB | Instrumented MiB |
|---|---:|---:|
| Empty | 1.410 | 6.066 |
| Touch 16 MiB, immediately free | 17.152 | 21.680 |
| Touch 128 MiB, immediately free | 129.152 | 133.688 |

The percentage overhead differs substantially. A similar absolute overhead in
these three probes also does not establish a universal constant: translated code
caches, engine/client allocations, stacks and loaded libraries depend on the
workload. Subtracting two independently observed peaks does not recover the
maximum difference between simultaneous measurements.

That cohort found the touched region in all 96 final native wait peaks. Sampling
missed the touched size in 15/48 immediate-free runs using statm and 4/48 using
smaps. A live 32 MiB RSS threshold allowed a 48.285 MiB observed peak before
termination. Thus a successful eventual MLE kill is not proof of an instantaneous
kernel RSS cap. These were diagnostic launch/measurement results, not proof that
production Go-helper final wait accounting has the same clean boundary.

A new **local Linux**, non-Cloud-Run diagnostic on 2026-11-10 ran six bounded C
fixtures three times each with native / Durante / native observations: 18
triplets, 54 executions. All exited normally, all work records were complete,
and each triplet's stdout/stderr matched. Linux was 6.1.0-54-cloud-amd64, GCC
12.2.0, with the pinned DynamoRIO 11.3.0-1 SDK and current Durante client.

| Trusted C fixture | Native median peak KiB, 6 runs | Metered median peak KiB, 3 runs |
|---|---:|---:|
| Empty | 1,490 | 5,888 |
| Reserve 256 MiB, untouched | 1,464 | 6,100 |
| Touch 16 MiB, immediately free | 17,414 | 21,720 |
| Touch 64 MiB, immediately free | 66,684 | 70,896 |
| Touch 64 MiB, hold 20 ms | 66,682 | 70,876 |
| Touch 128 MiB, immediately free | 132,284 | 136,428 |

The scope is final target-process RSS from the small broker's `wait4`, including
loader/runtime/instrumentation. It is neither requested allocation size nor a
simultaneous process-tree peak. Sampled statm, smaps and VmHWM were retained as
separate observations. Per-run diagnostic guards were 5 CPU seconds, 8 wall
seconds and 384 MiB sampled RSS; they were infrastructure safeguards, not student
time limits. No target/engine high-water subtraction was applied.

The raw local archive is `workmeter-memory-policy-20261110/result.json`, containing
all observations and binary/tool SHA-256 identities. The local replay validator
passed 10 tests covering failure, output/identity mismatch, missing final peaks,
unqualified children and memory disagreement. These local diagnostic artifacts
are not installed in the service and do not establish production memory authority.

## Matching output does not prove equivalent memory behavior

The trusted fixture
[`memory_observer.c`](../internal/execute/testdata/durante/memory_observer.c)
reads its own mapped-library list, then touches 16 MiB natively or 64 MiB when it
observes DynamoRIO. Both paths print the same answer and exit normally. It is a
measurement-policy counterexample, not an implementation of a judge workload or
a recommended submission technique.

The local diagnostic observed these three runs of the same executable/input:

| Execution | Final target peak RSS KiB |
|---|---:|
| Native before | 17,576 |
| Instrumented | 71,020 |
| Native after | 17,480 |

All stdout/stderr bytes matched. Both native observations were stable. Those
facts still cannot justify assigning 17,576 KiB to the instrumented execution:
the actual touched allocation differed. More generally, runtime state, randomness
and scheduling can change execution paths without changing accepted output.

To reproduce the qualitative counterexample on Linux x86-64 with a C compiler,
GNU time (`/usr/bin/time`) and coreutils timeout, set `DYNAMORIO_HOME`
to the already-installed pinned SDK and `DURANTE_CLIENT` to its built Durante
client, then run from the repository root:

```sh
set -eu
probe_dir=$(mktemp -d)
cc -std=c11 -O2 -Wall -Wextra -Werror \
  internal/execute/testdata/durante/memory_observer.c \
  -o "$probe_dir/memory-observer"
mkdir "$probe_dir/records"
timeout 15s /usr/bin/time -f '%M' -o "$probe_dir/native-before.rss" \
  "$probe_dir/memory-observer" >"$probe_dir/native-before.out" 2>"$probe_dir/native-before.err"
timeout 15s /usr/bin/time -f '%M' -o "$probe_dir/instrumented.rss" \
  "$DYNAMORIO_HOME/bin64/drrun" -quiet -follow_children \
  -vm_size 32M -vmheap_size 32M \
  -c "$DURANTE_CLIENT" -outdir "$probe_dir/records" -rep_bytes -- \
  "$probe_dir/memory-observer" >"$probe_dir/instrumented.out" 2>"$probe_dir/instrumented.err"
timeout 15s /usr/bin/time -f '%M' -o "$probe_dir/native-after.rss" \
  "$probe_dir/memory-observer" >"$probe_dir/native-after.out" 2>"$probe_dir/native-after.err"
cmp "$probe_dir/native-before.out" "$probe_dir/instrumented.out"
cmp "$probe_dir/native-before.out" "$probe_dir/native-after.out"
cmp "$probe_dir/native-before.err" "$probe_dir/instrumented.err"
cmp "$probe_dir/native-before.err" "$probe_dir/native-after.err"
cat "$probe_dir/native-before.rss" "$probe_dir/instrumented.rss" "$probe_dir/native-after.rss"
```

Require zero exits for all three executions before comparing. This short recipe
uses GNU time's process maximum RSS and reproduces the different allocation
paths; it does not reproduce the complete earlier broker/sampling experiment or
promise identical absolute KiB values. The fixture and runner must be readable
and executable by the test user. The private temporary directory contains only
trusted test artifacts and may be removed after inspection.

## What DynamoRIO exposes

The inspected pinned **DynamoRIO-Linux-11.3.0-1** headers are
`include/dr_os_utils.h`, `include/dr_tools.h` and `include/dr_defines.h`:

- `dr_memory_is_dr_internal()` and `dr_memory_is_in_client()` classify addresses.
  `dr_query_memory()` and `dr_query_memory_ex()` describe mappings/protections;
  these are not target-only resident-memory peak counters.
- `dr_get_stats()` exposes engine statistics, including heap, stack and code-cache
  block peaks. Its API is not thread-safe and returns false when statistics are
  disabled. Separate category block peaks cannot be subtracted from RSS as if
  they were simultaneously resident bytes.

Primary references: [memory APIs](https://dynamorio.org/dr__os__utils_8h.html),
[statistics API](https://dynamorio.org/dr__tools_8h.html),
[statistics fields](https://dynamorio.org/struct__dr__stats__t.html).
The pinned local headers define this build; current online documentation may
describe a newer version.

A future single-execution diagnostic could join address classification with
simultaneous per-mapping RSS observations. It must validate mapping races, mixed
ranges, coverage and transient peaks. Counting only malloc/new would miss stack,
mmap, runtime/loader pages and allocator-retained memory. No such accounting is
claimed by this change.

## Conditions for any future native-memory replay

Keep an explicitly named, versioned replay observation separate from raw metered
RSS. Bind it to binary/input/runtime/environment/workspace identities. Require
normal, complete termination and validate outputs, while acknowledging that
output equality is insufficient evidence of matching allocations. Setup failure,
missing measurement, raw-limit termination, incomplete reaping or unqualified
processes must leave the value absent/invalid, never zero and never an AC upgrade.
Native CPU/wall guards must remain independent infrastructure bounds rather than
silently reintroducing native CPU time as the work-based student TLE criterion.

A two-execution policy may be a deliberate future contract: one execution is
authoritative for native memory and another for work. That requires explicit
validation and semantics. It must not be described as measuring both properties
of the same execution.

Parent/child instruction counts are additive; RSS peaks are not. A target can
create and reap a child before a broker observes it. Summing individual historical
peaks invents simultaneity and can double-count shared pages. Taking only the
largest child misses concurrent usage. Linux explicitly documents
[`RUSAGE_CHILDREN.ru_maxrss`](https://man7.org/linux/man-pages/man2/getrusage.2.html)
as the largest child's value, not a process-tree peak. These fixtures are trusted
single-process programs and do not qualify general child-process memory reporting.

A properly delegated per-run cgroup can record hierarchical memory usage, but
its charged-memory definition is distinct from process RSS and still includes
instrumentation. The kernel documents its scope in
[`memory.peak`](https://docs.kernel.org/admin-guide/cgroup-v2.html).
This experiment does not establish per-run cgroup delegation in Cloud Run.
