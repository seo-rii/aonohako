# Case-scoped judging on held-out C++ submissions

This experiment tests actual Aonohako HTTP grading using a Durante deadline.
It compares ordinary scheduled CPU time and Durante verdicts for independently
implemented submissions to three deterministic algorithm problems. Each
request supplies a static C++20 executable, an input specifying a problem
instance, and the organizer's independently computed expected output.

The operator environment now uses the model selected by a second comparison on
516 real requests at fixed deadlines. [Live clock requalification](refit/README.md)

## Registered experiment

One disposable AMD Milan guest measures only the three reference solutions.
On each problem it records five ordinary CPU times and three completed raw
work counts. Divide median native nanoseconds by median instructions plus REP
bytes (sum the counters within each run first). The median of the three
ratios gives a single rational coefficient shared by every problem and both
counters. For each problem the contest budget in whole milliseconds is four
times the native reference median, rounded up, with a 10 ms minimum.
Calibration also binds the exact runtime options, image and resource policy.

Two fresh guests then use the identical frozen configuration. Each runs the
held-out fast, slow and incorrect submissions three times under ordinary
judging and twice under the Durante judge at the contest budget. This gives
an expected AC, CPU/virtual TLE, and WA for three different submissions per
problem. A second 1 ms budget tests whether the same held-out fast program
changes verdict from AC to TLE. Reference budgets at 80% and 120% of native calibration
time measure behavior closer to a decision boundary. These references are
calibration programs.

A contest case qualifies for this pilot only if all its native and virtual
contest verdicts match the registered labels on both validation guests,
the same fast ELF times out at 1 ms, and each guest records a live clock
cutoff on the slow submission. A raw watchdog stop cannot count as a virtual
timeout. AC and WA require a completed, model-bound clock and the ordinary
output comparison. Report the reference bracket separately including every
disagreement. Passing a wide-margin contest case licenses only that tested
input, runtime, model and contest deadline. The tight budgets are study
controls. Published receipts allow independent analysis of CPU spreads and
virtual cutoffs; the work counter retains its application-mutable contract.

All three guests use a single pinned logical CPU, SMT disabled, and separate
boot disks. Kernel exec traces verify contestant or instrumented launcher
placement per request. The server, reference tool and Durante installation
are mounted from the content-addressed read-only SquashFS identified in the
plan. The GCE base OS is separately identified.

## Reproducing the inputs and registration

The three problems use range minimum queries, positive-weight shortest paths
and inversion counting. An input specifies the instance size and PRNG seed.
The Python oracle independently computes the ordinary decimal output. Run
`python3 -B -m unittest discover -s docs/durante/gce-qualified-judge -p test_corpus.py`
to compare all twelve compiled solutions against several small instances.

From this repository, run the following with a clean checkout of the server
commit and its installed v12 runtime. The output directory must be new. The
registration records the toolchain, ELF, stdin, stdout, installed-file,
SquashFS and archive digests. The server and reference tool use the
`durante_v12` build tag. Preserve the calibration output before either
validation guest can receive a held-out submission.

```sh
python3 -B docs/durante/gce-qualified-judge/prepare.py \
  --server-source "$SERVER_CHECKOUT" --durante-install "$DURANTE_INSTALL" \
  --output "$STUDY/payload" --registration "$STUDY/registration.json"
```

The archive contains the complete guest runner. On the calibration guest,
run `sudo ./guest.sh calibrate` from the extracted directory. Run
`sudo ./guest.sh primary /path/to/frozen.json` and
`sudo ./guest.sh repeat /path/to/frozen.json` on two new guests with the
same archive and a byte-identical calibration file. Each writes an
`output` directory containing the request-level receipts and kernel traces.

## Measured contest verdicts

All three registered contest cases passed on both validation boots. The
following table covers 90 contest runs. Each verdict cell reports the outcome
of six native runs and four Durante runs with the same held-out executable,
input and deadline. AC means accepted, WA means wrong answer, and TLE means
time limit exceeded. [Machine-readable verdicts](results.json)

| Problem | Reference native median | Reference median work units | Contest deadline | Fast correct | Incorrect | Slow correct |
| --- | ---: | ---: | ---: | --- | --- | --- |
| Range minimum | 45.620 ms | 131,010,752 | 183 ms | AC | WA | TLE |
| Shortest paths | 16.156 ms | 63,315,373 | 65 ms | AC | WA | TLE |
| Inversions | 50.696 ms | 244,161,820 | 203 ms | AC | WA | TLE |

Every Durante contest TLE stopped the slow submission while it was running.
At the separate 1 ms stress deadline, the very same fast executables that
earned contest AC received TLE on all 18 native runs and all 12 Durante
runs. All 12 Durante stress stops also came from the live work clock. The
outer safety limits were 120 seconds of CPU, 170 seconds of wall time and
1,024 MiB of memory.

## Accuracy near a deadline

The reference solutions ran at two additional deadlines on both validation
boots. Each cell shows the verdict shared by all six native runs and the
verdict shared by all four Durante runs, in that order.

| Reference | Lower deadline | Native / Durante | Upper deadline | Native / Durante |
| --- | ---: | --- | ---: | --- |
| Range minimum | 36 ms | **TLE / AC** | 55 ms | AC / AC |
| Shortest paths | 12 ms | TLE / TLE | 20 ms | AC / AC |
| Inversions | 40 ms | TLE / TLE | 61 ms | **AC / TLE** |

At 36 ms, Durante estimated 33.430 ms for range minimum and allowed it to
finish. With the 55 ms budget the same reference took 45.512 and 45.761 ms
of median native CPU time on the two boots. At 61 ms, inversions finished
natively in 50.826 and 50.780 ms (boot medians) while Durante stopped it
during execution on all four runs. The frozen coefficient values the
calibration reference's median inversion work at 62.302 ms. These two
deadlines account for all four disagreements among the 12 problem, budget
and boot combinations. [Primary trial journal](evidence/primary/trials.jsonl.gz)
and [repeat trial journal](evidence/repeat/trials.jsonl.gz)

For the six held-out executables that completed at the contest deadline
(fast and incorrect for each problem), compare the two Durante runs' median
virtual time with the three native runs' median CPU time on each boot.
Across the 12 comparisons, the median signed error was **+7.48%**, with
individual errors ranging from −8.66% to +67.72%. The
largest absolute error was **+67.72%** for the incorrect inversion solution
on the primary boot (23.847 ms virtual, 14.219 ms native). A positive value
means the virtual estimate was higher. Runs stopped at a deadline do not
supply a completion time. [Full timing summaries](results.json)

Across the six completed held-out executables, the fast shortest-path
solver required more native CPU time per work unit than the incorrect
inversion solver, even using the most favorable observed CPU and work
extremes for each. The resulting lower bound on the worst-case relative
error of any shared coefficient was **24.85%** on the primary boot and
**25.00%** on the repeat boot (rounded). A separate post-hoc coefficient
from each problem's single reference raised mean absolute error on the
six primary-boot solvers from 21.20% to 24.81%, and across both boots from
20.52% to 24.15%. [Scalar timing analysis](scalar_bounds.json)

For cooperative practice judging, use only `judge_cases` from the verified
results. Its three entries bind a problem ID, CPP20, the exact stdin digest,
the tested contest deadline and the model binding. The organizer supplies
the expected output. The calibrated registry used on the study guests also
contains the 1 ms and reference-bracket controls. General timing accuracy
across other inputs, deadlines, languages and contestant programs remains
unqualified. Run `python3 -B docs/durante/gce-qualified-judge/operator_env.py`
to replay the evidence and emit the scoped operator environment for the
exact GCE worker runtime. Applying that environment is an explicit deployment
step. Cloud Run continues to use its existing configuration and deployments
use their existing defaults. [Qualified contest registry](results.json)

## Machine, freeze and independent replay

On 2026-11-27, the three independent `n2d-standard-4` guests ran on AMD
Milan in `asia-northeast3-a` using `debian-12-bookworm-v20261117`.
The worker and submissions were pinned to logical CPU 1 with one thread per
core. Each boot had its own VM and disk. GCE returned 404 for each of the
three instances and boot disks after cleanup. [Infrastructure receipts](evidence/infrastructure.json)

The calibration guest collected 15 native reference times and nine complete
raw work reports. The frozen clock charges **16,156,000 / 63,315,373 ns**
per counted instruction or MOVS/STOS byte. Each validation guest took 90
measurements with that exact coefficient. The immutable worker came from
Aonohako commit `840f8f77312d03327376d5c437c40de13ed4867c` with the
v12 DynamoRIO client, default dispatch and the x86-64-v2 CPUID profile.
The payload archive SHA-256 is
`43418db3906af41666c1d4674e9b64ceebb3c72cbc3cc493e4e52af6a89ddc00`.
The runtime SquashFS SHA-256 is
`170190aab6b32aac533ee8d34a6f0a348b06158a0f88ec337457bdf68d66044c`.
Both validation guests loaded a frozen file with SHA-256
`5cdbf4661f0c547c20a5fd7739729f58ff7b37e112d36c386f66072ce19949cb`.
[Payload registration](registration.json) and [frozen model](evidence/calibrate/frozen.json)

From the repository root, the following command replays all 204 receipts,
checks the CPU placement traces and GCE identities, reconstructs the HTTP
requests from the prepared payload, and compares the generated report with
the published one. Omitting `--payload` allows replay from the committed
evidence alone and skips reconstruction of the request bytes.

```sh
pilot=docs/durante/gce-qualified-judge
python3 -B "$pilot/verify.py" \
  --registration "$pilot/registration.json" \
  --calibration "$pilot/evidence/calibrate" \
  --validation "$pilot/evidence/primary" \
  --validation "$pilot/evidence/repeat" \
  --infrastructure "$pilot/evidence/infrastructure.json" \
  --payload "$STUDY/payload" \
  --output "$STUDY/replayed-results.json"
cmp "$STUDY/replayed-results.json" "$pilot/results.json"
```
