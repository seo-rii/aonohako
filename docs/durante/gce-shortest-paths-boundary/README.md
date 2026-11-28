# Two false timeouts at the original 65 ms shortest-paths limit

On each of two independent CPU-pinned GCE Milan boots, two correct C++20 block-minimum Dijkstra submissions finished within the previously admitted 65 ms limit in every native run. The sealed v12 and v13 deciding clocks stopped both programs in flight on every corresponding run. This disqualifies this exact input on both versions under the preregistered gate.

| Submission | Native ample CPU medians across the four engine/boot panels | Ample Durante estimate | Native at 65 ms | Durante at 65 ms |
| --- | ---: | ---: | ---: | ---: |
| Block-minimum Dijkstra, width 8 | 51.68–54.78 ms | 99.37 ms | 12 AC | 8 TLE |
| Block-minimum Dijkstra, width 2048 | 31.77–33.56 ms | 80.04 ms | 12 AC | 8 TLE |

Each engine also ran ten other correct submissions and the original AC, WA and live TLE controls. The block width controls minimum extraction from the priority queue. Both variants used identical input bytes, limits and expected output. The generous 30-second runs verify completed outputs and provide the full-work estimates in the table. The 65 ms outcomes are actual native and deciding HTTP receipts. All 540 requests are recorded. Across the twelve correct variants, matching the unanimous native results with one scalar requires more than 0.08366 and at most 0.15682 ns per retired instruction or REP byte (rounded). The current 0.23976 ns lies outside that interval. These are necessary constraints on this input only. No replacement was fitted or tested.

## Reproduction

The [protocol](protocol.md) and [registration](registration.json) were pushed in commit `5e6cdfbef314943b588ff33406b1902c902591a3` before either VM was created. The registration includes the exact request digests, runtime images, executable digests and both guest plans. The original Dial, BFS and quadratic Dijkstra binaries reproduce the compiler and executable identity of the first qualified-judge study. The variants are checked against a Python distance-relaxation oracle on the full input and an independent Floyd–Warshall oracle on smaller graphs.

From this directory, run `python3 -B replay.py` and compare with [results.json](results.json). The replay checks both sealed installations, request identities, native CPU accounting, model bindings, output, live-stop provenance, kernel exec traces, CPU affinity, independent instance and boot identities, and cloud cleanup. Run `python3 -B -m unittest -v test_boundary` to rebuild the 15 submissions and check the recorded results. The original GCE images and protocol require Debian 12 g++ 12.2.0 with the original static libraries for byte-for-byte reproduction.

To recreate the registered archive, run `python3 -B prepare.py --v12-parent PATH --v13-parent PATH --out NEW_DIR` with the unpacked v12 and v13 payloads from the [original inversion boundary](../gce-original-inversion-boundary/README.md). Pass `--payload NEW_DIR/payload` to the replay to independently reconstruct all HTTP body digests from the binaries. The [infrastructure receipts](evidence/infrastructure.json) record the successful deletion of both VMs and boot disks.
