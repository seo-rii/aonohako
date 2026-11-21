# Rust native timing with CPU affinity on Compute Engine

This study repeats the long Rust comparison on a Compute Engine VM in Seoul. The question is whether reducing variation in the native controls also reduces the error of Durante's instruction and REP-byte clock. The Cloud Run and Compute Engine collections use the same program sources, input, expected output, compiler, container image and diagnostic client. Each comparison uses native CPU time measured on its own instance. [Frozen GCE protocol](gce-pinned-rust-v10/protocol.json) · [Cloud Run study](V10_NATIVE_CALIBRATION.md#rust-follow-up-with-longer-inputs)

## Native stability and timing accuracy

Across the 15 matched program/scale cases, the median native spread fell from **7.85% on Cloud Run to 1.86% on pinned GCE**. Eleven cases had a smaller spread on GCE. All three copy scales on GCE still exceeded 10% (10.73%, 11.21% and 11.64%). The table summarizes the six uninstrumented process CPU observations per case. [Paired case results](gce-pinned-rust-v10/summary.json)

| Observation | Cloud Run | Pinned GCE |
|---|---:|---:|
| Native controls at or above 20 ms | 90/90 | 90/90 |
| Observed native CPU range | 46.263–426.480 ms | 39.774–244.287 ms |
| Cases within 10% native spread | 10/15 | 12/15 |
| Largest case spread | 30.16% | 11.64% |

| Clock evaluated against GCE native CPU | Held-out cases within ±20% | Worst held-out signed error |
|---|---:|---:|
| Frozen common v10 clock | 0/9 | +212.70% on branch/4 |
| Previous short Rust fit | 0/9 | +136.06% on branch/4 |
| Previous long Rust fit from Cloud Run | 4/9 | +71.22% on sort/1 |
| New long Rust fit trained on GCE | 3/9 | +71.42% on sort/1 |

The official fitter rejected the GCE candidate (exit 2). The common clock's prediction range was outside the full native range even with an additional ±20% margin on all 15 cases. Instruction spreads were 1–17 ppm. Every GCE counter run recorded zero MOVS bytes and 3,744 STOS bytes. The 45 diagnostic executions used 637.03 CPU seconds against a mean native total of 5.07 seconds (125.58×). This cost describes diagnostic collection for this workload mix. [Uncompressed-byte digest, original dataset and official fit](gce-pinned-rust-v10/RUST2024/)

**Pinning reproduced the previously observed model limitation.** On GCE, integer/4 took at least 170.116 ms, while fill/1 took at most 48.197 ms. Even with the most favorable native endpoints, one of these two cases must have at least **51.25%** error under any fixed nonnegative weighting of instructions, MOVS bytes and STOS bytes (52.59% on Cloud Run). The strongest observed GCE pair was integer/4 against sort/1. Sort/1 took at most 40.499 ms. Native times differed by at least 4.20×, while every corresponding pair of counters permits at most a 1.265× ratio. This implies at least **53.71%** error on one case for every such clock. [Counterexample calculation and individual ranges](gce-pinned-rust-v10/summary.json)

## Follow-up on Rust grading accuracy

Durante rebuilt the five original Rust programs from the same sources. The resulting ELF digests differed from those in the Aonohako study above. The subsequent Durante studies reused the archived Durante binaries byte for byte. [Build and input provenance](https://github.com/seo-rii/durante/blob/caaa53fac348855601eb5dcf3e1c0dd5c905f45d/docs/GCE_RUST_INSTRUCTION_MIX.md)

Durante measured modeled instruction latency and nominal memory operand bytes on these executables and three additional algorithms. A clock fitted on integer and copy passed all 9 original validation cases. It missed all three additional algorithms, Euclidean GCD, binary lower bound and rolling window. Across all 18 cases, even allowing each workload its most favorable observed timings and counters left at least 41.35% worst-case error for a nonnegative two-weight clock. [Operand-byte study and raw measurements](https://github.com/seo-rii/durante/blob/caaa53fac348855601eb5dcf3e1c0dd5c905f45d/docs/GCE_RUST_OPERAND_BYTES.md)

An opt-in load-address sampler then distinguished sequential from scattered traversal in four matched assembly programs. On a pinned GCE core it added 0.68% to total instrumented CPU time across 36 paired runs. A penalty scaled by modeled cycles per eligible load brought all four assembly programs within ±20%. On the eight scale-one Rust programs the frozen clock and the corrected clock passed the same five. Binary search took 34.45 ms natively, compared with 21.66 ms for the frozen clock and 120.89 ms with the scaled penalty. Even using the full observed timing ranges, serial scattered traversal requires more than 12.60 ns per far load, while binary search permits at most 3.17 ns. [Sampling study, archived inputs and timing receipts](https://github.com/seo-rii/durante/blob/9f9df437871b729669e3e79dd006be7bcc6b6c73/docs/GCE_ACCESS_SAMPLE.md)

A separate Durante experiment changed how the meter charges unsigned 32-bit division. It fitted divider latency and throughput on four assembly controls, then tested the frozen coefficients on two new Rust programs on a pinned GCE core. Both passed the ±20% gate. The grouped-floor-quotient error fell from +101.82% to −10.25%, and the batched-quotient error changed from −51.54% to +14.70%. On the same guest GCD took 39.276 ms. The old model estimated 62.02 ms and the revised model estimated 30.39 ms (−22.62%). Computing the revision at block translation added 0.28% to total instrumented CPU across these four programs. The new client declares research latency version 9001, which the normal reader rejects. [Divider measurements, validation and raw records](https://github.com/seo-rii/durante/blob/60fd8eed503242681191510be9546e16e10bbe68/docs/GCE_DIVIDE_CLOCK.md)

A matched GCD experiment found that changing one byte in the loop's exit condition changed native CPU from 38.507 ms to 20.102 ms on the same irregular input. Shift the same pairs into loop-length order and the two times become 19.711 ms and 19.908 ms. Version 9001 estimates 30.951 ms in all four conditions. An experimental loop-aware clock then tested five inputs on a second pinned VM. Three new random GCD inputs had errors within 1.3%, and grouped Fibonacci pairs passed ±20%. With regularly alternating pairs it predicted 152.884 ms for 71.068 ms of native work (+115.12%). All measured division and loop counts matched the input oracles. [Matched controls and prospective Rust validation](https://github.com/seo-rii/durante/blob/6fc1aea49af18e03fab981dd01e04da008293f12/docs/GCE_GCD_CONTROLS.md)

Durante then kept the same weights and counted how often a changed length matched neither of two previously observed successors. On a third independently pinned Milan VM, the revised clock passed both repeat inputs and all four new inputs. The alternating case took 71.093 ms and was predicted at 71.622 ms. The largest error across all 24 predictions was 5.61% or less. The history probe added 4.37% instrumented CPU over the divider clock. This is an opt-in research estimator for recognized unsigned 32-bit Euclidean loops. Binary search and other Rust workloads still require independent timing validation before a general time-limit policy can use the model. [Independent history validation and placement](https://github.com/seo-rii/durante/blob/9eed9fbda2476c6c937016fcde701fb7590301bc/docs/GCE_GCD_HISTORY.md)

These results leave the proposed Rust coefficients unqualified for Aonohako time-limit verdicts. Aonohako observation mode can collect its supported counters alongside native grading. Collecting new load-address measurements requires the separate Durante research client. The published records can be replayed offline. Operators evaluating a new clock can use the archived GCD and search failures alongside the original five programs before validating on fresh programs and the intended runtime. [Observation and qualification workflow](../durante-judging.md#automatic-observations-alongside-native-grading)

## Collection

The `n2d-standard-4` VM in `asia-northeast3-a` exposed two guest CPUs with one thread per core. Benchmark work used guest CPU 1 and the affinity observer ran on CPU 0. The provider reported AMD Milan. The earlier Cloud Run instance reported an AMD EPYC 9B14. These runs vary both platform and CPU model. Repeated inputs characterize the observed variability on each instance. They cannot isolate the contribution of pinning from the host or hypervisor. [Compute Engine threads per core](https://docs.cloud.google.com/compute/docs/instances/set-threads-per-core)

The benchmark image was reconstructed from its immutable base and archived layer. Its full OCI manifest reproduced the Cloud Run image digest `sha256:856be473011809c36a32d7ecfb6501c45db07b3204b7e475be515ca4baa6cf95`. The Rust compiler was 1.95.0. The reference tool ran the ordinary embedded compile and execute handlers with the helper sandbox on the VM. Each program's compiled binary hash remained fixed throughout its nine triplets. All five recompiled hashes differed between platforms; across platforms this is a matched source/input/toolchain comparison. [Executable digests](gce-pinned-rust-v10/summary.json)

For each of five programs at three scales, the reference tool ran native, Durante diagnostic, then native three times in that order. Each of the 15 cases thus pairs three counter observations with six native CPU controls. Native spread is the difference between the largest and smallest native CPU time divided by their median. An error of at most 20% counts as a success for a held-out case only if all three predictions meet that threshold against the case median. Integer and copy supplied the two training programs. Branch, sort and fill supplied the nine held-out cases.

## Evidence for CPU placement

Linux process-lifecycle tracing captured all 90 native program execs, 45 Durante launcher execs and their 45 DynamoRIO loader execs on CPU 1. The full trace contained 6,200 records across both guest CPUs, with zero recorded drops or overruns. All 1,908 lifecycle records belonging to the collector and its descendants were on CPU 1. An observer on CPU 0 independently polled thread masks and last-run CPUs 2,769 times, covering 144 distinct TIDs. Every observed container thread was restricted to CPU 1. The container's effective cgroup cpuset stayed at 1, and the normal guest system and user slices were on CPU 0. The guest reported zero additional steal ticks on CPU 1 during collection (100 ticks per second). Hypervisor scheduling and physical-host load remain outside this guest-level audit. [Placement](gce-pinned-rust-v10/affinity.json) · [Sanitized process lineage](gce-pinned-rust-v10/lineage.json.gz)

All 135 execution receipts selected final wait-based process CPU accounting. The temporary VM and its auto-delete boot disk each returned HTTP 404 after deletion. [Guest configuration, attempts and cleanup](gce-pinned-rust-v10/platform.json)

## Repeating the collection

Use a disposable Linux guest with cgroup v2, tracefs, two visible CPUs on separate cores and the Rust benchmark image and v10 binaries pinned in the protocol. In this run the regular system and user services used CPU 0. Docker placed the network-isolated benchmark container under a separate cgroup with `--cpuset-cpus 1` and 2 GiB of memory. The measured collector used the embedded reference API on loopback, with local reference authentication and CPU normalization and short-case sampling disabled.

The command below runs inside that container. Use a fresh output directory per attempt. The native limits are 10 s CPU and 30 s wall, and the diagnostic run has 90 s CPU and 120 s wall as safety limits. The trace observer runs on CPU 0 outside the container over the whole measured interval.

```sh
taskset -c 1 /usr/local/bin/durante-reference collect \
  --language RUST2024 --corpus-id rust-long-reference-v1 --repetitions 3 \
  --metered-cpu-ms 90000 --metered-wall-ms 120000 --timeout 50m \
  --output /out/collection
```

The first launch stopped at configuration validation with zero samples. The guest's embedded reference API requires local `inbound_auth=none`. After selecting that setting inside the network-isolated container, the collector used the frozen workload and clock settings. [First attempt](gce-pinned-rust-v10/attempts/8416d6c50fde32951f1d9920d31146648ed7e8739053c2dd37f1654f8acd389f.json)

Run the guest capture script on the housekeeping CPU. The container must already be running and hard-pinned. Use a unique private directory for trace and sampler files. This collector reads the GCE instance ID from the guest metadata service and hashes it for the receipt. Capture covers process lifecycle and cgroup moves. The reduction publishes only the measured lineage and aggregates, leaving unrelated guest events in the private trace. [Guest capture script](gce-pinned-rust-v10/pin_audit.py) · [Receipt reduction](gce-pinned-rust-v10/affinity_summary.py)

```sh
taskset -c 0 python3 pin_audit.py start --cpu 1 --init-pid "$container_init_pid" --out "$trace_dir"
# Run the collector above and wait for it to exit, keeping the container alive.
taskset -c 0 python3 pin_audit.py stop --cpu 1 --init-pid "$container_init_pid" --out "$trace_dir"
python3 affinity_summary.py "$trace_dir" dataset.json lineage.json.gz > affinity.json
```

From the repository root, replay the archived measurements, observed guest placement, Cloud Run baseline and all four clock comparisons. Pass `--reference /path/to/durante-reference` to replay the official Go fit using the reference binary pinned by the long Rust protocol.

```sh
study=docs/durante/gce-pinned-rust-v10
python3 scripts/verify_durante_gce_rust.py "$study" --verify "$study/summary.json"
```
