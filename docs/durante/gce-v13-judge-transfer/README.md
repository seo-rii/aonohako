# Durante v13 transfer on CPU-pinned GCE judges

## Transfer results

Across two CPU-pinned Milan guests all six registered gates passed. The v13 environment covers range minimum at 183 ms, shortest paths at 65 ms, and inversion counting at 203 ms, for the three exact original inputs. The exported clock binding is `work-clock-v1:sha256:45bbdab9d7b82b05a27bc55cae38ab7811e6699542f792634f374e2036aecb69`. [Replayed results](results.json) · [Guest identities, evidence hashes and cleanup](evidence/infrastructure.json)

| Observation | v12 | v13 |
|---|---:|---:|
| Held-out boundary groups agreeing with native | 40/48 | 40/48 |
| False timeouts on those groups | 8 | 8 |
| Missed timeouts on those groups | 0 | 0 |
| Original reference boundaries agreeing with native | 10/12 | 10/12 |
| Worst absolute virtual-time error on 24 ample-budget case/guest medians | 68.62% | 68.62% |

All eight false timeouts occurred at the high limits for the two held-out inversion-counting implementations. Both clocks accepted the original range-minimum reference at its low limit on both boots while native execution timed out. These diagnostic limits remain outside the release list.

The deterministic control returned exactly 60,018 instructions, 4,096 MOVS bytes and 4,096 STOS bytes on all eight runs. The C++ binaries produced exactly matching counter tuples in 72 of 120 paired completions. For a given ELF and input the maximum instruction spread was 42 across engines, boots and limits. All recorded terminal REP byte counts agreed.

With the release configuration, which has latency counting off, the median paired v12/v13 raw CPU ratio was **0.998×** across the 24 held-out ample-budget case/guest medians. Separately, with both the live page and latency bound enabled, the v12/v13 raw CPU ratio was **8.49–9.99×** on these three fast implementations. Each paired instruction, `cycles_x16` and REP-byte count agreed in that diagnostic.

| Latency-enabled v12/v13 median CPU ratio | Primary guest | Repeat guest |
|---|---:|---:|
| Range minimum | 8.86× | 9.02× |
| Shortest paths | 8.66× | 8.49× |
| Inversion counting | 9.94× | 9.99× |

## Registered experiment

The frozen v12 pilot covers three exact CPP20 inputs on AMD Milan. The selected clock uses 1139371945900000 / 4752214584908649 ns per instruction and REP byte. The candidate changes the engine to Durante commit `6b4d74f248235f8f616aa5b8e151ca0db5a029de`. The experiment holds the coefficient, solvers, expected outputs, deadlines, and program options fixed. Each arm binds its own worker image, installation, engine and judge clock. [Original selection and qualification](../gce-qualified-judge/refit/README.md)

Two independent, SMT-disabled N2D Milan boots execute target processes on CPU 1. Each receives the same authenticated payload and runs real HTTP workers with read-only runtime mounts. The primary boot runs v12 first and the repeat boot runs v13 first. Each case receives three native executions on the v12 worker and two executions with each deciding clock on each guest. Both instrumented arms use the native controls from that same boot.

- The original panel contains independent fast, wrong, and slow solutions at the contest deadlines, fast solutions at 1 ms, and reference solutions at both frozen boundary limits for each of three inputs.
- The diagnostic panel compares two other input seeds and two implementations per problem at the previously registered low and high limits. All twelve pairs also run at 30 seconds to compare completed counters, stdout and raw CPU overhead.
- The main panel contains 378 scheduled requests per boot, comprising 162 native, 108 v12 and 108 v13. Two additional executions per arm use a fixed-address freestanding control whose data movement is exactly 4096 MOVS bytes and 4096 STOS bytes. A separate latency-enabled diagnostic records CPU cost and terminal counts on the three fast solvers. All twelve diagnostic receipts per guest must be complete and valid. The numerical speedup and counter differences have no promotion threshold.

### Prospective release criteria

1. On both boots all three arms must produce AC, WA and TLE as registered at all three contest deadlines and TLE at the 1 ms controls. Every observed instrumented timeout in these controls must be an in-flight budget stop.
2. At all original and held-out boundary limits, every pair of v12/v13 judge repetitions on both boots must agree. Record unanimous native errors, false timeouts, false passes and mixed groups separately relative to each engine.
3. Every held-out 30-second execution must terminate with the independently expected stdout and verdict. All completed responses for a given input/program must agree on stdout and both REP byte counters. At most 128 instructions of total spread across completed observations is permitted per input/program. The previous frozen v12 evidence already contained startup variation of up to 42 instructions on identical static C++ binaries. Report exact cross-engine matches and maximum spread separately.
4. Every freestanding control must complete with its fixed output and the exact known REP counts. Instruction count must agree exactly across all eight instrumented invocations. All live and terminal receipts must pass the existing weighted-clock checks and bind the pinned engine, image, installation and runtime options.
5. Require two fresh instance, disk and boot identities, verified CPU placement for every registered judging request, complete evidence digests and deletion of each instance and boot disk. A failed criterion withholds the v13 environment. The v12 release remains usable.

The raw CPU and wall watchdogs remain 120 and 170 seconds. Report median raw target CPU on completed executions separately from the virtual time used for a verdict. The latency-enabled diagnostic has its own model and timing context. These tests cover cooperative static CPP20 programs with integrity mode off. Cases outside the original three are diagnostics and cannot enter the operator environment.
### Replay

Run these commands from the Aonohako repository root. The first replays all 756 main requests, eight deterministic controls, 24 latency diagnostics and both cleanup receipts. The second compares the replay with the published results, verifies the three original v12 release entries and checks all twelve ordinary HTTP receipts against the candidate environment. It prints the v13 environment only if those checks pass. Automatic routing additionally requires the operator to set `AONOHAKO_DURANTE_JUDGE_AUTO=on` on the compatible control plane.

```sh
study=docs/durante/gce-v13-judge-transfer
python3 -B "$study/verify.py"
python3 -B "$study/verify.py" --operator-env
```

A separate offline container ran the sealed v13 binary as two HTTP servers, an auto-enabled controller and its worker. Twelve ordinary requests covered AC, WA and an in-flight TLE for every registered problem, plus native execution on an unregistered problem ID. To repeat it, mount the extracted image at `/opt/durante`, provide executable tmpfs at `/work` and `/tmp`, and run `smoke_auto.py --payload PATH --output NEW.json` on the authenticated payload from `prepare.py`. [Automatic routing receipts](evidence/automatic-routing.json.gz)

For current automatic judging, use the sealed v13 binary as the worker with its schema-1 environment and automatic routing off. Build the controller with the v13 tag and export its schema-2 configuration with `python3 -B docs/durante/gce-qualified-judge/controller_env.py --engine v13`. Enable `AONOHAKO_DURANTE_JUDGE_AUTO=on` on that controller. It also matches the measured nominal 768 MiB memory and 60,000 ms wall limits. [Controller setup](../../durante-judging.md)

### Building and selecting the measured image

The builder used Debian Bookworm, GCC 12.2.0 and CMake 3.25.1. Aonohako built both the server and the reference tool with Go 1.26.11, CGO disabled and the v13 build tag. The read-only SquashFS includes the client, the corrected core, the launcher and both Go executables. [Packaging commit](https://github.com/seo-rii/aonohako/commit/b7f7ab7e3dbbba6226735fc4179f2708be4088df) · [Compiler and installed-file hashes](evidence/builder.json)

From that packaging commit, run the following in a clean Linux x86-64 checkout with the build tools and pinned Go toolchain installed. The vendored fetcher verifies the SDK and builds the two patched runtime profiles. Use a fresh output directory.

```sh
set -eu
umask 022
build=$(mktemp -d)
python3 scripts/verify_durante_vendor.py --version v13 --output "$build/vendor"
python3 "$build/vendor/toolkit/fetch_sdk.py"
sdk="$build/vendor/toolkit/sdk/DynamoRIO-Linux-11.3.0-1"
sh scripts/build_durante_client.sh "$build/vendor" "$sdk" "$build/client"
sh scripts/install_durante_runtime.sh "$sdk" "$build/client/libdurante.so" "$build/vendor" "$build/runtime"
CGO_ENABLED=0 go build -tags durante_v13 -trimpath -buildvcs=false -o "$build/runtime/server" ./cmd/server
CGO_ENABLED=0 go build -tags durante_v13 -trimpath -buildvcs=false -o "$build/runtime/reference" ./cmd/durante-reference
mksquashfs "$build/runtime" "$build/v13.squashfs" -noappend -all-root -no-xattrs -all-time 0 -mkfs-time 0 -processors 2 -comp xz
sha256sum "$build/v13.squashfs"
```

The measured image SHA-256 is `5ebf65c242036f85ea8873926aa9e8721fa22cd8d0d3394dd32309e09354a3d2`. Compare the result with that digest before using the emitted environment. Different compiler inputs, file metadata or container packaging can yield a different fingerprint. Such an artifact needs its own measured binding.
