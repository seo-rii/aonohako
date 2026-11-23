#!/usr/bin/env python3
"""Recompute the paired CPP20 v9/v10 Cloud Run comparison from individual requests."""
import argparse
from collections import Counter
import json
from math import comb, exp, log
from pathlib import Path
from statistics import median

from verify_durante_native_boundaries import digest, require
from verify_durante_v9_performance import ARMS, check_arms, check_placement, check_trials, encoded


PROTOCOL_SHA256 = "10e3f324872cf519380608a1b415223cfb08d9e8b8b95e5febee69760ebbc2f1"


# From the production-tag builds, recorded before the Cloud Run campaign.
BINARY_DIGESTS = {
    "baseline": ("e47753e85c8d550939ceef370fc86ff444b5f2c52653f49dff284fa0edbf3840",
                 "7dec997f4be38823e35848e5ee0bcff41b1d4dfc10a144542d8ef989957a6f17"),
    "candidate": ("082f83a6456e42469cf31aba5cd44ea4ad53dda29dd616cea7be7f71a40f4864",
                  "0778403bbafec22588f41ddf7abaf8b7789794fe3074550c9b825b04f29e7378"),
}


def schedule(design):
    def key(epoch, name):
        return digest(f"{design['seed']}/{epoch}/{name}".encode())

    blocks = design["blocks"]
    emitted = Counter()
    epochs = max(blocks.values())
    for epoch in range(epochs):
        active = [name for name, count in blocks.items() if epoch in {i * epochs // count for i in range(count)}]
        for name in sorted(active, key=lambda n: key(epoch, n)):
            k = emitted[name]
            emitted[name] += 1
            a, b = ARMS if (int(key(-1, name)[:2], 16) + k) % 2 == 0 else reversed(ARMS)
            for position, arm in enumerate((a, b, b, a)):
                yield "full", 2 * k + position // 2 + 1, name, design["clock_budget_ms"], arm
    require(emitted == Counter(blocks), "incomplete block schedule")
    for n in range(design["cutoff_pairs"]):
        for j, (name, ms) in enumerate(design["cutoff_ms"].items()):
            for arm in (ARMS if (n + j) % 2 == 0 else reversed(ARMS)):
                yield "cutoff", n + 1, name, ms, arm


def check_block_estimates(case, rows, count):
    ratios = []
    for index in range(0, len(rows), 4):
        block = rows[index:index + 4]
        require(len(block) == 4 and [r[0] for r in block] == list(range(block[0][0], block[0][0] + 4)),
                "incomplete or disordered four-request block")
        order = [r[2] for r in block]
        require(order[0] == order[3] and order[1] == order[2] and order[0] != order[1], "unbalanced arm order")
        logs = {arm: sum(log(r[4]) for r in block if r[2] == arm) for arm in ARMS}
        ratios.append(exp((logs["candidate"] - logs["baseline"]) / 2))
    require(len(ratios) == count, "incorrect number of blocks")
    effects = [round(100 * (r - 1), 5) for r in ratios]
    # With four blocks even [min, max] is below 95% coverage for an iid continuous median.
    k = max((k for k in range(1, count // 2 + 1)
             if 2 * sum(comb(count, i) for i in range(k)) * 100 <= 5 * 2**count), default=0)
    bounds = [sorted(effects)[k - 1], sorted(effects)[-k]] if k else None
    expected = dict(block_cpu_change_percent=effects, median_block_cpu_change_percent=round(100 * (median(ratios) - 1), 5),
                    faster_blocks=sum(r < 1 for r in ratios), slower_blocks=sum(r > 1 for r in ratios),
                    order_statistic_interval_percent=bounds, endpoint_rank=k or None)
    require(case["counterbalanced_cpu"] == expected, "incorrect block effect, direction count or interval")
    distribution = {}
    for key, col in (("aonohako_cpu_ns", 4), ("aonohako_wall_ms", 5)):
        distribution[key] = {}
        for arm in ARMS:
            values = [r[col] for r in rows if r[2] == arm]
            center = median(values)
            distribution[key][arm] = dict(min=min(values), median=center, max=max(values),
                                           median_absolute_deviation=median(abs(v - center) for v in values))
    require(case["distribution"] == distribution, "incorrect timing distribution")


def verify(data):
    protocol = data["protocol"]
    require(data["schema"] == 1 and digest(encoded(protocol)) == data["protocol_sha256"] == PROTOCOL_SHA256,
            "incorrect v9/v10 protocol")
    design = protocol["design"]
    check_arms(data, protocol, ("v9", "v10"))
    link = protocol["package_link"]
    require(data["package_link"] == link and link["core_sha256"] == protocol["platform"]["dynamorio_core_sha256"],
            "incorrect shipped core")
    for arm, field in zip(ARMS, ("baseline_rebuild_sha256", "candidate_sha256")):
        pinned, observed = protocol[arm], data["arms"][arm]
        require((observed["worker_sha256"], observed["reference_sha256"]) == BINARY_DIGESTS[arm],
                f"{arm}: unrecognized worker or reference binary")
        require(link[field] == pinned["ordinary_client_sha256"] and link[arm + "_text_sha256"] == pinned["client_text_sha256"]
                and all(observed[k] == pinned[k] for k in ("aonohako_source_commit", "aonohako_build_tag")),
                f"{arm}: package or worker source mismatch")
    plan = list(schedule(design))
    require(digest(json.dumps(plan, separators=(",", ":")).encode()) == design["schedule_sha256"],
            "request schedule differs from the sealed plan")
    expected = {i: (phase, name, rep, arm, ms) for i, (phase, rep, name, ms, arm) in enumerate(plan, 1)}
    rounds = {name: 2 * n for name, n in design["blocks"].items()}
    trials = dict(protocol, full=dict(ids=list(rounds), nominal_ms=design["clock_budget_ms"]),
                  cutoff=dict(nominal_ms=design["cutoff_ms"]), input_manifest_sha256=design["sha256_inputs"],
                  allowlist_sha256=design["sha256_allowlist"])
    check_trials(data, trials, expected, rounds)
    identities = {name: data["cases"][name]["identity"] for name in ("merge-empty", "merge-short", "merge-long")}
    source = identities["merge-empty"]
    require(all(identity["source_sha256"] == source["source_sha256"] and identity["elf_sha256"] == source["elf_sha256"]
                for identity in identities.values()), "held-out inputs must use the same executable and source")
    manifest = dict(schema=1, source_sha256=source["source_sha256"], elf_sha256=source["elf_sha256"],
                    cases={name: {k: ident[k] for k in ("stdin_sha256", "stdout_sha256")}
                           for name, ident in identities.items()})
    require(digest(encoded(manifest)) == data["source_fixture_manifests_sha256"]["transfer"] == design["transfer_manifest_sha256"],
            "incorrect held-out fixture manifest")
    for name, case in data["cases"].items():
        check_block_estimates(case, case["full"], design["blocks"][name])
    check_placement(data, len(plan), sum(rounds.values()), design["cutoff_pairs"] * len(design["cutoff_ms"]))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("receipt", type=Path)
    args = parser.parse_args()
    try:
        verify(json.loads(args.receipt.read_bytes()))
    except (OSError, KeyError, TypeError, ValueError, RuntimeError) as error:
        parser.exit(1, f"invalid v10 performance receipt: {error}\n")
    print("Verified 88 balanced four-request blocks and 12 work-clock cutoffs on one Cloud Run instance, including cleanup.")
