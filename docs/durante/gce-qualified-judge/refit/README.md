# Live requalification of a reference-only clock fit

Three calibration references on a previous Milan guest determined a new
coefficient for the existing instruction-plus-REP work clock. It had
lower average and worst error on twelve separate input/solver pairs in a
completed-counter replay. This experiment compares real deciding clocks
and tests whether the new coefficient meets the release criteria for the
three registered pilot cases. [Reference fits and registered inputs](https://github.com/seo-rii/durante/pull/109)

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
