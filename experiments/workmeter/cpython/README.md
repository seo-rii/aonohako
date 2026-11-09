# Experimental CPython work meter

Build a matched pair of CPython 3.13.5 interpreters: `plain` links the reporting
library without instrumentation, and `meter` instruments the core and built
standard-library modules using LLVM 19. Both use the same sources and compiler
options. This image is a diagnostic tool, not a production execution image.

```sh
docker build -t cpython-workmeter experiments/workmeter/cpython
```

The default runtime image is pinned by digest; `--build-arg BASE=...` can select
another compatible Debian trixie image. Python's source archive is checked
against its SHA-256. Build provenance records the compiler, LLVM version, source
files, configure logs, actual make arguments, smoke checks and metadata hashes.
The image does not build bit-for-bit reproducibly across changed apt packages;
use the resulting image digest and provenance to identify an experiment.

Executables are installed under
`/opt/interpreter-meter/cpython/{plain,meter}/bin/python3.13`. Set:

| Variable | Meaning |
| --- | --- |
| `WM_REPORT_PATH` | New absolute JSON report path, written exclusively with mode 0600 at normal process exit |
| `WM_EXPECT_METER` | `1` requires instrumented modules; use `0` for the plain control |
| `WM_RUN_ID` | Optional experiment identifier retained in the report |
| `HM_SEED` | `random` or a decimal uint32; unset retains the upstream `Py_BytesMain` path |

Seed selection occurs before initialization through `PyConfig`, so `-I -S`
continues to ignore `PYTHONHASHSEED` and other Python environment configuration.
Use nonzero seeds for a randomized-hash implementation with repeatable state.
Seed 0 disables hash randomization. Two distinct seed numbers are not guaranteed
to give distinct hash state; the smoke suite checks the selected 12 profiles.

The score is the sum of each optimized LLVM basic block's visit count times its
static IR instruction count. PHI, debug and lifetime intrinsics are excluded;
calls and memory intrinsics each count as one instruction. This is neither
retired machine instructions nor CPU milliseconds. The `llvm-ir-v1` model
version defines this counting rule; the executable and metadata manifest digest
identify the concrete compiled model. A scalar score is independently
recomputable from the sparse per-module vector and metadata weights. Distinct
module arrays with the same metadata identifier must both be included.

`valid=true` means only that the counter-integrity checks passed. External shared
libraries, assembly, kernel work and variable-sized memory intrinsic costs are
not fully measured. Counters and report state reside in the target's address
space and are not a trusted enforcement boundary. Do not use the score to grant
AC, undo a timeout, or replace production CPU limits.

The diagnostic runtime rejects `pthread_create` with `ENOTSUP` before another
thread can race its non-atomic counters. A libc `fork` attempt invalidates the
parent report, and the child does not write the inherited report path. Other
process-creation paths are not comprehensively detected. `_exit`, signals and
forced termination can leave no report; missing or invalid reports are never
successful zero-cost measurements. Process descendants must be handled by the
outer experiment runner.

Counter flags are 1 (counter overflow), 2 (invalid registration), 4 (duplicate
array registration), 8 (unsupported threads), 16 (aggregate overflow), 32
(missing instrumented modules), 64 (unsupported fork), and 128 (setup failure).
Every report explicitly declares `authority=diagnostic_only` and
`validity_scope=counter_integrity_only`.

Small local tests do not need a full Python build:

```sh
python3 experiments/workmeter/cpython/test_runtime.py
WM_LLVM_VERSION=16 python3 experiments/workmeter/cpython/standalone.py
```

The standalone LLVM gate verifies ordinary loops, used pure-function results,
cross-translation-unit effects and computed-goto control flow. Set
`WM_TEST_OUTPUT` to keep its generated files outside the source directory.
The full build additionally verifies Python outputs, dynamic stdlib imports,
hash-seed behavior, rejected threads, fork invalidation, and vector sums in both
the builder and final runtime image.
