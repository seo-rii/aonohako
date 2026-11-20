# Durante v10 clock accuracy against native CPU on Cloud Run

None of the four language fits qualified for grading against native CPU time. The frozen v10 trial clock met the ±20% error target on 7 of 36 held-out cases. Separate language fits met it on 10 of 36. For C++20, CPython and PyPy, even the most favorable observed native timings leave errors above 20% for some pairs of programs under any fixed nonnegative instruction, MOVS-byte and STOS-byte weights. Rust's native runs were all shorter than the protocol's 20 ms minimum. [Fixed protocol](v10-native/protocol.json) · [Verified scores](v10-native/summary.json)

## Results by language

Each held-out case is one algorithm at one input scale. It counts as within ±20% only when every repetition falls within 20% of that case's native median. The native-spread and range columns cover all 15 cases, including training. A range miss means all three frozen estimates fall below 80% of the lowest native observation or above 120% of the highest.

| Language | Frozen clock within ±20% on held-out cases | Worst held-out error, frozen | Language fit within ±20% on held-out cases | Native spread above 10% | Frozen range misses | Diagnostic CPU / native CPU |
|---|---:|---:|---:|---:|---:|---:|
| C++20 | 0/9 | 62.0% | 0/9 | 10/15 | 12/15 | 17.42× |
| CPython 3.13.5 | 0/9 | 260.7% | 4/9 | 12/15 | 14/15 | 168.14× |
| PyPy 7.3.19 (Python 3.11.11) | 4/9 | 67.8% | 0/9 | 12/15 | 5/15 | 89.96× |
| Rust 2024 (rustc 1.95.0) | 3/9 | 59.5% | 6/9 | 14/15 | 10/15 | 89.20× |

For C++20, the frozen clock overestimated all three branch and fill scales and underestimated all three sort and copy scales, even after allowing the full native range and the 20% margin. For CPython, the frozen clock overestimated all nine held-out cases relative to their native medians. With language-specific coefficients, all three branch scales and the smallest fill passed the prediction-error gate. [C++20 archive and fit](v10-native/CPP20/) · [CPython archive and fit](v10-native/PYTHON3/)

CPython's instruction counts exceeded the 0.1% repeatability threshold on 12 of 15 cases. PyPy's passed it on every case. Both used the worker's runtime determinism profile `v1`, and neither showed any within-case variation in MOVS or STOS bytes. For PyPy, the frozen clock overshot the two larger fill cases by 41.9% and 67.8% even though both sets of native controls stayed within the 10% spread threshold. The PyPy-specific fit overestimated all nine held-out cases. [PyPy archive and fit](v10-native/PYPY3/)

Rust's 90 native controls took 1.832–16.958 ms. Its fitted clock missed the target on branch at scales 1 and 4 and sort at scale 4. The strongest two-case lower bound for Rust was 6.84%. A Rust calibration needs larger inputs that clear the native duration floor. For the other languages, a new counter or model must distinguish the pairs below before another held-out evaluation. [Rust archive and fit](v10-native/RUST2024/)

The CPU cost column divides total CPU time across the 45 diagnostic runs by the mean of total native-before and native-after CPU time. For example, CPython consumed 730.10 seconds of diagnostic CPU against 4.34 seconds of native CPU. These runs used the diagnostic collector with the production live-work clock disabled. The ratios describe collection overhead for this workload mix. The earlier worker comparison measured live-clock execution separately. [v9/v10 worker performance](CPP20_V10_PERFORMANCE.md)

## What the native variation can explain

On C++20, the fastest native sort at scale 4 took 148.029 ms. The slowest native branch run at scale 1 took 40.529 ms. Across all pairs of measured repetitions, each of the sort's three counter values was at most 1.638 times its counterpart for branch. Any fixed nonnegative weighted sum has that same upper ratio before the final rounding to nanoseconds. The favorable native ratio exceeds 3.65. Even allowing every run to use its most favorable observed native time, at least one of these cases must have a relative error of 38.08% or more.

On CPython, fill at scale 4 took at least 119.905 ms, and integer at scale 1 took at most 60.383 ms. Their three counters allow a ratio of at most 1.184. The same argument gives a minimum error of 25.33% on at least one case. On PyPy the lower bound is 20.74%, comparing integer at scale 4 with fill at scale 2. These are retrospective bounds for this corpus and model form. Distinguishing instruction costs, cache effects and other causes of the gap would require additional observations. [Bound calculation](../../scripts/durante_native_envelope.py)

## Collection and acceptance criteria

The study used private Gen2 Cloud Run instances in Seoul (`asia-northeast3`) with one vCPU, 2 GiB of memory and request concurrency one. The C++20 instance reported an Intel Xeon CPU at 3.10 GHz. CPython and PyPy shared an Intel Xeon 6985P-C instance. Rust ran on an AMD EPYC 7B13 instance. Each language comparison kept its metered and native runs on the same revision and instance. All used the v10 client, default dispatch and the virtual `x86-64-v2` CPUID profile. Each dataset records the immutable image, installed binaries, runtime, compiler flags and observed CPU model. [Dataset directories](v10-native/)

Integer and copy were the training programs. Branch, sort and fill were held out at all scales. Each program ran at scales 1, 2 and 4 with three ordered repetitions per scale. Each repetition ran the same compiled artifact and input three times, in native-before, diagnostic-metered and native-after order. This yields 45 metered runs and 90 uninstrumented native controls per complete language. The native target is the median of the six CPU observations for a case. The score retains the worst prediction error of the three actual counter vectors. Native controls must finish with accepted stdout. A complete diagnostic receipt must confirm matching stdout. Native runs had CPU and wall limits of 10 s and 30 s. Metered runs had safety limits of 90 s and 120 s. Scores use the CPU accounting from the executed programs. [Collection protocol](v10-native/protocol.json)

The frozen comparison reuses the C++ trial's existing coefficients for instructions, MOVS bytes and STOS bytes, rounding the total up once to whole nanoseconds. The official language fit learns two positive coefficients, one for instructions and one for total REP bytes. It fits the six training cases and then checks all 15 cases. For a fit to pass, every native observation must reach 20 ms, native spread must be at most 10%, work spread at most 0.1%, and each prediction error at most 20%. Spread is the range divided by the median. The frozen analysis also checks MOVS and STOS spread separately. A partial collection can contribute diagnostics for cases with three complete triplets. Only a complete language cohort can enter the official fit. [Frozen clock weights](cloud-run-cpp20-v10-performance.json) · [Reference fitter](../../internal/durante/reference/fit.go)

The original synchronous C++20 request returned its dataset. The first CPython request logged HTTP 200, but the client lost its response body. Its sample count and collector outcome remain unknown. The recovered CPython run and the PyPy run shared a separate revision with CPU always allocated. A short POST accepted each job, and subsequent GETs retrieved its completed receipt from the same instance. The receipt cache lasts for the lifetime of that instance.

The type-i base image could not resolve the Rust compiler and returned a zero-sample failure. The Rust cohort completed on the type-c image with the same worker, reference and client binaries. The four complete cohorts comprise 360 accepted native executions and 180 completed diagnostic measurements. All three temporary services, their revisions and their image packages returned HTTP 404 after removal. [Initial Rust attempt](v10-native/attempts/780e5e4b0e69b1aed73ac2bd64820e2053640671ab6aab16bca27cba70d9e31a.json) · [Cloud Run placement and cleanup](v10-native/platform.json)

## Recompute the results

From the repository root, Python 3.10 or newer can recompute case scores, compare the archived evaluations and check the platform receipts against the published summary. Each `dataset.json.gz` expands to the original collector JSON. The SHA-256 in its sibling `evaluation.json` identifies the **uncompressed** bytes.

```sh
study=docs/durante/v10-native
python3 scripts/verify_durante_v10_native.py "$study" --verify "$study/summary.json"
```

To rerun the official Go fits as well, use the Linux/amd64 reference binary pinned by the protocol. The deployed worker and reference came from Aonohako `22eb4f7`. A build from the protocol commit `7dd7c0c` produced byte-identical binaries. The Durante client source is `191858a02666c1406b871e4974b9072b2c91bb34`. This builds the reference with Go 1.26.11 and the original flags in a fresh temporary directory. The verifier checks its SHA-256 before replay. [Durante build inputs](../../third_party/durante-v10/UPSTREAM.json)

```sh
build_dir=$(mktemp -d)
git archive 7dd7c0c107ff6ad2ad1354a2e19eafac17be3543 | tar -x -C "$build_dir"
(
  cd "$build_dir" || exit 1
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 GOTOOLCHAIN=go1.26.11 \
    go build -p 2 -buildvcs=false -tags=durante_v10 -trimpath -ldflags='-s -w -buildid=' \
      -o durante-reference ./cmd/durante-reference
)
python3 scripts/verify_durante_v10_native.py docs/durante/v10-native \
  --reference "$build_dir/durante-reference" --verify docs/durante/v10-native/summary.json
```

To repeat collection inside a matching private worker image, run the pinned collector as root with that image's Cloud Run execution configuration and v10 installation. Choose the archived language profile and a fresh output directory. A new instance can give different native observations. The archived JSON preserves the inputs for replaying this study's measurements.

```sh
durante-reference collect --language PYTHON3 --repetitions 3 \
  --metered-cpu-ms 90000 --metered-wall-ms 120000 --timeout 50m \
  --output /tmp/durante-python3-native
```
