# Runtime reference-time qualification

`cmd/durante-reference` collects explicitly selected runtime programs through Aonohako's actual
`/compile` and `/execute` handlers, then compares two-coefficient diagnostic time models. It starts a temporary loopback API; it does not connect to a running
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
`cpp-reference-v1`. Select `AONOHAKO_DURANTE_LATENCY_BOUND=v1` for a paired model comparison.
The collector records `latency_bound_version: 1` at the dataset level. Every
metered sample must carry the matching process-tree `latency_bound.cycles_x16`
receipt. Both time models use the same compiled binaries, native observations
and training/held-out partition.

The current collector uses `dynamorio-11.3-durante-v7-vm32m` and pins both runtime
patches. A collection made with `AONOHAKO_DURANTE_CPUID_PROFILE` records that
profile as the dataset's `cpuid_profile`, requires every metered receipt to match
it, and the fitted candidate policy carries the same `cpuid_profile`, so it
applies only to identically profiled runs. Likewise, a collection made with
`AONOHAKO_DURANTE_RUNTIME_DETERMINISM=v1` records `runtime_determinism` for a
pinned source (CPython, PyPy, Node), requires every metered report to carry it, and
fits only such reports; bind the resulting coefficients through a schema 2
descriptor generated with the same setting. Older seven-field installation identities and v3/v4 reports can be decoded
for inspection, but the current fitter rejects earlier installation/profile identities, including v5.
Keep the original dataset and matching historical fitter for reproducing past
results. A current-runtime model requires fresh collection; do not relabel old
identities, fill absent detail with zeros, or reuse old coefficients as qualified.

Native execution uses a 10,000 ms CPU limit and a 30,000 ms wall limit.
Instrumented execution defaults to the same limits. For a slower counter,
`--metered-cpu-ms` and `--metered-wall-ms` select a larger raw envelope for
that execution only. The collector checks it against the operator's resource
policy before starting and records it for offline validation. Memory defaults to 256 MiB for
CPP20 and 512 MiB for the new runtime corpus, accommodating interpreter startup.
`--memory-mb` can explicitly select another positive limit up to 4,096 MiB; the
resolved value is recorded in both `native_limits` and `metered_limits`. It must
fit the operator's resource policy and total admission budget. The collector
never silently increases that policy or substitutes a smaller limit.
The collector disables automatic Durante observations in its private API instance
so each trial runs exactly one native control, one metered execution and one
native control. This also applies when the operator sets
`AONOHAKO_DURANTE_OBSERVE=on` for ordinary submissions. Collection and offline
fitting reject a triplet if any phase carries an automatic observation receipt.

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
  -e AONOHAKO_DURANTE_CPUID_PROFILE=x86-64-v2 -e AONOHAKO_DURANTE_LATENCY_BOUND=v1 \
  -e AONOHAKO_DURANTE_RUNTIME_IMAGE="$image_id" \
  -e 'AONOHAKO_DURANTE_RESOURCE_POLICY={"max_limits":{"cpu_time_ms":60000,"wall_time_ms":90000,"memory_mb":256},"memory_budget_mb":2048,"reserved_memory_mb":512}' \
  --entrypoint /validation/reference-tool "$image_id" \
  collect --language CPP20 --memory-mb 256 --metered-cpu-ms 60000 --metered-wall-ms 90000 \
    --output /output/full --repetitions 3 --timeout 20m
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
source/corpus metadata. `--method weighted-work-nanoseconds-v1` fits instructions and aggregate REP
bytes. `--method latency-bound-rep-nanoseconds-v1` fits version 1
`cycles_x16` and the same aggregate REP bytes. Each method uses the same
default thresholds. The latency fit also checks the spread of the latency
counter against the work-spread threshold. The fitted latency coefficient
already accounts for the counter's unit of one sixteenth of a modeled cycle.

Run both methods over the same dataset, including when the first candidate
fails a quality gate.

```sh
for method in weighted-work-nanoseconds-v1 latency-bound-rep-nanoseconds-v1; do
  status=0
  /tmp/durante-reference fit --input "$output/full/dataset.json" \
    --output "$output/$method" --method "$method" --model-id "cpp-$method" \
    --model-version 1 --reference-id YOUR_COLLECTION_RECORD || status=$?
  case "$status" in 0|2) ;; *) exit "$status" ;; esac
done
```

Each evaluation records the input SHA-256 and the worst observed prediction
error per case. Keep both evaluations and any failed gates with the dataset.

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

## Validate a frozen policy on external C++ solutions

Use an external manifest to check an existing candidate on programs that were
kept out of model development. Choose the programs, inputs, independently
computed stdout and policy before running them. Keep the exact files, their
provenance and the host reservation with the measurement record.

The manifest is a single UTF-8 JSON document. Each workload contains the full
CPP20 source and one or more numbered cases. For example, this short program
illustrates the format. Accuracy validation uses inputs long enough to pass
the native duration gate.

```json
{
  "schema": 1,
  "language": "CPP20",
  "workloads": [{
    "id": "sum-pair",
    "source": "#include <iostream>\nint main() { long long a,b; std::cin >> a >> b; std::cout << a+b; }",
    "cases": [{"scale": 1, "input": "5 8\n", "expected_stdout": "13\n"}]
  }]
}
```

Use a unique workload ID (1–64 ASCII letters, digits, dots, hyphens or
underscores, starting with a letter or digit), positive distinct scales per
workload, and a nonblank stdout oracle. An explicit empty input is supported.
The manifest allows up to 64 workloads, 128 total cases and 16 MiB of JSON.
Source and input each have a 1 MiB limit and each oracle has a 64 KiB limit.

Supply a standalone diagnostic policy with the exact runtime image, v7 engine,
dispatch and CPUID profile. The command supports the instruction/REP, split
MOVS/STOS and latency/REP formulas. The latency formula needs the v1 latency
counter enabled during collection. Keep the policy outside the judge
configuration. In the collection container shown above, mount the two input
files read-only and keep the output directory private.

```sh
/validation/reference-tool collect --language CPP20 \
  --validation-manifest /inputs/manifest.json --frozen-policy /inputs/frozen-policy.json \
  --repetitions 3 --metered-cpu-ms 60000 --metered-wall-ms 90000 --output /output/heldout
```

The collector hashes the exact bytes of both files before compiling. Each
case reuses its compiled artifact for all native-before, Durante and native-after
executions. Every external observation belongs to the validation split.
Evaluate the collected dataset offline with the same files.

```sh
/tmp/durante-reference validate --input "$output/heldout/dataset.json" \
  --validation-manifest "$inputs/manifest.json" --frozen-policy "$inputs/frozen-policy.json" \
  --output "$output/evaluation"
```

`validate` checks the default gates in the table above on every case, including
the prediction error on every repetition. Its only output file is
`evaluation.json`, which records the dataset, manifest and policy digests. Exit
0 means the frozen policy passed on this cohort, 2 means a measured quality or
error gate failed, and 1 means the inputs or execution could not be validated.
A changed input file requires a fresh collection. `fit` accepts only the
built-in training corpora. For operating decisions, record the host CPU,
affinity, competing load and how the selected programs represent the intended
submission population.

### Compare actual native and Durante verdicts

After collecting a frozen external CPP20 dataset, use the following command
to replay its programs on the same reference host and pinned CPU. Use the
compiler and runtime image from collection. Build `./cmd/server` alongside
`./cmd/durante-reference` and mount both executables and the repository's
`scripts` directory read-only. Run as root in the network-isolated container
described above, with Python 3 available, the frozen inputs mounted read-only
and a writable output parent. The tool creates the specified output directory.

```sh
python3 /scripts/verify_durante_native_boundaries.py \
  --server /validation/server --reference /validation/reference-tool \
  --runtime-image "$image_id" --manifest /inputs/manifest.json \
  --frozen-policy /inputs/frozen-policy.json --dataset /inputs/dataset.json \
  --output /output/boundaries
```

For each workload and scale, the low CPU limit is 80% of its native median
rounded down to whole milliseconds (at least 1 ms). The high limit is 120%
rounded up. A median too short to bracket at whole-millisecond resolution
is recorded as an exclusion. Both judges receive the same compiled artifact, input, expected
stdout and nominal limits. At each limit the tool obtains one actual Durante
verdict and three ordinary native verdicts. A native timeout must come from
the CPU limit. A Durante timeout requires evidence of exhausted weighted
work under the bound model. A raw watchdog stop cannot count as an agreement.

Exit 0 means the Durante verdict matched all three native verdicts at every
limit. Exit 2 records measured disagreement or mixed native outcomes. Exit 1
indicates an incomplete or invalid comparison. The output contains individual
HTTP results and a checkpointed `summary.json` with their digests and counts
for each direction of disagreement. Check its
`reference_validation_exit` as well. A value of 2 records a failed accuracy
or stability gate even if the actual verdicts agreed. The tool starts temporary
loopback servers inside the test container.

## GCE CPP20 qualification, 2026-11-19

A temporary Google Compute Engine `n2d-standard-4` VM ran the v7 collector
and the Aonohako judge on an AMD EPYC 7B12. The VM had one thread per core,
and the grading container ran one execution at a time on guest CPU 1. The VM
was reserved for this experiment. The physical host could serve other tenants.
The server was built from merge commit `fdc8525` and the collector from
its PR head `6bb1ce4`. Both commits have the same Git tree. All runs used
the same static CPP20 compiler image. Durante used the `x86-64-v2` CPUID
profile and default dispatch.

The development run paired three Durante measurements with six native CPU
controls for each of 15 program/scale combinations. A new candidate used all
five previously exposed program families to minimize the largest relative
error with nonnegative weights for instructions, MOVS bytes and STOS bytes.
Its weights were frozen before running the external solutions. Nine of 45
development estimates were within 20% of their case median.

The data also gives a lower bound for *any* nonnegative choice of those three
weights. Branch at scale 2 executed at least 458,749,200 instructions, while
sort at scale 4 executed at most 373,690,919. Both had zero MOVS bytes and 3,624 STOS
bytes. The model must assign branch at least as much time as sort. Yet the
slowest native branch took 66.455 ms and the fastest native sort took
141.451 ms. Even at those favorable endpoints, one of the two relative errors
must be at least `(141.451 − 66.455) / (141.451 + 66.455) = 36.07%`.

The external cohort used six author-published CSES solutions. Edit Distance,
Counting Rooms and Factory Machines had appeared in an earlier experiment.
Towers, Removal Game and Counting Divisors were reserved before this run.
Each used a fixed input and an independently computed stdout oracle. Each
of three policies ran its own 18 native-before / Durante / native-after
triplets. The collector allowed 10 s CPU and 30 s wall per native execution,
and 60 s CPU and 90 s wall per instrumented execution.

| Frozen policy | Repetitions within ±20% | Fresh subset within ±20% | Largest absolute error |
|---|---:|---:|---:|
| Historical instruction + REP | 3/18 | 3/9 | 270.61% |
| Historical instruction + MOVS + STOS | 0/18 | 0/9 | 207.88% |
| GCE instruction + MOVS + STOS | 0/18 | 0/9 | 200.89% |

Factory Machines and Counting Divisors each had a native sample below the
20 ms minimum in every round. Counting Divisors also exceeded the 10% spread
limit in both historical-policy rounds. All three estimates within ±20% in
the first round came from Counting Divisors. All three policies failed
qualification. In the GCE round, Edit Distance met both native duration and
spread gates, yet its estimated 120.732 ms was three times its native median
of 40.125 ms.

### Verdicts at native CPU limits

The GCE-fitted policy was bound to the test judge. Each program was run at
two nominal CPU limits derived from its six native controls collected under
the 10 s CPU limit. The low limit was 80% of the median rounded down to whole
milliseconds, and the high limit was 120% rounded up. At each limit the
judge ran the same binary and input once with Durante and three times
through ordinary native grading. The native runs allowed 30 s wall, and
Durante retained its 60 s CPU / 90 s wall watchdog.

| Program | Low / high limit (ms) | Native low / high (all 3 runs) | Durante low / high |
|---|---:|---|---|
| Edit Distance | 32 / 49 | TLE / AC | TLE / TLE |
| Counting Rooms | 17 / 27 | TLE / AC | TLE / TLE |
| Factory Machines | 15 / 24 | TLE / AC | TLE / TLE |
| Towers | 21 / 33 | TLE / AC | TLE / TLE |
| Removal Game | 46 / 70 | TLE / AC | AC / AC |
| Counting Divisors | 15 / 24 | TLE / AC | AC / AC |

Every native timeout came from its CPU limit, every Durante timeout came from
the live work clock, and every acceptance matched the expected stdout. Across
these twelve limits, Durante timed out four cases that native grading accepted
and accepted two cases that native grading timed out. Four of the eight limits
from programs meeting both native timing quality gates also disagreed. These
are paired observations on six programs from one guest. The GCE candidate
does not qualify for deciding submissions.

The separate judge smoke used a fixture model on both direct and forwarded
requests. Four finite executions passed with full credit. Durante stopped two
busy loops when their measured work exceeded the nominal limit. Two sleeping
processes reached the outer wall watchdog and received no score. Four
requests with insufficient watchdog headroom were rejected, as was a
forwarded request with a mismatched model binding.

The [measurement record](durante/gce-cpp20-v7-qualification.json) includes
per-repetition native CPU times, counter values and policy coefficients. To
recreate the exact external manifest, run the following from this repository
with a checkout of the upstream sources at commit
`eeaf7b164735a4747493b0555edf688479014448`.
[Upstream solutions](https://github.com/ks-iitjmu/MyCSES/tree/eeaf7b164735a4747493b0555edf688479014448)

```sh
python3 scripts/durante_gce_cpp20_manifest.py /path/to/MyCSES -o combined.json
sha256sum combined.json
# 6d41c52f5f923f8a2deeba794f6c3f2b58de8f2318ec2559aa1caf7e4c44912a
```

## Paired CPP20 qualification, 2026-11-18

A shared AMD EPYC 7B12 KVM host ran the v6 engine with fixed `x86-64-v2`
CPUID and the v1 latency counter. The static g++ 14.2.0 runtime image was
`sha256:5f1fee5fb589c9494f2155478bab0e01af620f73e99ebf2ff9177a09626d5bf3`.
In the first full collection the raw 10,000 ms CPU limit stopped the instrumented
branch case at scale 4 after 24 complete triplets. The incomplete run was
retained. A fresh collection used 60,000 ms raw CPU and 90,000 ms raw wall;
native limits remained 10,000/30,000 ms. All 45 full triplets completed (135
executions), with matching output and a latency receipt for each instrumented
execution. Both fits used the same dataset, SHA-256
`d1fd7c1f03e541a75ff45fef104a9e3af9de16956995c514634ec187ccde11d7`.

Each cell below shows the signed error for the worst repetition at each
held-out scale (1, 2, 4), relative to the native CPU median. Positive is an
overestimate. The predeclared maximum absolute error was 20%.

| Held-out program | Instructions + REP bytes | Latency bound + REP bytes |
|---|---:|---:|
| Branch | +73.30%, +77.97%, +78.81% | −45.76%, −44.30%, −44.03% |
| Sort | −37.80%, −35.88%, −33.80% | −80.53%, −79.93%, −79.28% |
| REP fill | +61.82%, +62.81%, +70.97% | +62.14%, +68.34%, +79.71% |

Both models failed all 27 held-out repetitions. Six of the 15 program/scale
combinations also exceeded the 10% native spread gate (maximum 30.69%).
Branch and sort passed that spread gate and still failed on prediction error.
Neither fit exported a policy. Repeating on a dedicated reference host would
test how much host variability contributed. A revised model needs a new
predeclared validation set, especially for branch, sort and memory work.

Two held-out inputs show why refitting positive weights alone cannot meet
the gate on this dataset. In all three repetitions, branch at scale 1 has
at least 229,460,596 instructions and 917,843,604 latency units. Sort at
scale 2 has 177,135,248 instructions and 708,542,180 latency units. Both
have 3,624 REP bytes. Any nonnegative weighting of these counters therefore
estimates at least as much time for the branch case. Its native median is
34.1025 ms and the largest passing prediction is 40.923 ms. The sort median
is 71.154 ms and its smallest passing prediction is 56.9232 ms. Those bounds
require the opposite order. This post-collection check uses the reported
observations to identify a missing distinction and fits no new weights.

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
