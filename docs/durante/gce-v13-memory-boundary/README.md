# Native and Durante memory near the 768 MiB limit

On two fresh GCE boots, a 764 MiB table returned the correct output in every native execution at a 768 MiB limit. All four matching Durante executions completed with the correct output, yet their measured peak exceeded 768 MiB. At table sizes of 768 and 816 MiB, every native execution hit its memory limit while each unscored Durante execution completed within its larger raw envelope.

| Table size (MiB) | Native verdict (four executions) | Native reported peak (KiB) | Durante reported peak (KiB) |
| ---: | --- | ---: | ---: |
| 32 | Accepted | 0–33,520 | 37,420–37,480 |
| 720 | Accepted | 738,112–738,116 | 742,152–742,444 |
| 764 | Accepted | 783,168–783,172 | 787,108–787,224 |
| 768 | Memory Limit Exceeded | 787,020–787,268 | 791,148–791,224 |
| 816 | Memory Limit Exceeded | 789,608–792,660 | 840,492–840,592 |

The nominal threshold was 786,432 KiB. The native RSS watchdog stopped the 816 MiB allocation while the target was still touching pages, so its reported peak is close to the threshold. Two short native runs returned zero for the observed peak. All 20 instrumented runs supplied a positive peak.

A scoped deciding judge that observes a peak above the nominal threshold, or lacks a positive sample, withholds the competitive verdict and score. A raw diagnostic still reports its counters and output check. Passing this one-sided check does not establish exact native memory use. Both implementations sample usage, and these paired observations do not qualify other programs, runtime images or memory limits.

## Reproduce the comparison

The test program initializes a byte vector of the requested size and makes one or eight passes, reading and updating the first byte of each 4 KiB page. Its sum is computed independently from table size and pass count. Each boot ran five sizes with both pass counts in both modes. The second boot reversed the order. Both workers ran on CPU 1 of an `n2d-standard-4` AMD Milan VM with one hardware thread per core. The metered worker used the sealed v13 engine and a 1024 MiB raw envelope. The native and raw programs were byte-identical. The twenty memory trials per boot ran after the main clock experiment. [Frozen collector, C++ source and image preparation](https://github.com/seo-rii/aonohako/tree/a5a98052e36d7b0dd5e88d3e5fb2c230e51c8695/docs/durante/gce-inversion-clock)

Run the following from the repository root.

```sh
python3 -B docs/durante/gce-v13-memory-boundary/replay.py
```

The replay checks both GCE lifecycle records, boot-specific file hashes, request hashes, CPU 1 exec traces, measured CPU receipts and native stop causes. For each raw receipt it verifies the installation against the sealed v13 image, clock progress against terminal counts, the output oracle and the raw limits. The resulting table uses every scheduled trial.

A separate local HTTP check sent 27 ordinary requests to an unregistered native route, an updated public controller forwarding to the sealed schema-1 worker, and an updated embedded worker. Each route received correct-output, wrong-output and nonzero-exit programs at 32, 764 and 816 MiB. All three routes returned the usual AC, WA and RE at 32 MiB. At 764 MiB native retained those three verdicts, while both instrumented routes withheld all three. At 816 MiB native returned MLE in all three cases, and both instrumented routes again withheld all three. The Go regression also exercises missing RSS, virtual time stops, raw watchdog expiry and rejection of inconsistent forwarded receipts.
