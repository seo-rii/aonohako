# Native and shadow verdicts on three contest problems

The public v13 shadow agreed with the native verdict in 49 of 72 paired HTTP requests. It accepted 18 submissions for which native CPU crossed the deadline and timed out five native accepts. Of the 32 native accepts, six shadow time estimates were within ±20% of the completed native CPU time. These are repeated observations of three problems on one local host.

| Problem | Native AC, shadow AC | Native AC, shadow TLE | Native TLE, shadow AC | Native TLE, shadow TLE | Native accepts within ±20% | Native TLEs provably underestimated by >20% |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Longest common subsequence | 1 | 0 | 11 | 12 | 0 / 1 | 11 / 23 |
| Maximum subarray | 11 | 3 | 4 | 6 | 2 / 14 | 1 / 10 |
| Weighted path independent set | 15 | 2 | 3 | 4 | 4 / 17 | 3 / 7 |

Native TLE censors completion CPU time. For 15 of the 40 native CPU timeouts, the verified completed shadow estimate was at most 80% of a deadline strictly crossed by this native execution. Its underestimate therefore exceeds 20%. The other 25 do not establish a bound of that size. All 72 shadow executions completed and checked the expected output. All 108 requests used unsampled native CPU time.

## Reference limits

Each problem had two inputs, one reference solver and two alternative solvers. Three plain native reference requests preceded the 24 shadow comparisons and three followed them **for each input**. With a 500 ms limit, all 36 references completed. Each alternative ran three times at 1× and 2× the median of the preceding reference CPU times, rounded up once to whole milliseconds after multiplication. Later references measure changes during collection. They do not set the limit. Each shadow request carried inline input, the expected stdout and an explicit diagnostic request. The controller returned the native verdict at the top level.

| Problem / input | Before median (ms) | After median (ms) | 1× / 2× limits (ms) |
| --- | ---: | ---: | ---: |
| LCS wide | 6.366 | 6.674 | 7 / 13 |
| LCS tall | 5.319 | 6.224 | 6 / 11 |
| Maximum subarray short | 1.706 | 1.846 | 2 / 4 |
| Maximum subarray long | 16.084 | 16.287 | 17 / 33 |
| Weighted path short | 3.704 | 3.470 | 4 / 8 |
| Weighted path long | 16.054 | 16.047 | 17 / 33 |

The programs, ELF and input identities, and expected answers came from three pinned Durante studies. Each collection checked the sealed workload archive and the submitted ELF, inline input and expected output against its registration before sending requests. The HTTP responses record native and shadow work for those requests. [LCS source and registration (#135)](https://github.com/seo-rii/durante/tree/b96099de59df9561a6263ecab4a3d7ae8d5545ae/experiments/capacity_misses/lcs_layout) · [Maximum subarray (#137)](https://github.com/seo-rii/durante/tree/7716848a5dc3e6f81c100aa71d39b23ce14e6dc6/experiments/capacity_misses/maxsub_floor) · [Weighted path (#140)](https://github.com/seo-rii/durante/tree/514c32af204575cd1206017fae4df548c896839e/experiments/capacity_misses/path_mwis)

The local controller and sealed v13 worker ran on CPU 5 with an empty case registry. The original local container was unavailable. The replacement Debian container used the host's read-only userland and the same hashed worker, operator configuration and controller binary. The plans record the Debian image identity, and each completion record identifies the runtime and controller. The paired receipts carry the current weighted instruction/REP clock. The upstream studies measure separate native Milan guests and evaluate experimental relative clocks. This local result supports keeping the native verdict authoritative. [Sealed runtime registration](../gce-v13-judge-transfer/registration.json)

## Replay

Run from a checkout with Python 3.10+ and the project's Go toolchain.

```sh
python3 -B docs/durante/local-v13-contest-shadows/replay.py --verify
```

The archive includes the original HTTP response lines, collection plans, completion records, upstream manifests and registrations, and inline inputs. Replay verifies their hashes and checks the full schedule, all six derived pairs of deadlines, each native verdict source and each v13 estimate reconstructed from the measured counters. It compares all 72 shadow pairs with the production summary command built using the v13 tag. The checked-in results preserve the three repetitions at each limit as outcome counts, plus every reference CPU reading. [Evidence](evidence.tar.gz) · [Exact results](results.json) · [Replay](replay.py)
