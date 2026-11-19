#!/usr/bin/env python3
"""Reproduce the three external CPP20 workloads held out for the Cloud Run refit.

Use a checkout of ks-iitjmu/MyCSES at eeaf7b164735a4747493b0555edf688479014448
with original line endings.
"""

from durante_gce_cpp20_manifest import lcg, main


MANIFEST_SHA256 = "1f7332ffa7f0df1a519cf1b39d859617263a1b459c8d329a673737d257063aa0"


def shortest_routes_ii():
    cities = 440
    positions = [0] * (cities + 1)
    edges = []
    for city in range(1, cities):
        weight = 1 + (17 * city) % 997
        positions[city + 1] = positions[city] + weight
        edges.append((city, city + 1, weight))
    # Each chord costs one more than the path it spans, so distances are
    # the absolute differences between positions along the original path.
    for city in range(1, cities - 6):
        edges.append((city, city + 7, positions[city + 7] - positions[city] + 1))

    rng = lcg(0x1672CEED)
    queries = [(1 + (next(rng) >> 8) % cities, 1 + (next(rng) >> 8) % cities) for _ in range(256)]
    lines = [f"{cities} {len(edges)} {len(queries)}"]
    lines.extend(f"{start} {end} {weight}" for start, end, weight in edges)
    lines.extend(f"{start} {end}" for start, end in queries)
    distances = [abs(positions[start] - positions[end]) for start, end in queries]
    return "\n".join(lines) + "\n", "\n".join(map(str, distances)) + "\n"


def book_shop():
    books, budget, price = 1000, 90000, 100
    rng = lcg(0x1158CEED)
    pages = [1 + (next(rng) >> 8) % 1000 for _ in range(books)]
    # Equal prices and positive rewards make the highest 900 page counts optimal.
    best = sum(sorted(pages, reverse=True)[:budget // price])
    lines = [f"{books} {budget}", " ".join([str(price)] * books), " ".join(map(str, pages))]
    return "\n".join(lines) + "\n", f"{best}\n"


def subarray_sums_ii():
    size = 70000
    order = list(range(size))
    rng = lcg(0x1661CEED)
    for i in range(size - 1, 0, -1):
        j = (next(rng) >> 1) % (i + 1)
        order[i], order[j] = order[j], order[i]
    prefixes = [0] + order * 2
    differences = [b - a for a, b in zip(prefixes, prefixes[1:])]
    # Equal prefix sums delimit zero-sum intervals. Each nonzero prefix value
    # occurs twice, contributing one pair. Zero occurs three times, adding three.
    answer = (size - 1) + 3
    return f"{len(differences)} 0\n" + " ".join(map(str, differences)) + "\n", f"{answer}\n"


WORKLOADS = (
    ("run2_cses_shortest_routes_ii", "4_graph_algorithms/shortest_routes_II.cpp",
     "2e097d9347099446f8ea0919cf1e56ba4b7f18448046391ea2f43ecfb0a33f50", shortest_routes_ii),
    ("run2_cses_book_shop", "3_dp/Book_shop.cpp",
     "0fbb415093a2e819113341c01c82b4f179a82223e5315451f6b3996b535c6595", book_shop),
    ("run2_cses_subarray_sums_ii", "2_sorting_searching/subarray_sums_II.cpp",
     "52e3d7b08b3e8fc258a2621b43927fa080f4f0ebb5ed4d228a11ef85f2d2224f", subarray_sums_ii),
)


if __name__ == "__main__":
    main(__doc__, WORKLOADS, MANIFEST_SHA256)
