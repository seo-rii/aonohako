# Inversion-counting deadlines on two CPU-pinned GCE workers

The shared v13 clock stopped two Fenwick-tree submissions above their native completion times on two diagnostic inputs. This experiment tests a single inversion-specific weight against the same sealed v13 image. The original pilot's fast and wrong submissions supply the fit. Their summed native medians are 31,933,000 ns for 187,714,408 units of work. The resulting candidate weight is 3,991,625 / 23,464,301 ns per instruction or REP byte. Choosing this fitting strategy was informed by the known historical errors. [Historical trials](../gce-v13-judge-transfer/README.md) · [Calibration and prospective gates](plan.json)

Two fresh AMD Milan VMs run the native worker and both deciding clocks on CPU 1 with SMT disabled. All clocks use the registered C++20 image, raw watchdogs and memory envelope. The study includes the original contest and stress controls, reference deadlines, the two previously examined input seeds and two previously unmeasured seeds. The new seeds run two original Fenwick implementations, an iterative merge sort and a separate right-to-left Fenwick implementation. Five ample native measurements per prospective case on the first boot set 80% and 120% deadlines, rounded down and up to whole milliseconds. Both boots use those same deadlines. Kernel exec traces, measured outputs and the live work counters are recorded for each request.

The candidate must preserve the original controls, reduce historical disagreements, preserve comparable coverage and avoid new directional errors within each panel on either boot. For every new solver/input/deadline on which the shared clock unanimously agrees with native, the candidate must also agree. The original 203 ms inversion-counting case is the only potential release case; any release requires an additional ordinary routing test. The other two problems retain their previously qualified model. [Measurement schedule](guest.py) · [Additional C++ implementations](alternatives.cpp)

## Live results

Both boots produced the same comparison. Each cell below represents a solver, input and deadline on one boot, with three native repetitions and two per deciding clock. All repetitions in these panels agreed within each arm. A false timeout means the clock stopped a submission that completed natively. A false pass means the clock let it finish when native returned TLE.

| Panel, both boots | Cells | Shared agreement | Candidate agreement | Shared errors | Candidate errors |
|---|---:|---:|---:|---|---|
| Original reference boundaries | 4 | 4 | 4 | 0 | 0 |
| Two previously examined seeds | 16 | 8 | 16 | 8 false timeouts | 0 |
| Two prospective seeds | 32 | 22 | 28 | 10 false timeouts | 4 false passes |

The original AC, WA and in-flight timeouts at 203 ms and 1 ms passed on both boots. The candidate corrected all historical errors but failed the prospective gate. Iterative merge sort finished under the candidate at the 44 ms and 93 ms lower deadlines on both boots. Native medians on those inputs were 55.74–56.50 ms and 116.42–118.53 ms. Candidate medians were 43.15 ms and 83.86 ms. No candidate configuration was released. [Results and individual outcomes](results.json)

Even allowing each solver the most favorable completed counter value seen on its boot, the 93 ms merge-sort timeout needs a weight strictly above 0.188656 ns/work. Finishing the historical B wrong-answer solver within 54 ms needs at most 0.173036 ns/work. Those constraints conflict on each boot. Retuning this one coefficient cannot reproduce both decisions for the observed work counts. The experiment tests a specific C++20 runtime, two additional algorithms and two new input distributions. [Native and instrumented receipts](evidence/) · [Preregistered commit](https://github.com/seo-rii/aonohako/commit/a5a98052e36d7b0dd5e88d3e5fb2c230e51c8695)

## Replay

The 662 clock requests include 80 ample native measurements used to set or independently repeat the prospective brackets. An additional 40 native and raw memory controls ran afterward. The infrastructure ledger records distinct instance, disk and boot identities, the payload and raw archive digests, threshold transfer, and successful instance and boot-disk deletion. The saved kernel traces place every native target and measured launcher on CPU 1. The guest CPU steal counter stayed at zero during both clock panels. [Infrastructure and file hashes](evidence/infrastructure.json)

From the repository root, run the following commands with Python 3.11 or later. The optional payload check independently reconstructs the HTTP bodies from the registered executable bytes.

```sh
study=docs/durante/gce-inversion-clock
python3 -B "$study/replay.py" | diff - "$study/results.json"
python3 -B -m unittest discover -s "$study" -p 'test_*.py'
python3 -B "$study/replay.py" --payload /path/to/unpacked/registered/payload
```

To rebuild the guest payload, compile both variants of `alternatives.cpp` with the original corpus flags (`-std=c++20 -O2 -static -s -march=x86-64-v2 -Wl,--build-id=none`). Use `-DSOLUTION=1` for `pilot/corpus/inversions-bottom_up` and `-DSOLUTION=2` for `pilot/corpus/inversions-reverse_bit`. Supply the original unpacked v13 transfer payload, the new solver directory and the `table.cpp` workload compiled with `c++ -O2 -std=c++20 -static` to `prepare.py`. Compare the returned registration with the committed file before running either guest. [Archive builder](prepare.py) · [Registration](registration.json)
