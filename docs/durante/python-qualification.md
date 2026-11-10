# Python through Durante

CPython and PyPy now use the same Durante process-tree binary counter as native
programs. They run their existing interpreter commands: no rebuilt CPython,
LLVM pass, PyPy JIT hook, or Python-specific score converter is required.

The earlier PR #84 instrumented CPython LLVM IR and mapped PyPy JIT segments.
That independent experiment was useful for finding variance, but omitted work
inside external native libraries; its hashlib/zlib scores could remain nearly
constant while CPU work grew. Durante observes executed interpreter, generated
native, and shared-library instructions. It still excludes kernel work and does
not turn instruction counts into trusted CPU milliseconds or student verdicts.

## Reproducible native-library coverage gate

The fixed `hashlib_native.py` and `zlib_native.py` fixtures are retained from PR
#84. Each runs at input sizes 1, 2, and 4 million bytes with fixed iteration
counts. The verifier independently checks the expected digest or output length,
normal exit, complete uninterrupted process accounting, and enabled REP-byte
telemetry. Each larger input must increase total instruction work. This catches
the earlier flat native-library score; it does not establish linearity, universal
library coverage, cost acceptance, or a calibrated time formula.

```sh
python3 scripts/verify_durante_python.py \
  --runner-bin /absolute/path/to/durante \
  --drrun /absolute/path/to/DynamoRIO/bin64/drrun \
  --client /absolute/path/to/libdurante.so \
  --python python3 \
  --output-dir /absolute/path/to/new-private-results
```

`--python pypy3` exercises the same corpus when PyPy is installed. An unavailable
interpreter is missing coverage. The verifier requires Linux `prlimit`, applies
an inherited 1 GiB address-space ceiling, 150-second CPU ceiling and 16 MiB file
ceiling, and sets a per-observation 30-second wall timeout by default. The output
directory must be new. Raw stdout/stderr, lifecycle records, reports, installation
hashes and a final receipt are preserved on success and failure. Fixed trusted
fixtures are the only inputs; this utility is not an execution sandbox.

Local validation on 2026-11-11 used CPython 3.11.2, Durante `3fd6a73`, and
DynamoRIO 11.3.0-1 with REP telemetry enabled. All six observations preserved
output/exit and complete uninterrupted accounting:

| Fixture | Scale 1 instructions | Scale 2 instructions | Scale 4 instructions |
|---|---:|---:|---:|
| hashlib SHA-256 | 372,930,691 | 712,980,055 | 1,393,087,573 |
| zlib compress/decompress | 1,333,503,086 | 2,666,487,210 | 5,321,063,366 |

This receipt qualifies these stock-interpreter CLI fixtures. It does not qualify
the actual aonohako image/sandbox/API, arbitrary native extensions, Python child
creation, PyPy, or production timing. The corpus does not exercise subprocesses;
that independent compatibility gate must also pass before any permitted child
path is enabled. Instrumentation changes resource consumption, so measured raw
CPU/wall/RSS ceilings stay separate from nominal limits. Counters remain
application-writable and diagnostic-only.
