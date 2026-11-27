# C++20 clock transfer to Aonohako v12 on AMD Milan

A live-compatible clock calibrated on 12 short contest inputs put **4 of 6 longer inputs** within ±20% of ordinary Aonohako CPU time, up from **1 of 6** with the historical GCE weights. All four successes were larger inputs for Wall and Money for Nothing, both present in calibration. On the newly tested Scrivener solution, the candidate underestimated CPU time by 47–56%. The results support a scale improvement for two C++ solutions and leave cross-algorithm grading unqualified. [Long-input results](scale/results.json)

## Short-input transfer

The historical GCE instruction/MOVS/STOS model overestimated all 12 IOI and ICPC inputs on the first guest. The median absolute error was 78.6% with that model and 71.4% with a combined-REP model trained on all 21 archived v11 cases. All 12 upper limits still had a false timeout under both models. The table counts inputs for which **both** v12 counter readings predicted the median of seven ordinary Aonohako CPU readings within ±20%. Each error summary uses the worse of the two readings per input. [Frozen schedule and coefficients](plan.json) · [Case-level predictions](results.json)

| Model frozen before the first run | Integer/copy, 6 inputs | Other historical programs, 15 inputs | Contest inputs, 12 inputs | Median absolute contest error |
|---|---:|---:|---:|---:|
| Historical v7 split REP clock | 3/6 | 0/15 | 0/12 | 78.6% |
| Historical v11 split REP fit | 5/6 | 0/15 | 0/12 | 112.2% |
| Historical v11 latency + nominal operand bytes | 4/6 | 1/15 | 0/12 | 3,207.2% |
| Combined instruction + REP clock trained on all 21 v11 inputs | 0/6 | 3/15 | 0/12 | 71.4% |

The first two columns contain workloads used to fit the broader candidate. Its transfer test is the contest column. The split v11 fit has zero STOS weight, and the latency/operand-byte model uses terminal diagnostics. The v7 policy was bound to its original worker image. All coefficients here are applied offline to v12 receipts. [v7 policy](../gce-cpp20-v7-qualification.json) · [v11 fit](../gce-v11-cpp20-accuracy/README.md)

| C++ solution | Native median range | v7 estimate / native median |
|---|---:|---:|
| IOI Rice Hub | 4.38–5.22 ms | 1.78–1.83 |
| IOI Wall | 10.96–13.60 ms | 1.80–1.93 |
| ICPC World Finals Money for Nothing | 6.39–7.16 ms | 1.35–1.41 |
| ICPC World Finals Catch the Plane | 6.32–7.53 ms | 1.62–1.79 |

The raw live counters matched exactly across repetitions on 11 of 12 contest inputs. The remaining pair differed by 22 instructions and agreed on both REP components. Native spread was within 10% on seven inputs, but all native medians were below the planned 20 ms floor. An independent longer-input run follows below. [Original HTTP receipts](receipts.jsonl.gz) · [Kernel exec events](exec-trace.txt.gz)

## Longer inputs on a separate guest

After finding every short contest case below the 20 ms native floor, we registered all six longer inputs from Durante's scale corpus before measuring them with v12. We kept the four existing models and added one candidate, computed from the **first guest's 12 short contest cases**. A single factor of 0.610871 multiplies both coefficients of the broader v11 model. After rounding, the candidate predicts nanoseconds as `ceil((122900846 × instructions + 9783784 × rep_bytes) / 1000000000)`. The fit uses the mean of both live readings per short case and that case's seven-run native median. The six scale inputs supply no training labels. [Second registration](scale/plan.json) · [Exact calibration and inputs](scale/prepare.py) · [Upstream scale corpus](https://github.com/seo-rii/durante/tree/b43685c76b94a365886031359b032899b009cd1b/experiments/contest_scale)

| Live clock | Within ±20% | Median absolute error | Worst absolute error | Implied false TLE | Implied false pass |
|---|---:|---:|---:|---:|---:|
| Historical v7 split REP | 1/6 | 85.4% | 103.5% | 4 | 1 |
| Combined REP trained on the 21 v11 cases | 1/6 | 77.9% | 95.3% | 4 | 1 |
| Combined REP recalibrated on short contest cases | 4/6 | 18.8% | 56.0% | 0 | 2 |

| Long case | Native median | Native spread | Signed error with short-contest calibration |
|---|---:|---:|---:|
| Wall – phase envelopes | 152.78 ms | 3.23% | +19.30% |
| Wall – scatter repairs | 142.04 ms | 4.65% | +18.32% |
| Money for Nothing – terraced bids | 139.55 ms | 1.24% | +9.88% |
| Money for Nothing – warehouse lots | 137.91 ms | 0.91% | +7.44% |
| Scrivener – bookmarked chapters | 130.23 ms | 19.99% | −47.03% |
| Scrivener – interwoven pages | 191.61 ms | 9.93% | −56.01% |

The seven native readings agreed at all 12 whole-millisecond boundaries. The calibrated candidate agreed at 10. Both misses would let an over-budget Scrivener run pass. Five of six cases met the native quality thresholds, including the 56.01% underestimate. The observed difference therefore persists on a stable reference. The strongest registered pair by itself gives a 14.44% lower bound for the worst relative error of any nonnegative instruction/MOVS/STOS weighting on this panel. The bound is below the 20% qualification threshold. [All five models, both readings and native controls](scale/results.json) · [Original long-input receipts](scale/receipts.jsonl.gz)

The second guest executed all 72 requests on guest CPU 1 with zero recorded steal ticks. It used the same server, patched runtime and static Wall/Money binaries as the first guest. The long panel used a 1 MiB stdout capture on all three worker paths so the verifier could hash Scrivener's 960,000-byte answer. The largest Money stdin was 12,800,014 bytes. Both temporary VMs and their auto-delete boot disks returned HTTP 404 after collection. [Scale payload hashes](scale/inputs.sha256) · [Machine and placement](scale/platform.json) · [Exec events](scale/exec-trace.txt.gz) · [Deletion record](scale/cleanup.json)

## Accuracy limits of the measured counters

A retrospective check of the six long inputs rules out ±20% accuracy for both fixed, zero-intercept models below. The lower bounds allow every input to take its most favorable observed native time and allow the final estimate to round up to a whole nanosecond. They apply to these static C++ executables on the measured Milan guest. [Exact fractions](scale/results.json) · [Replay](score.py)

| Clock features | Lower bound on the worst relative error | Inputs used in the proof |
|---|---:|---|
| Instructions, MOVS bytes and STOS bytes | 23.14% | Money – terraced bids, Scrivener – interwoven pages, Wall – phase envelopes |
| Static latency and nominal memory operand bytes, with nonnegative weights | 42.87% | Scrivener – interwoven pages, Wall – phase envelopes |

For the live clock, the Money counter vector equals `p × Scrivener + q × Wall` in all three components, with `p = 139068037507/156810905382` and `q = 67608818548/156810905382`. The same identity must hold for any unrounded weighted sum of those counters. Even using the fastest Scrivener and Wall readings, their weighted native time is 225.402 ms. The slowest Money reading is 140.671 ms. If all three estimates have relative error at most `e`, they must satisfy `(1+e) × 140671000 ≥ (1−e) × (p × 181155000 + q × 150166000) − (p+q)` in nanoseconds. Against the six native medians, the exact best possible worst error of the unrounded model is 25.6444%. The replay verifies coefficients attaining it on all twelve live readings. [Live and native receipts](scale/receipts.jsonl.gz)

For the diagnostic clock, Scrivener used 46.18% of Wall's modeled latency and 46.70% of its nominal operand bytes. Any nonnegative weighted sum puts Scrivener at most 46.70% of Wall's estimate before rounding. Yet the fastest Scrivener reading was 181.155 ms and the slowest Wall reading was 155.101 ms. The resulting error bound is independent of the fitted coefficients. These receipts contain no measurements of cache misses or branch mispredictions that would distinguish their contributions. [Diagnostic receipts](scale/receipts.jsonl.gz)

## Measurement and replay

Each panel ran on a separate N2D standard-4 Compute Engine guest in Seoul on AMD Milan with one thread per core. A systemd cpuset and scheduler affinity confine the three embedded HTTP workers and their children to guest CPU 1. The kernel's exec trace locates each ordinary submission or Durante launcher on that CPU. Native, live and diagnostic requests run serially with the same static ELF, stdin, expected stdout and sandbox backend. Native programs use ordinary host dispatch and metered programs use Durante's fixed x86-64-v2 profile. [Guest setup](guest.sh) · [Worker requests and validation](guest.py)

Each case gets one native warmup, seven native runs, two raw live-clock runs with a nonbinding budget and two runs with static latency and nominal operand-byte counters. Native normalization and short-case resampling are disabled. The reference is the worker's median `cpu_time_ns`. On the first guest all 231 post-warmup native receipts selected final user plus system CPU after subtracting the sandbox baseline. All 396 outputs matched the frozen SHA-256. Guest CPU 1 accumulated zero steal ticks during collection. [Machine and placement](platform.json) · [Payload hashes](inputs.sha256) · [VM and boot disk deletion](cleanup.json)

An estimate exceeds a limit only when its integer nanoseconds are strictly greater. The low limit rounds 80% of the native median down to whole milliseconds. The high limit rounds 120% up. Each direction is counted once per case and limit. Either predicted repetition disagreeing with unanimous native outcomes counts as a miss. The results identify limits with mixed native observations separately. These comparisons estimate what the weighting formula would decide. The guest ran every workload to completion.

From the Aonohako root, replay the archived receipts with the Python standard library.

```sh
study=docs/durante/gce-v12-clock-transfer
python3 -B "$study/score.py"
python3 -B "$study/score.py" --root "$study/scale"
```

To re-create the exact request bodies, provide a Durante checkout containing commit `b43685c`. The preparer extracts the eight archived static executables and checks the four contest executables against their registered hashes. It can use prebuilt contest ELFs through `--contest-binaries`. Rebuilding them requires the matching GCC 12.2 static toolchain. Choose a fresh output directory.

```sh
python3 -B "$study/prepare.py" --durante-repo "$DURANTE_REPO" --output "$PAYLOAD"
python3 -B "$study/score.py" --payload "$PAYLOAD"
```

The long panel uses the first panel's archived receipts to reproduce the fifth coefficient pair.

```sh
python3 -B "$study/scale/prepare.py" --durante-repo "$DURANTE_REPO" --output "$SCALE_PAYLOAD"
python3 -B "$study/score.py" --root "$study/scale" --payload "$SCALE_PAYLOAD"
```
