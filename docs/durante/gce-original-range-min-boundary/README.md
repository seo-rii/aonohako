# Challenge the original 183 ms range-minimum judge

Nine correct C++20 block-decomposition submissions exercised the original seeded
input on the sealed v12 and v13 workers. Three variants timed out under each
Durante clock on both fresh boots. All three passed every corresponding native
deadline. The preregistered rule disqualifies this problem/input/limit on both
engines.

| Submission | Native CPU, ample-run median across each of four boots | Durante estimate, ample-run median | Native / deciding-clock verdict at 183 ms |
| --- | ---: | ---: | --- |
| Precomputed block edges, size 4 | 161.6–162.5 ms | 382.1 ms | 12 AC / 8 TLE |
| Scan edges, size 256 | 90.1–90.5 ms | 191.4 ms | 12 AC / 8 TLE |
| Scan edges, size 512 | 157.6–158.2 ms | 359.4 ms | 12 AC / 8 TLE |

Each engine received 105 real HTTP submissions per boot, 420 in total. The
historical fast, wrong-answer and slow controls reproduced AC, WA and live TLE.
Every ample variant returned the independently calculated sum, 368028705. Each
boot pinned the guest workload to CPU 1 on a fresh Debian 12 N2D Milan VM with
SMT disabled. CPU steal was zero in all four scheduling windows. The evidence
includes exec traces, full receipts and VM/disk deletion verification.

## Clock accuracy across problems

An additional retrospective comparison tests a uniform multiplier on the
existing clock's *completed* time estimates. For each submission we allow the
most favorable pairing of any of six native and four Durante observations,
including across boots. At ±20% the possible intervals are:

| Engine | RMQ cached edges, size 64 | Inversion hybrid, cutoff 1024 |
| --- | ---: | ---: |
| v12 | 0.4983–0.7544 | 0.2087–0.3140 |
| v13 | 0.4917–0.7787 | 0.2074–0.3139 |

Even these intervals are disjoint. Multiplying all completed estimates by a
single constant cannot bring these two workloads within ±20% of native CPU.
The decision to remove an input uses the separate preregistered deadline
experiment above.

## Reproduction

The source, exact executables and request hashes, two schedules, effective
clock bindings and decision rule were published at
`b7a05b55be8259d8519d47eb3ede4c7913584134` before any VM started. Each
engine uses the authenticated collector and runtime from the corresponding
[original inversion study](../gce-original-inversion-boundary/). The new
block solver precomputes one minimum per block. Six variants scan the two
partial edges and the whole interior blocks. Three variants precompute each
block's prefix/suffix minima and scan the interior blocks.

From this directory:

- Run `python3 -B -m unittest discover` for the independent small-input oracle
  and archived-evidence regressions.
- Run `python3 -B replay.py v12` and `python3 -B replay.py v13` for the complete
  guest, verdict and scale-interval replay. The output for each engine matches
  its committed `results.json`.
- Run `python3 -B prepare.py ENGINE PARENT_PAYLOAD PILOT_CORPUS OUTPUT` with
  GNU g++ 12.2.0 and the corresponding original inversion and judge-pilot
  payloads. Compare the registration with `ENGINE/registration.json`. Pass
  `--payload OUTPUT/payload` to the replay to recompute every HTTP request digest.
- Each engine's compressed ordinary-routing receipt comes from twelve requests
  without measurement fields, a schema-2 controller and a sealed schema-1
  worker. The candidate controller registry retains only the original
  shortest-paths scope. Run `python3 -B smoke_routing.py ENGINE --worker-env
  WORKER_ENV --verify ENGINE/ordinary-routing.json.gz` to verify the archived
  native routes. Use `--output`, `--controller`, `--runtime` and `--payload` to
  exercise that pair again. Build the controller at `746ff27` with Go 1.26.11,
  `CGO_ENABLED=0 go build -trimpath -buildvcs=false -tags durante_ENGINE
  -o controller ./cmd/server`, replacing `ENGINE` with `v12` or `v13`.
  This candidate configuration is a routing witness. Controller settings for
  operations are maintained by the [qualified-judge exporter](../gce-qualified-judge/).
