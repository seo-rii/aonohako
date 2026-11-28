# Live requalification of a reference-only clock fit

Three calibration references on a previous Milan guest determined a new
coefficient for the existing instruction-plus-REP work clock. It had
lower average and worst error on twelve separate input/solver pairs in a
completed-counter replay. This experiment compares real deciding clocks
and tests whether the new coefficient meets the release criteria for the
three registered pilot cases. [Reference fits and registered inputs](https://github.com/seo-rii/durante/pull/109)

## Verified release

The refitted work clock passed all seven registered criteria on 516 real
HTTP requests. The operator configuration selects this coefficient only for
range minimum at 183 ms, shortest paths at 65 ms and inversion counting
at 203 ms. The raw CPU and wall watchdogs remain 120 s and 170 s.
A separate test sent twelve ordinary requests through the auto-enabled
controller to the original qualified worker using the exported environment.
All nine matched submissions produced the expected AC, WA or in-flight
TLE. The three unregistered controls used native judging.
[Selected model and scope](results.json) · [Two-hop HTTP receipts](evidence/automatic-routing.json.gz)

| Actual deadline checks | Previous clock | Selected clock |
|---|---:|---:|
| Agreement on 48 solver/input/bracket/guest groups | 36 | 40 |
| False timeouts on those 48 groups | 12 | 8 |
| False passes on those 48 groups | 0 | 0 |
| Mixed native or work-clock groups | 0 | 0 |
| Agreement on 12 original reference-bracket checks | 8 | 10 |

The remaining eight false timeouts were the two inversions implementations
at their high bounds on both new inputs and both guests. The original range
minimum reference at 36 ms still passes the virtual limit while native
execution times out on both guests. These diagnostic limits are excluded
from the operator registry. On the original completed held-out contest
groups the new guest medians had a signed error of +2.98% and a worst
absolute error of 57.28%. These experiments cover one fixed architecture,
workload corpus and runtime image.

Run the following from the repository root to regenerate the report and
the environment for the verified worker. Automatic routing additionally
requires an updated v12 controller configured from
`python3 -B docs/durante/gce-qualified-judge/controller_env.py --engine v12`.
Set `AONOHAKO_DURANTE_JUDGE_AUTO=on` on that controller. Keep automatic
routing off on the sealed schema-1 worker. [Controller setup](../../../durante-judging.md)
The default deployment uses native grading.

```sh
study=docs/durante/gce-qualified-judge/refit
python3 -B "$study/verify.py" --infrastructure "$study/evidence/infrastructure.json"
python3 -B docs/durante/gce-qualified-judge/operator_env.py
```

The two VMs used different boot disks, had SMT disabled and executed
contestant or meter launchers on CPU 1. Both instances and both disks
returned HTTP 404 after collection. [Infrastructure and checksums](evidence/infrastructure.json)

## Registered promotion criteria

Two fresh CPU-1-pinned Milan guests receive the same sealed old and
candidate models. On each boot, 90 requests reproduce the original contest,
1 ms and reference-bracket panels using native grading and the candidate.
Another 168 requests compare native, old-clock and candidate-clock verdicts
on the twelve previously measured solver/input pairs. For each pair, two
budgets bracket the independently observed five-run native median at
80% and 120%, rounded down and up respectively. These are additional
decision controls for known programs and inputs. The new guest verdicts
are collected after both clocks and all deadlines have been fixed.

Every original contest case must pass its previously published fast,
wrong, slow and 1 ms checks on both boots. Reference-bracket unanimous
oppositions must not increase. Across the 48 matched solver/bracket/guest
controls, the candidate must reduce total unanimous opposition with no
increase in either false timeouts or false passes. Report mixed controls
separately and require comparable coverage at least as high as the old
clock. Only the three original contest deadlines are eligible for
promotion. If the criteria fail, emit the verified baseline configuration.
