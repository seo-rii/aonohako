# Durante v11 client cost on Compute Engine

These measurements compare the release v10 and v11 clients used by the Aonohako worker. Each trial runs one static x86-64 program through the same Durante supervisor and DynamoRIO core. The timer covers user and system CPU for the supervisor and its reaped child, including startup and terminal report collection. The measurements cover the client portion of a worker execution.

## Results

Each cell gives the two median CPU times and the median of six paired ratios (second arm divided by first). The 44-instruction operand fixture principally measures startup. The store/branch loop executes one 8-byte store and load per iteration and takes an additional arithmetic instruction on even iterations.

| Fixture | Ordinary v10 → v11 | Latency v10 → v11 | v11 latency → latency plus bytes | Live-clock v10 → v11 |
| --- | ---: | ---: | ---: | ---: |
| Operand sentinel | 12.995 → 12.927 ms (0.994×) | 13.421 → 13.492 ms (1.005×) | 13.768 → 14.011 ms (1.024×) | 14.049 → 14.008 ms (0.998×) |
| 40,000,011 instructions | 561.809 → 45.149 ms (0.080×) | 566.330 → 597.687 ms (1.055×) | 598.831 → 593.409 ms (0.991×) | 52.103 → 51.922 ms (1.000×) |
| 13,000,012 instructions with stores | 179.631 → 24.363 ms (0.136×) | 177.900 → 184.988 ms (1.038×) | 185.135 → 186.488 ms (1.008×) | 189.669 → 191.224 ms (1.008×) |

The kernel trace placed all 168 supervisors and 168 metered process roots on CPU 1 at exec. The trace had zero overruns and drops. CPU 1 recorded 0 guest steal ticks during the comparison. The original and new clients agreed on every instruction and REP count. Latency-only v10 and both v11 latency variants agreed on all three cycle totals. Every requested final live-work result agreed with its terminal counters. The 623-byte sentinel also checked its XMM value after the last store. All 168 reports were complete, valid, uninterrupted and diagnostic-only. [Paired CPU samples, ranges, exact counters, and binary digests](gce-v11.json)

## Reproduction

Use a single n2d-standard-4 (AMD Milan, one hardware thread per core) with the benchmark cgroup restricted to CPU 1 and the trace controller pinned to CPU 0. This run used debian-12-bookworm-v20261117 and Linux 6.1.0-56-cloud-amd64. The archived v10 and v11 client digests and the common runtime digest are recorded with the samples. The boot disk had automatic deletion enabled. Both the instance and its disk returned HTTP 404 after cleanup and the disposable key was removed.

Extract the three assembly sources from the receipt and build them with GCC 12.2.0 and GNU ld 2.40 on x86-64 Linux.

```sh
python3 - <<'PYCODE'
import hashlib
import json
import subprocess
from pathlib import Path
evidence = json.loads(Path("gce-v11.json").read_text())
for name, item in evidence["fixtures"].items():
    Path(name + ".S").write_text(item["assembly"])
    obj = item["link_object"]
    subprocess.run(["gcc", "-DFIXTURE=1", "-c", name + ".S", "-o", obj], check=True)
    subprocess.run(["gcc", "-static", "-nostdlib", "-no-pie", "-Wl,--build-id=none",
                    obj, "-o", name], check=True)
    assert hashlib.sha256(Path(name).read_bytes()).hexdigest() == item["elf_sha256"]
PYCODE
```

Pass each binary to the recorded `durante run` command using absolute paths and a fresh report path for every run. The receipt lists the flags and order for all four comparisons. Perform one untimed native output check per fixture, then discard the first instrumented pair for each fixture and comparison. In the six measured pairs, alternate arm order according to the recorded schedule. Use `wait4` to record user plus system CPU for the launched supervisor; all child processes inherit the same CPU restriction. The supplied ranges show how much the six pairwise ratios varied on this VM. [Machine-readable protocol and fixtures](gce-v11.json)
