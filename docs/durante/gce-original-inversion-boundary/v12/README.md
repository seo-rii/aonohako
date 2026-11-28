# The v12 clock at the original 203 ms inversion deadline

At the original 203 ms deadline both implementations finished all twelve native attempts with the correct answer. The v12 clock returned TLE on all eight deciding attempts. Both independent boots reproduced the original fast AC, wrong WA and live slow TLE. The registered rule disqualifies automatic v12 clock routing for this input at 203 ms. [Replayed verdicts](results.json)

| Submission | Native median at 30 s, primary | Native median at 30 s, repeat | Clock estimate at 30 s, both boots |
| --- | ---: | ---: | ---: |
| Merge sort with insertion cutoff 1024 | 142.150 ms | 142.394 ms | 544.289 ms |
| Block decomposition with 1024 elements per block | 155.828 ms | 155.875 ms | 395.685 ms |

This follow-up uses two C++ implementations that incurred false timeouts on the v13 worker. Both run on the original input, for which the registered answer is `603431314`. The v12 engine has its own image, installation and clock binding. The native worker and that clock each see the same submitted executables, input, expected output and advertised limits. [Original v13 study](../README.md) · [Frozen v12 requests](registration.json)

Each fresh AMD Milan boot runs 21 native and 14 deciding-clock requests on CPU 1 with SMT disabled. The original fast, wrong and slow controls run at 203 ms. The hybrid-merge and block-decomposition implementations run at 203 ms and 30 seconds. A counterexample requires the same variant to produce the same unanimous disagreement on both boots. The original fast and wrong verdicts, the live slow timeout and all ample answers must also reproduce. [Pre-registered gate](plan.json)

To rebuild the input archive, supply the authenticated parent experiment payload and the `pilot/runtime.squashfs` from the v13 transfer study. The manifest contains the SHA-256 of every guest member and each of the 14 distinct HTTP bodies.

```sh
python3 prepare.py --exact-payload /path/to/original-panel/payload \
    --v12-image /path/to/gce-v13-judge-transfer/pilot/runtime.squashfs --out /tmp/inversions-v12
python3 test_v12.py
python3 replay.py --payload /tmp/inversions-v12/payload
```

A separate ordinary HTTP test used the updated v12 controller and original sealed worker. The five inversion submissions all used native judging. The fast, hybrid and block-decomposition submissions returned AC, the wrong submission returned WA, and the slow control hit the native CPU limit. Both retained problems produced measured AC, WA and in-flight TLE. Regenerating the controller settings applies this revised scope. The 11 local requests verify routing; the time comparisons above use the GCE panel. [Ordinary HTTP receipts](evidence/ordinary-routing.json.gz) · [Routing replay](smoke_routing.py)
