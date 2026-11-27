# Case-scoped judging on held-out C++ submissions

This experiment tests actual Aonohako HTTP grading using a Durante deadline.
It compares ordinary scheduled CPU time and Durante verdicts for independently
implemented submissions to three deterministic algorithm problems. Each
request supplies a static C++20 executable, an input specifying a problem
instance, and the organizer's independently computed expected output.

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
