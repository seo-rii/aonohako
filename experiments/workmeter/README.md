# Interpreter work-meter experiment

This isolated experiment compares kernel CPU time with software work counters.
It does not change Aonohako's production runtimes, response fields, time units or
verdicts. Replacing the production interpreter with an instrumented interpreter
under unchanged CPU limits would make those limits stricter.

CPython uses the [LLVM runtime](cpython/README.md), fixed nonzero hash seed 1 and
the production `-I -S` runner prefix. Its score includes instrumented CPython
core and built stdlib C code. PyPy uses compile hooks and JIT segment counters
as an explicitly incomplete comparison. Neither score is CPU milliseconds or
a tamper-resistant accounting boundary. Reports marked valid passed internal
integrity checks; they do not establish complete work coverage.

The corpus has seven fixed-work families at scales 1, 2 and 4 in both runtimes:
Python arithmetic, sorting, large integers, strings, hashlib, zlib and regular
expressions. Eight exit/limit cases per runtime distinguish successful output,
SystemExit, exceptions, immediate exit, CPU limits, wall limits and unsupported
threads. Expected outputs use independent arithmetic identities and checked
digest constants. Missing measurements remain unavailable, never zero time.

`runner.py` starts a new process for each observation, records wait4 user/system
CPU and monotonic wall time, retains raw vectors and hashes, and kills/reaps the
target process group. `launch.c` applies diagnostic limits and drops privilege.
It is not the production sandbox. Only trusted preinstalled fixtures are accepted
by `server.py`; callers cannot supply source code or execution paths.

## Reproduce

Use Linux x86-64, Docker, a C compiler supporting static linking, and access to
the pinned base image. The native build records compiler/source identities and
checks the exact resulting image, but is not bit-for-bit reproducible across apt
repository updates. Retain its digest and provenance with every cohort.

```sh
docker build -t workmeter-native experiments/workmeter/cpython
mkdir -m 700 /tmp/workmeter-evidence
docker run --rm --entrypoint tar workmeter-native \
  -C /opt/interpreter-meter/cpython -czf - \
  metadata metadata-manifest.json provenance > /tmp/workmeter-evidence/native.tar.gz
tar -xzf /tmp/workmeter-evidence/native.tar.gz -C /tmp/workmeter-evidence
```

Create a `runtime-manifest.json` containing the native image identity in `image`
and the SHA-256 of the extracted `metadata-manifest.json` in
`metadata_manifest_sha256`. For a remote experiment use an immutable image digest.
Then prepare a public-only final image context:

```sh
python3 experiments/workmeter/prepare.py \
  --runtime-manifest /tmp/runtime-manifest.json --output /tmp/workmeter-context
docker build --build-arg RUNTIME_IMAGE=workmeter-native \
  -t workmeter-corpus /tmp/workmeter-context
mkdir -m 700 /tmp/workmeter-preflight
docker run --rm --network none --cpus 1 --memory 4g --pids-limit 256 \
  --mount type=bind,src=/tmp/workmeter-preflight,dst=/output \
  --entrypoint python3 workmeter-corpus -B /opt/interpreter-meter/preflight.py
```

Require `preflight.json` exit code 0 before collecting measurements. It records
every serial result plus a separate two-process stress check. Run the private
service on loopback locally or an explicitly provisioned private test service.
For each service use one vCPU, request concurrency 1 and startup CPU boost off.

```sh
docker run --rm --cpus 1 --memory 4g -p 127.0.0.1:8080:8080 workmeter-corpus
python3 experiments/workmeter/client.py --root /tmp/workmeter-context \
  --url http://127.0.0.1:8080 --repeats 3
python3 experiments/workmeter/analyze.py --root /tmp/workmeter-context \
  --evidence /tmp/workmeter-evidence --repeats 3
```

The service command runs in a separate terminal. Multiple `--url` arguments run
independent cohorts concurrently. For authenticated services, provide an identity
token through a private environment variable and name it with
`--identity-token-env`; do not put tokens in command arguments or logs. Collection
preserves failures before validation and never retries to select a faster result.
Run long builds and collection as detached jobs with private logs and an exit-status
receipt. No script here provisions or deletes cloud infrastructure.

`parallelism=2` intentionally overlaps two targets on one CPU as an interference
stress test. Production Eden distributes TC requests across single-run Aonohako
instances; this stress condition does not represent that dispatch path. Report
total CPU consumption, concurrent batch latency and deployment/cold readiness
separately. Client wall includes transport, pool creation and report handling.
Neither local nor diagnostic service latency is production Eden latency.

## Validation and interpretation

```sh
python3 -B -m unittest discover -s experiments/workmeter/tests -v
python3 -B experiments/workmeter/cpython/test_runtime.py -v
```

The analyzer independently validates compressed raw reports, native metadata
weights, JIT counter deltas/mappings, CPU components, actual CPython hash state,
cohort identities and expected exit behavior. It separates CPU CV from work-score
CV, serial runs from interference stress, and private submission samples from
public scaling diagnostics. A flat score while CPU work grows is a coverage
failure, even when its CV looks excellent. Selected-case maxima are not full
submission rejudges.

The model still omits external-library internals, variable-sized memory intrinsic
costs, assembly and kernel work. PyPy additionally omits interpreted execution,
native calls, GC and compilation work outside mapped JIT segments. Forced
termination may produce no completed report. A stable score alone cannot justify
converting existing millisecond limits or changing AC/TLE decisions.

Initialization follows [CPython's PyConfig API](https://docs.python.org/3.13/c-api/init_config.html).
The PyPy comparison uses [documented JIT hooks](https://doc.pypy.org/jit-hooks.html);
their counters describe JIT behavior, not complete program resource consumption.
