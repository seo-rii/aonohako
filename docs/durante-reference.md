# C++ reference-time qualification

`cmd/durante-reference` collects fixed C++20 programs through Aonohako's actual
`/compile` and `/execute` handlers, then evaluates a two-coefficient diagnostic
time policy. It starts a temporary loopback API; it does not connect to a running
judge or change its configuration. A successful fit is a reference-cohort result,
not authorization to use the estimate as an official verdict.

## Collection contract

Every program is compiled once using the normal CPP20 registry (`g++ -O2 -Wall
-lm --static -pipe -DONLINE_JUDGE=1 -std=c++20`). Each case reuses exactly that
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
overflow reject collection/fitting. Dataset schema and corpus ID remain 1 and
`cpp-reference-v1`; the two-coefficient fitter continues to use `instructions`
and aggregate `rep_bytes`, with the same train/validation split and gates.

The current collector uses `dynamorio-11.3-durante-v5-vm32m` and pins both runtime
patches. Older seven-field installation identities and v3/v4 reports can be decoded
for inspection, but the current fitter rejects their old installation/profile.
Keep the original dataset and matching historical fitter for reproducing past
results. A current-runtime model requires fresh collection; do not relabel old
identities, fill absent detail with zeros, or reuse old coefficients as qualified.

Both paths use CPU 10,000 ms, wall 30,000 ms, and memory 256 MiB limits.
Collection requires root Linux amd64, embedded/helper execution, one active run,
the `cpp` family, default dispatch, and an explicit resource policy and image
digest. Controlled glibc dispatch, interpreters, peers, remote runners and memory
calibration are outside this corpus. The default total timeout is 20 minutes
(maximum 1 hour); cancellation retains an incomplete dataset.

## Run in the intended execution image

Build the tool using the repository's Go version:

```sh
CGO_ENABLED=0 go build -o /tmp/durante-reference ./cmd/durante-reference
```

Use an immutable execution image with the normal CPP20 compiler/static libraries
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
  collect --output /output/full --repetitions 3 --timeout 20m
sudo chown -R "$(id -u):$(id -g)" "$output"
```

`dataset.json` is checkpointed atomically after each completed triplet and on
return, including failure. Output directories must be new; files are private
(0600, directory 0700). Keep the raw dataset with the model's reference record.
Rerunning uses a new directory and a new complete collection.

For CI only, `docker/durante.Dockerfile --target reference-tools` provides a small
compiler image. CI mounts its verified SDK/client, collects with `--smoke
--repetitions 1`, and retains the dataset artifact. It tests the actual compile
and execute path without treating hosted-runner timing as a production model.

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

An exported policy applies only to its explicit `CPP20@sha256:...` runtime,
default dispatch and recorded cohort. A different image, engine, source variant
or materially different target population needs new qualification. Reproducible
counts alone do not establish a general mapping from two work dimensions to
CPU time, and this collection does not establish credited native memory.
For API configuration and unchanged verdict semantics, see
[Durante integration](durante.md#explicit-reference-time-calculator).

## Local qualification result

The 2026-11-15 (KST) local run completed all five smoke cases and all 45 full triplets
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
