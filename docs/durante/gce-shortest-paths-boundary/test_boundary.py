from collections import Counter
from pathlib import Path
import subprocess
import tempfile
import unittest

import prepare
import replay


def brute_distances(n, extra, max_cost, seed, weighted=True):
    # Floyd-Warshall on a tiny independently materialized multigraph.
    above = n * max_cost + 1
    distance = [[above] * n for _ in range(n)]
    rng = prepare.corpus.numbers(seed)
    for u in range(n):
        distance[u][u] = 0
        cost = 1 + next(rng) % max_cost
        distance[u][(u + 1) % n] = cost if weighted else 1
    for _ in range(extra):
        u = next(rng) % n
        v = (u + 1 + next(rng) % (n - 1)) % n
        cost = 1 + next(rng) % max_cost
        distance[u][v] = min(distance[u][v], cost if weighted else 1)
    for k in range(n):
        for u in range(n):
            for v in range(n):
                distance[u][v] = min(distance[u][v], distance[u][k] + distance[k][v])
    return f"{sum(distance[0])}\n"


class ShortestPathsTests(unittest.TestCase):
    def test_all_solvers_against_independent_shortest_paths(self):
        with tempfile.TemporaryDirectory() as temp:
            bins = Path(temp)
            cases = prepare.build(bins)
            for arguments in ((2, 0, 1, 1), (9, 0, 3, 333), (23, 100, 18, 314159), (37, 500, 3, 71)):
                data = " ".join(map(str, arguments)) + "\n"
                weighted = brute_distances(*arguments)
                unweighted = brute_distances(*arguments, weighted=False)
                self.assertEqual(prepare.wrong_output(arguments), unweighted)
                for role, case in cases.items():
                    with self.subTest(role=role, arguments=arguments):
                        binary = bins / Path(case["binary"]).name
                        actual = subprocess.check_output([binary], input=data, text=True)
                        self.assertEqual(actual, unweighted if role == "wrong" else weighted)

    def test_both_engine_schedules_and_bindings(self):
        reg = prepare.read(prepare.HERE / "registration.json")
        plans = reg["plans"]
        self.assertEqual(plans["v12"]["cases"], plans["v13"]["cases"])
        self.assertNotEqual(plans["v12"]["baseline_binding"], plans["v13"]["baseline_binding"])
        for engine, plan in plans.items():
            per_phase = [list(replay.v12.fixed.schedule(plan, phase, {})) for phase in replay.PHASES]
            self.assertEqual(len(per_phase[0]), reg["expected_rows"][engine])
            self.assertEqual(len(set(per_phase[0])), 135)
            self.assertEqual(set(per_phase[0]), set(per_phase[1]))
            self.assertNotEqual(per_phase[0][0], per_phase[1][0])
            reps = Counter((key, ms, arm) for key, _, arm, _, ms in per_phase[0])
            for name, case in plan["cases"].items():
                for boundary in case["limits"].values():
                    self.assertEqual(reps[name, boundary, "native"], 3)
                    self.assertEqual(reps[name, boundary, "shared"], 2)
            self.assertTrue(all(c["problem"] == "shortest_paths" for c in plan["cases"].values()))

    def test_public_replay_matches_published_results(self):
        self.assertEqual(replay.verify(prepare.HERE / "evidence"), prepare.read(prepare.HERE / "results.json"))


if __name__ == "__main__":
    unittest.main()
