# Runtime reference-time qualification

`cmd/durante-reference` collects explicitly selected runtime programs through Aonohako's actual
`/compile` and `/execute` handlers, then evaluates a two-coefficient diagnostic
time policy. It starts a temporary loopback API; it does not connect to a running
judge or change its configuration. A successful fit is a reference-cohort result,
not authorization to use the estimate as an official verdict.

## Collection contract

Every program is compiled once using its normal source-language registry. CPP20
retains `g++ -O2 -Wall -lm --static -pipe -DONLINE_JUDGE=1 -std=c++20`. Each case reuses exactly that
artifact and input in an ordered native-before → Durante → native-after triplet.
The native executions must return AC against an independent checksum oracle;
the Durante execution must complete with matching output, complete uninterrupted
counts, and diagnostic-only authority. Its outer status remains initialization
failure, as required by the raw resource-envelope API.

The fixed `cpp-reference-v1` corpus separates whole algorithms before collecting
observations:

| Program | Split | Work |
|---|---|---|
| `integer` | train | Unsigned integer recurrence |
| `rep_copy` | train | Explicit x86 `rep movsb` and checksum |
| `branch` | validation | Branch-dependent integer work |
| `sort` | validation | Sorting and weighted checksum |
| `rep_fill` | validation | Explicit x86 `rep stosb` and checksum |

`collect --language` selects a single source profile. Omission preserves CPP20;
aliases are resolved to the canonical ID stored in the dataset. Only profiles
with fixed implementations are accepted; registering another Durante family does
not supply a corpus for it.

| Source profile | Family | Corpus identity | Artifact executed in all three phases |
|---|---|---|---|
| `CPP20` | `cpp` | `cpp-reference-v1` | The existing static amd64 `Main` executable |
| `PYTHON3` | `cpython` | `runtime-reference-v1` | `Main.pyc` from the selected CPython compile handler |
| `PYPY3` | `pypy` | `runtime-reference-v1` | `Main.pyc` from the selected PyPy compile handler |
| `RUST2024` | `native` | `runtime-reference-v1` | Normal rustc edition-2024 `Main` executable, which may link dynamically |

The new runtime corpus uses independent `integer` and `copy` training programs,
with `branch`, `sort` and `fill` held out. All three runtime profiles use the same
inputs, algorithm semantics, split and independent checksum oracles. Integer
results use modulo-2^64 arithmetic. Copy/fill use each runtime's normal memory
operations; their names do not promise a particular REP instruction or count.
The exact existing CPP20 sources, input sizes, hashes and splits remain unchanged.

Python compilation uses isolated `compileall` with checked-hash bytecode. The
collector validates that the returned source matches the fixture, then executes
only the compiled `Main.pyc`, so it cannot silently fall back to source execution.
`binary_sha256` identifies that bytecode, not the interpreter. The selected
interpreter version/path and immutable image identify the runtime separately.
Rust uses the standard-library-only compiler path; Python execution explicitly
selects the standard-library-only environment. No external packages are required.
Rust collection admits the API's normal 16 MiB single-artifact limit, including
its base64 response; the existing CPP20 and Python artifact cap remains 8 MiB.
The collector never strips or otherwise rewrites the compiled artifact.

Full collection has three scales (1, 2, 4) and 3–9 repetitions, default 3:
45 triplets / 135 executions plus five compilations. Smoke collection uses five
short cases and permits one repetition. Smoke data cannot qualify a policy.
Inputs, source, expected output, and compiled artifact hashes are recorded.
Native CPU is `cpu_time_ns` with normalization and short-case sampling disabled;
wall time and Durante's instrumented CPU time are not regression targets.
All raw responses and CPU-accounting metadata remain in the dataset. The tool
does not discard slow repetitions or replace failed executions.

Each `metered.work_measurement` retains the optional version-1 `rep_breakdown`
(MOVS/STOS requested bytes and entries), including explicit zero values. Missing
legacy detail means unspecified, not zero. Invalid partitions or counter
overflow reject collection/fitting. Dataset schema remains 1. The source-language
ID and corpus ID are validated together; a runtime dataset cannot be relabeled as
`cpp-reference-v1`. The two-coefficient fitter continues to use `instructions`
and aggregate `rep_bytes`, with the same train/validation split and gates.

The current collector uses `dynamorio-11.3-durante-v5-vm32m` and pins both runtime
patches. A collection made with `AONOHAKO_DURANTE_CPUID_PROFILE` records that
profile as the dataset's `cpuid_profile`, requires every metered receipt to match
it, and the fitted candidate policy carries the same `cpuid_profile`, so it
applies only to identically profiled runs. Likewise, a collection made with
`AONOHAKO_DURANTE_RUNTIME_DETERMINISM=v1` records `runtime_determinism` for a
pinned source (CPython, PyPy, Node), requires every metered report to carry it, and
fits only such reports; bind the resulting coefficients through a schema 2
descriptor generated with the same setting. Older seven-field installation identities and v3/v4 reports can be decoded
for inspection, but the current fitter rejects their old installation/profile.
Keep the original dataset and matching historical fitter for reproducing past
results. A current-runtime model requires fresh collection; do not relabel old
identities, fill absent detail with zeros, or reuse old coefficients as qualified.

Both paths use CPU 10,000 ms and wall 30,000 ms. Memory defaults to 256 MiB for
CPP20 and 512 MiB for the new runtime corpus, accommodating interpreter startup.
`--memory-mb` can explicitly select another positive limit up to 4,096 MiB; the
resolved value is recorded in both `native_limits` and `metered_limits`. It must
fit the operator's resource policy and total admission budget. The collector
never silently increases that policy or substitutes a smaller limit.
Collection requires root Linux amd64, embedded/helper execution, one active run,
the selected source's enabled family, default dispatch, and an explicit resource policy and image
digest. Controlled glibc dispatch, JVM, peers, remote runners and memory
calibration are outside this corpus. The default total timeout is 20 minutes
(maximum 1 hour); cancellation retains an incomplete dataset.

## Run in the intended execution image

Build the tool using the repository's Go version:

```sh
CGO_ENABLED=0 go build -o /tmp/durante-reference ./cmd/durante-reference
```

Use an immutable execution image with the selected compiler/interpreter
and the pinned Durante SDK/client installed at their standard `/opt/durante`
paths. Mount the tool read-only into that exact image. The image digest is
operator-supplied deployment metadata, not attestation by the measured program.
The dataset also records the collector executable SHA-256, compiler version and
flags, kernel and CPU model. Review those records when comparing collections.

Example for an existing local image (`REFERENCE_IMAGE` must identify that image):

```sh
image_id=$(docker image inspect --format '{{.Id}}' "$REFERENCE_IMAGE")
output=$(mktemp -d)
docker run --rm --network none --read-only --memory 2g --pids-limit 512 --cpus 2 \
  --tmpfs /tmp:rw,exec,mode=1777,size=268435456 \
  --tmpfs /work:rw,exec,mode=0755,size=268435456 \
  --mount type=bind,src=/tmp/durante-reference,dst=/validation/reference-tool,readonly \
  --mount type=bind,src="$output",dst=/output \
  -e AONOHAKO_DEPLOYMENT_TARGET=dev -e AONOHAKO_INBOUND_AUTH=none \
  -e AONOHAKO_EXECUTION_TRANSPORT=embedded -e AONOHAKO_SANDBOX_BACKEND=helper \
  -e AONOHAKO_MAX_ACTIVE_RUNS=1 -e AONOHAKO_WORK_ROOT=/work \
  -e AONOHAKO_WORK_ROOT_MAX_BYTES=268435456 \
  -e AONOHAKO_CPU_NORMALIZATION=false -e AONOHAKO_CPU_SHORT_CASE_SAMPLING=false \
  -e AONOHAKO_DURANTE_FAMILIES=cpp -e AONOHAKO_DURANTE_DISPATCH_PROFILE=default \
  -e AONOHAKO_DURANTE_RUNTIME_IMAGE="$image_id" \
  -e 'AONOHAKO_DURANTE_RESOURCE_POLICY={"max_limits":{"cpu_time_ms":10000,"wall_time_ms":30000,"memory_mb":256},"memory_budget_mb":2048,"reserved_memory_mb":512}' \
  --entrypoint /validation/reference-tool "$image_id" \
  collect --language CPP20 --memory-mb 256 --output /output/full --repetitions 3 --timeout 20m
sudo chown -R "$(id -u):$(id -g)" "$output"
```

`dataset.json` is checkpointed atomically after each completed triplet and on
return, including failure. Output directories must be new; files are private
(0600, directory 0700). Keep the raw dataset with the model's reference record.
Rerunning uses a new directory and a new complete collection.

For a Python smoke collection, use an image containing `python3`, enable
`AONOHAKO_DURANTE_FAMILIES=cpython`, provision `max_limits.memory_mb` of at least
512, and run `collect --language PYTHON3 --memory-mb 512 --smoke --repetitions 1
--output /output/python-smoke`. Select `pypy`/`PYPY3` or `native`/`RUST2024` in the
same way, with `pypy3` or `rustc` installed. Each selection writes its own dataset;
collection never enables a `TimeModels` binding or applies configured coefficients.

For CI only, `docker/durante.Dockerfile --target reference-tools` provides a small
compiler/interpreter image with CPP20, CPython, PyPy and Rust tools. CI mounts its
verified SDK/client, collects each profile with `--smoke --repetitions 1`, and
attempts all four profiles even if an earlier one fails, and retains each exit
status and dataset plus `collection-summary.json`. Any failed or missing result
fails the final step. It tests the actual compile and execute path without
treating hosted-runner timing as a production model.

## Fit and evaluate offline

Choose model/reference identities and quality thresholds before collecting the
qualification data. The defaults are explicit starting gates, not a universal
accuracy guarantee:

| Gate | Default |
|---|---|
| Minimum repetitions per case | 3 |
| Minimum CPU of every native observation | 20 ms |
| Native range / median | ≤10% |
| Instruction and REP-byte range / respective median | ≤0.1% |
| Absolute prediction error / native median, every metered repetition | ≤20% |

```sh
/tmp/durante-reference fit \
  --input "$output/full/dataset.json" --output "$output/fit" \
  --model-id cpp-reference-candidate --model-version 1 \
  --reference-id YOUR_COLLECTION_RECORD
```

The tool verifies the complete fixed corpus, splits, hashes, limits, CPU sources,
output agreement, engine/client installation identity and diagnostic report
contracts. The native reference for each case is the median of all `2*N` native
observations (an even midpoint rounds upward to integer nanoseconds). Training
minimizes squared relative error through the origin, with equal weight per case
and `1/N` per repetition. Only the two training programs determine coefficients.
Validation programs never influence fitting.

The fitter chooses the corpus and report family from the dataset's canonical
source/corpus metadata. `--method weighted-work-nanoseconds-v1` is the default
and currently the only fitted method; unsupported corpora, languages and methods
fail explicitly. Runtime workloads may expose too little REP variation to
identify both weights. Such a dataset receives the existing rank/conditioning
failure, not an invented REP coefficient or a silent one-variable fit.

The two-variable normal equations use exact rational arithmetic. Singular or
near-collinear designs (`det/(A*C) < 10^-12`), nonpositive weights, coefficients
that quantize to zero, and numeric overflow are rejected. Positive weights round
to a common denominator of 1,000,000,000. Every actual repetition is then
re-evaluated using `TimePolicy.Estimate`, including its exact combined rounding.
Each case's reported estimate/error belongs to its worst absolute-error
repetition. Gate comparisons use exact products rather than rounded display PPM.

`evaluation.json` binds the result to the exact input file SHA-256 and records
thresholds, case results, failure reasons and any fitted candidate. `policy.json`
is written last, only if all gates pass. Exit codes are 0 for pass, 2 for measured
quality/model rejection, and 1 for execution, data, cancellation or I/O errors.
Do not copy a failed receipt's candidate into operator configuration. Fix the
measurement/model problem and evaluate a new complete collection; do not remove
held-out cases or relax thresholds after seeing their errors.

An exported policy applies only to its explicit `SOURCE@sha256:...` runtime,
default dispatch and recorded cohort. A different image, engine, source variant
or materially different target population needs new qualification. Reproducible
counts alone do not establish a general mapping from two work dimensions to
CPU time, and this collection does not establish credited native memory.
The optional shared-model registry can reuse coefficients across explicitly
approved execution profiles. A successful single-language fit supplies no
qualification for another language. Independently fitting every language can
also produce different coefficients; that does not validate one common model.
A common model requires evaluating the same fixed coefficients against every
target runtime's held-out observations and passing every gate. This command
does not perform that combined approval or export registry bindings. Non-C++
resolved policies belong in the explicit shared registry only after the operator
has reviewed the applicable evidence; the legacy single-policy environment
setting retains its C/C++ restriction.
For API configuration and unchanged verdict semantics, see
[Durante integration](durante.md#explicit-reference-time-calculator).

## Local multi-language smoke result

The 2026-11-15 local Linux amd64 smoke run used the verified v4 SDK/client and
reference-tools image
`sha256:fe9efbe763f085e889f12fd3d0f27329fb13d1adb71032ef8f89603c8080e467`.
Each profile completed five compile/native/Durante/native cases with one
repetition: 60 executions total, matching outputs, complete work counters and
zero observed signals.

| Profile | Toolchain in that image | Completed triplets |
|---|---|---|
| `CPP20` | g++ 14.2.0 | 5 |
| `PYTHON3` | CPython 3.13.5 | 5 |
| `PYPY3` | PyPy 7.3.19 / Python 3.11.11 | 5 |
| `RUST2024` | rustc 1.85.1 | 5 |

The first Rust attempt stopped after compilation because its normal executable
exceeded the collector's previous 8 MiB cap. Its incomplete receipt was retained;
a separate rerun passed after aligning the Rust artifact and HTTP budgets with
the API's 16 MiB artifact limit. A subsequent campaign ran all four profiles
with the same final collector, SHA-256
`eda175a4168f0ee1159ac42f79711965e7da5ce8e789eb23777fe7e6570c5fcb`.
All sixty executions passed in 91.538 seconds, with unchanged source/runtime
inputs and complete container cleanup. The original incomplete Rust receipt
remains preserved. CI also runs all four profiles against one collector build.
These smoke results do not test full-cohort repeatability gates, fit coefficients
or qualify a model.

## Local qualification result

The earlier CPP20 run on 2026-11-15 (KST) completed all five smoke cases and all 45 full triplets
(135 full executions) through the real API, with matching outputs. It used the
default gates above and the immutable compiler/runtime image
`sha256:5f1fee5fb589c9494f2155478bab0e01af620f73e99ebf2ff9177a09626d5bf3`.
The full dataset SHA-256 is
`bb96fda9aa5a4a9927e384c384241a1dd9aeb5d6a17bbb28f54780de7a3adef1`.
The reference host was AMD EPYC 7B12 with kernel `6.1.0-54-cloud-amd64`, g++
14.2.0, and a two-CPU/2-GiB container allocation. The collector executable hash
was `69bf86d3ec60112f1fbd16222c915dd50b9e209d2494f0f8f553e3056637d626`.

This was a v3 collection without MOVS/STOS detail, before the v4 vendor update.
It remains historical evidence; it has not been relabeled or requalified.

The candidate **failed qualification** and no policy was exported:

| Program | Absolute error across the three scales | Direction |
|---|---|---|
| integer (train) | 0.13–2.09% | mixed |
| REP copy (train) | 5.28–11.43% | mixed |
| branch (validation) | 67.42–74.79% | overestimate |
| sort (validation) | 32.69–36.64% | underestimate |
| REP fill (validation) | 75.26–89.74% | overestimate |

Instruction/REP counters were stable within one displayed PPM, but six cases
also exceeded the 10% native CPU spread gate (maximum 24.28%). Even validation
cases with stable native CPU failed the prediction gate. This is evidence that
the current instruction-plus-REP-byte model is insufficient for this corpus;
counter repeatability does not make its coefficients portable between these
algorithms. Additional workload-sensitive accounting/model design and a quieter
reference collection are needed before another qualification attempt. The
thresholds were not relaxed and no cases were removed to accept this result.
