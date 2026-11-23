# C++20 v9 worker performance on Cloud Run

The opt-in v9 worker reduced the histogram submission's metered CPU median from 2,605 ms to 216 ms. A short program that read and printed an integer with C++ iostreams took 8 ms more. These measurements describe the cost of running the judge. The ordinary clock still charges instructions and REP MOVS/STOS bytes. [Primary measurements](cloud-run-cpp20-v9-performance.json)

## Workloads and tradeoffs

The table uses all five full executions per arm on one private Gen2 instance in Seoul with one vCPU and 2 GiB of memory. Each pair ran the same executable and input successively through two actual Aonohako workers. The protocol rotated order and both workers used the same patched runtime core, CPU profile and clock coefficients. Each full execution returned the expected output with exit code zero.

| Program | v8 median CPU | v9 median CPU | Change |
|---|---:|---:|---:|
| Monotonic-stack histogram | 2,605.32 ms | 216.50 ms | −91.7% |
| Count subarrays with distinct values | 108.11 ms | 110.76 ms | +2.4% |
| Sparse-table range minimum | 136.34 ms | 144.10 ms | +5.7% |
| Branch control | 552.92 ms | 555.98 ms | +0.6% |
| Integer control | 240.92 ms | 240.65 ms | −0.1% |
| REP copy control | 453.70 ms | 447.61 ms | −1.3% |
| Short C++ iostream read and print | 94.38 ms | 102.16 ms | +8.2% |
| Short libc program | 44.63 ms | 50.06 ms | +12.2% |
| Eight-instruction assembly startup | 6.37 ms | 6.79 ms | +6.5% |

After a successful stdin read on this static binary, v9 selects inline accounting for newly translated eligible blocks earlier. The monotonic stack reaches a multi-block hot loop during that window. The benchmark found no general speedup on short programs. Five pairs on a single instance also leave small differences sensitive to run-to-run variation. [Upstream implementation and local paired measurements](https://github.com/seo-rii/durante/pull/66)

## Live limits and package identity

The 90 full executions reconciled their live and terminal work. Per-case typed REP byte counts were identical. Histogram, short iostream and assembly startup each also had identical instructions across the two workers. Some other processes varied by up to 55 instructions over their ten complete runs. The source and counters in the receipt identify the exact binaries used.

The trial also submitted the integer control at 63 ms and the REP copy control at 142 ms three times per worker. All 12 returned TLE from the internal work clock. The independent CPU and wall watchdogs were 90 s and 120 s. Cloud Logging accounted for all 102 requests on one instance. The temporary service, revision and image package each returned HTTP 404 after deletion. [Limits, progress counters and placement](cloud-run-cpp20-v9-performance.json)

The production build used GCC 14.2 and the patched SDK link inputs. Using this build for the v8 source reproduced the archived client byte for byte. The production v9 client is pinned at `a3e79f03f1f1d894b1850bb874e61bd67b63c6280d9e455b19baaf8c36ae6ac4`. The v9 worker and reference utility use the source commit recorded in its manifest. [v9 source and file digests](../../third_party/durante-v9/UPSTREAM.json)

An earlier trial used the same v9 machine code with different link inputs. Its client differed in the build ID and symbol types for three imported runtime functions. That trial ran its own 102 requests on a separate instance and observed a 71.7% reduction on the histogram. Both sets of results include every signed time change, all cutoff verdicts and teardown records. The table above uses the production-linked trial. [Auxiliary trial](cloud-run-cpp20-v9-auxiliary.json)

From the repository root, verify the primary receipt with the Python standard library.

```sh
python3 scripts/verify_durante_v9_performance.py docs/durante/cloud-run-cpp20-v9-performance.json
```
