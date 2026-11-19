#!/usr/bin/env python3
"""Reproduce the six external CPP20 workloads from the Durante GCE calibration.

Use a checkout of ks-iitjmu/MyCSES at eeaf7b164735a4747493b0555edf688479014448
with original line endings. Only the six pinned source files are read.
"""

import argparse
import hashlib
import json
import sys
from heapq import heapify, heappop, heappush
from math import isqrt
from pathlib import Path


MANIFEST_SHA256 = "6d41c52f5f923f8a2deeba794f6c3f2b58de8f2318ec2559aa1caf7e4c44912a"


def lcg(state):
    while True:
        state = (1664525 * state + 1013904223) & 0xFFFFFFFF
        yield state


def edit_distance():
    rng = lcg(0xDBD23575)
    original = "".join(chr(ord("A") + ((next(rng) >> 12) % 25)) for _ in range(5000))
    mutated = list(original)
    for i in range(0, len(original), 17):
        mutated[i] = "Z"
    # Each edit introduces at most one Z, and one substitution per Z suffices.
    return f"{original}\n{''.join(mutated)}\n", f"{mutated.count('Z')}\n"


def counting_rooms():
    size = 1000
    row = "".join("#" if x % 31 == 30 else "." for x in range(size))
    grid = ["#" * size if y % 29 == 28 else row for y in range(size)]
    # Each open row band intersects each open column band in one room.
    rooms = len(range(0, size, 29)) * len(range(0, size, 31))
    return f"{size} {size}\n" + "\n".join(grid) + "\n", f"{rooms}\n"


def factory_machines():
    periods = [1009, 1013, 1019, 1021]
    machines, target = 200000, 999983721
    group_size = machines // len(periods)
    # Each completion event produces one item per machine with that period.
    events = [(p, p) for p in periods]
    heapify(events)
    for _ in range(0, target, group_size):
        finish, period = heappop(events)
        heappush(events, (finish + period, period))
    stdin = f"{machines} {target}\n" + " ".join(map(str, periods * group_size)) + "\n"
    return stdin, f"{finish}\n"


def towers():
    rng = lcg(0x46932CE7)
    values = [1 + ((next(rng) >> 10) % 7000) for _ in range(200000)]
    # A nondecreasing subsequence needs one tower per cube. Cubes with the
    # same subsequence length ending there form a strictly decreasing tower.
    # The Fenwick tree finds the longest preceding subsequence ending at <= value.
    best = [0] * (max(values) + 1)
    for value in values:
        i, prefix = value, 0
        while i:
            prefix = max(prefix, best[i])
            i -= i & -i
        i = value
        while i < len(best):
            best[i] = max(best[i], prefix + 1)
            i += i & -i
    return f"{len(values)}\n" + " ".join(map(str, values)) + "\n", f"{max(best)}\n"


def removal_game():
    rng = lcg(0x8AC3D91B)
    pairs = [((next(rng) >> 4) % 2000001) - 1000000 for _ in range(1300)]
    values = [value for value in pairs for _ in range(2)]
    # Player one can force either parity. Player two can always take the
    # twin of player one's choice, capping the first player's score at half.
    return f"{len(values)}\n" + " ".join(map(str, values)) + "\n", f"{sum(pairs)}\n"


def counting_divisors():
    rng = lcg(0xFD28AB81)
    queries = [1, 2, 36, 64, 83160, 999983, 997 * 997, 1000000]
    queries.extend(1 + ((next(rng) >> 4) % 1000000) for _ in range(248))
    counts = []
    for value in queries:
        root = isqrt(value)
        pairs = sum(value % divisor == 0 for divisor in range(1, root + 1))
        counts.append(2 * pairs - (root * root == value))
    return f"{len(queries)}\n" + "\n".join(map(str, queries)) + "\n", "\n".join(map(str, counts)) + "\n"


WORKLOADS = (
    ("cses_edit_distance", "3_dp/edit_distance.cpp",
     "f38ea4d086e8a37be07f75b7729f692c3a1d9879410d4340a8356a44318eaca5", edit_distance),
    ("cses_counting_rooms", "4_graph_algorithms/Counting_Rooms.cpp",
     "8101472e4bb5fca278d1b9387c64cc28c51398c9692339a17270d4cdc57a6fe3", counting_rooms),
    ("cses_factory_machines", "2_sorting_searching/Factory_Machines.cpp",
     "8ef9f167b087e625a709b2863a6fddaa7d1170810d71406cae7c94284f956ab5", factory_machines),
    ("gce_cses_towers", "2_sorting_searching/towers.cpp",
     "910adc7bf6958e3d8eb1238a2c1b6046623812816b3be3f379dbd862a6e3f9e3", towers),
    ("gce_cses_removal_game", "3_dp/Removal_Game.cpp",
     "8c3cee70202324d0f75be410419c058ef72d08867eb0c59ed77f897054781d6e", removal_game),
    ("gce_cses_counting_divisors", "7_mathematics/counting_divisors.cpp",
     "0d85cc2452f058439d58355288a26ab6e8da0dc42fac37c7f1f9fb36d60a70f4", counting_divisors),
)


def manifest(checkout):
    workloads = []
    for name, path, source_hash, generate in WORKLOADS:
        source = (checkout / path).read_bytes()
        digest = hashlib.sha256(source).hexdigest()
        if digest != source_hash:
            raise ValueError(f"{path}: expected SHA-256 {source_hash}, got {digest}")
        stdin, expected = generate()
        case = {"scale": 1, "input": stdin, "expected_stdout": expected}
        workloads.append({"id": name, "source": source.decode("utf-8"), "cases": [case]})
    document = {"schema": 1, "language": "CPP20", "workloads": workloads}
    data = (json.dumps(document, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8")
    digest = hashlib.sha256(data).hexdigest()
    if digest != MANIFEST_SHA256:
        raise ValueError(f"manifest: expected SHA-256 {MANIFEST_SHA256}, got {digest}")
    return data


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("checkout", type=Path, help="local ks-iitjmu/MyCSES checkout")
    parser.add_argument("-o", "--output", default="-", help="output file (default: stdout)")
    args = parser.parse_args()
    try:
        data = manifest(args.checkout)
        if args.output == "-":
            sys.stdout.buffer.write(data)
        else:
            Path(args.output).write_bytes(data)
    except (OSError, ValueError) as exc:
        parser.exit(1, f"{parser.prog}: {exc}\n")


if __name__ == "__main__":
    main()
