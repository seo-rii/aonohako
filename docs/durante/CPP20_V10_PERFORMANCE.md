# C++20 v10 worker performance on Cloud Run

Using the v10 client reduced the CPU cost of metering a short C++ iostream submission by 4.7% across 12 balanced blocks on one Seoul Cloud Run instance. A second program that reads two integers and computes the intersection of two sorted sequences took 6.6% less CPU with 256 elements. Its empty-input-size case showed essentially no change. V10 retains v9's inline thresholds and uses one thread-local load in the generated block and write accounting code. [Client source and pinned build inputs](../../third_party/durante-v10/UPSTREAM.json)

## Measurements

Each submission started a fresh process on one warm, private Gen2 instance with one vCPU and 2 GiB of memory. The two actual workers used the same patched DynamoRIO core, x86-64-v2 CPUID profile, runtime options, ordinary clock weights and input bytes. The three sorted-intersection cases used the same executable, with source, inputs and expected outputs fixed before candidate selection.

The CPU columns are medians of individual executions in milliseconds, including metering. Each block ran both versions twice in consecutive ABBA or BAAB order. Its change is the geometric mean v10/v9 CPU ratio, converted to a percentage. The change column is the median across blocks. The last column is a distribution-free interval for the median under an independent-block assumption, with at least 95% coverage. Four blocks cannot yield that interval. The receipt includes all timings, run order, counter ranges, arm medians and individual block effects. [Cloud Run measurements](cloud-run-cpp20-v10-performance.json)

| Program | v9 CPU (ms) | v10 CPU (ms) | Block change | Faster blocks | Block interval |
|---|---:|---:|---:|---:|---:|
| Short iostream read/print | 131.43 | 123.03 | −4.7% | 12/12 | −10.2% to −2.3% |
| Sorted intersection, zero elements | 138.17 | 133.97 | −0.02% | 6/12 | −8.9% to +4.3% |
| Sorted intersection, 256 elements | 135.42 | 131.59 | −6.6% | 11/12 | −7.8% to −1.4% |
| Monotonic-stack histogram | 278.58 | 244.49 | −10.0% | 8/8 | −15.6% to −1.3% |
| Sorted intersection, 262,144 elements | 144.67 | 139.06 | −5.0% | 7/8 | −13.5% to +0.5% |
| Distinct subarrays | 151.41 | 140.79 | −6.7% | 3/4 | — |
| Sparse-table range minimum | 196.07 | 175.00 | −9.3% | 4/4 | — |
| Integer control | 291.87 | 242.92 | −16.4% | 4/4 | — |
| Branch control | 736.14 | 591.86 | −19.5% | 4/4 | — |
| REP copy control | 538.53 | 510.38 | −5.3% | 3/4 | — |
| Short libc startup | 58.82 | 61.37 | +2.2% | 3/8 | −7.0% to +10.7% |
| Eight-instruction startup | 9.65 | 9.54 | −3.8% | 6/8 | −30.4% to +21.2% |

## Clock and cleanup

All 352 full submissions returned the expected stdout and exit code zero. Each live clock reconciled with its terminal counters. Typed MOVS and STOS bytes were identical across all full runs for each input. Fresh processes varied by up to 149 instructions on the REP copy control. Its modeled time varied by 35 ns. For the histogram the modeled time ranged from 51,379,471 to 51,379,478 ns across both versions as the CPU cost of collecting it fell. This comparison measures collection cost. Native CPU time needs a separate calibration.

All 12 low-budget requests returned TLE from the Durante clock. The integer budget was 63 ms and the REP copy budget was 142 ms. Their CPU and wall safety limits were 90 s and 120 s. Cloud Logging attributed all 364 requests to one instance. The temporary service, revision and image package each returned HTTP 404 after deletion. [Individual budgets, progress and cleanup records](cloud-run-cpp20-v10-performance.json)

The production-link build reproduced the archived v9 DSO byte for byte. The v10 DSO is pinned at `dfa6bcd57398cda092ea6510d033d738d9104883c0d030a69f9041e661aefcf0`. Recompute the comparisons, clock crossings and placement checks with the Python standard library.

```sh
python3 scripts/verify_durante_v10_performance.py docs/durante/cloud-run-cpp20-v10-performance.json
```
