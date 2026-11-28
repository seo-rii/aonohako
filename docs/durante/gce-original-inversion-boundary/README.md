# Judging the original 203 ms inversion-counting case

**Four correct submissions were accepted by native judging and timed out under the shared v13 clock on the original 203 ms input.** Each completed on all six native contest trials across the two independent boots. Durante stopped each in flight on all four deciding-clock trials. The original fast AC, wrong WA and slow live TLE controls held on both boots. All 110 ample trials finished with their expected outputs. The result meets the registered condition for removing this case from automatic v13 work-clock judging. [Replayed verdicts and termination sources](results.json)

| Correct implementation | Native ample median, first boot | Native ample median, second boot | Clock ample estimate, second boot |
|---|---:|---:|---:|
| Insertion sort and merge, cutoff 512 | 82.884 ms | 82.651 ms | 281.932 ms |
| Insertion sort and merge, cutoff 1,024 | 141.704 ms | 141.710 ms | 544.289 ms |
| Block frequencies, block size 512 | 86.254 ms | 86.399 ms | 203.768 ms |
| Block frequencies, block size 1,024 | 155.816 ms | 155.895 ms | 395.685 ms |

Native and clock verdicts agreed for the other five new submissions on both boots. The version with frequency blocks of 2,048 exceeded 203 ms under both judges. All 24 solver/boot cells produced unanimous outcomes within each judge. The ample-run times above explain the direction of the four errors, and the separate deciding runs establish the verdicts at the real limit. [CPU-pinned guest receipts and cleanup](evidence/infrastructure.json)

## Experiment

The original C++20 inversion-counting input was eligible for the shared v13 Durante clock. This study tests whether that exact route preserves native verdicts for independent correct submissions. It uses the previously qualified shared weight, original problem ID, input, expected output, 203 ms CPU limit, 768 MiB memory limit, 60,000 ms wall limit and sealed runtime. [Existing qualification](../gce-v13-judge-transfer/README.md) · [Frozen plan](plan.json)

Nine new submissions implement two algorithms. Five maintain frequencies in value blocks of 32, 128, 512, 1,024 or 2,048 entries. Four use insertion sort on runs of 16, 128, 512 or 1,024 entries and merge the runs. Both count strictly decreasing pairs and return the same total of 603,431,314. The algorithms and parameters were registered before collecting either guest's timings. [Solvers](variants.cpp) · [Independent oracle tests](test_boundary.py) · [Pre-registration](https://github.com/seo-rii/aonohako/commit/42ece96bc41072e5d61320e066f37707231df2a8)

Each fresh AMD Milan VM runs the native worker and the same shared v13 clock on CPU 1 with SMT disabled. Each solver and deadline receives three native trials and two actual deciding-clock trials. The 30,000 ms ample limit records completion times for all variants and the original fast and wrong controls. All twelve solvers also run at the real 203 ms limit, including the original quadratic control. This gives 115 HTTP requests per boot. [Schedule](guest.py) · [Registered worker and contestant digests](registration.json)

The pre-registered rule requires the same solver to receive a unanimous false timeout or false pass in the same direction on both boots. Both original fast and wrong controls must keep their AC and WA verdicts. Both metered slow controls on each boot must stop in flight from the work clock, and every ample trial must finish with its expected output. Any mixed vote is recorded separately. [Decision rule](plan.json)

## Ordinary judging after narrowing the controller export

With automatic routing enabled, the v13 controller sent all twelve original 203 ms inversion submissions to native judging. On the separate local CPU, eight were accepted, one returned WA and three received a native CPU timeout. Range minimum and shortest paths each produced an AC, a WA and an in-flight work-clock TLE on the sealed v13 worker. This smoke test checks which worker path handled each ordinary HTTP request. The pinned GCE runs above establish the timing comparison. [Ordinary-routing receipts](evidence/ordinary-routing.json.gz) · [Routing smoke and verifier](smoke_routing.py) · [Controller export](../gce-qualified-judge/controller_env.py)

## Replay

From the repository root, run the verifier and compare its full output with the published result.

```sh
study=docs/durante/gce-original-inversion-boundary
python3 -B "$study/replay.py" | diff - "$study/results.json"
python3 -B -m unittest discover -s "$study" -p 'test_*.py'
```

The optional `--payload` argument independently reconstructs the 46 distinct HTTP request bodies from the authenticated contestant binaries. Every replay validates the 230 receipts, CPU-1 kernel trace windows, effective clock bindings and budgets, native CPU accounting, oracle outputs, archived hashes and deletion of both instances and boot disks. [Replay implementation](replay.py)
