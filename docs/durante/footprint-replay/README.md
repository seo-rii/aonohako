# Replay Durante's CPU-pinned footprint study in Aonohako

The replay compares Aonohako's Go work-clock decision with each recorded Durante
decision for 13 held-out inputs, two calibration methods and four deadlines per
input. The guest recorded the live counter values for three static C++ programs
on CPU 1 of an AMD Milan Compute Engine VM. [Study and raw data](https://github.com/seo-rii/durante/blob/d69f19b29b796e29b6d37593e915ec512339c0ed/docs/GCE_FOOTPRINT_ADMISSION.md)

Use Python 3.11 or newer, a trusted Durante checkout at commit
`d69f19b29b796e29b6d37593e915ec512339c0ed`, and the original `fenwick`,
`lookup` and `window` ELFs in one directory. With Debian 12's g++ 12.2.0 and
static libraries, these commands reproduced all three registered ELF hashes.

```sh
durante=/path/to/durante
out=$(mktemp -d)
mkdir "$out/binaries"
for source in "$durante/experiments/calibrated_limits/fenwick.cpp" \
              "$durante/experiments/calibrated_limits/lookup.cpp" \
              "$durante/experiments/shape_calibration/window.cpp"; do
  g++ -std=c++20 -O2 -static -s -march=x86-64-v2 -Wl,--build-id=none \
    "$source" -o "$out/binaries/$(basename "$source" .cpp)"
done
```

From the Aonohako repository root, build the diagnostic CLI and run the replay.

```sh
go build -o "$out/durante-reference" ./cmd/durante-reference
python3 docs/durante/footprint-replay/replay.py \
  --durante "$durante" --binaries "$out/binaries" --reference "$out/durante-reference"
```

The script pins the SHA-256 of the collection manifest, checks the registered
sources and raw archive, and invokes Durante's verifier to reproduce the
chronological selection of paid calibrations and validate the measurements.
It regenerates 26 per-input projections and calls `replay-input-deadline` for
each of the 104 live or terminal observations. Each result must agree on the
policy, selection, process ID, deadline, diagnostic authority and decision.
An unavailable projection or a mismatch exits with an error. The replay hashes
the original binaries and inputs and performs all comparisons offline.

| Calibration method | Within budget | Exceeded | Matched archived decisions |
| --- | ---: | ---: | ---: |
| Temporal | 27 | 25 | 52/52 |
| Footprint | 22 | 30 | 52/52 |

These counts measure agreement between the two clock implementations. Durante's
study reports native CPU-time accuracy and the two deadlines at which native
runs fell on both sides of the limit.
