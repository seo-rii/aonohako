from collections import Counter
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


def load(name):
    spec = importlib.util.spec_from_file_location("original_inversions_" + name, HERE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


protocol = load("protocol")
guest = load("guest")


class BoundaryTests(unittest.TestCase):
    def test_two_boot_schedules_have_the_same_fixed_cells_and_opposite_start_order(self):
        plan = protocol.read(HERE / "plan.json")
        cases = plan["cases"]
        self.assertEqual(len(cases), 12)
        seeds = {case["stdin"] for case in cases.values()}
        self.assertEqual(seeds, {"256 3072 4096 658417\n"})
        self.assertEqual({case["expected_stdout"] for case in cases.values()}, {"603431314\n"})
        schedules = [list(guest.schedule(plan, phase, {})) for phase in ("primary", "repeat")]
        collector = guest.collector(protocol.PREVIOUS / "guest.py")
        self.assertEqual(collector.ARMS, ("shared",))
        for phase, schedule in zip(("primary", "repeat"), schedules, strict=True):
            self.assertEqual(list(collector.prelude(plan, phase)), [])
            self.assertEqual(list(collector.schedule(plan, phase, {})), schedule)
        self.assertNotEqual(schedules[0][0], schedules[1][0])
        self.assertEqual(set(schedules[0]), set(schedules[1]))
        for schedule in schedules:
            self.assertEqual(len(schedule), 115)
            self.assertEqual(len(set(schedule)), 115)
            counts = Counter((key, boundary, kind, ms) for key, boundary, kind, _, ms in schedule)
            for key in cases:
                limits = {"contest": 203} if key == "original-slow" else {"contest": 203, "ample": 30_000}
                for name, ms in limits.items():
                    self.assertEqual(counts[key, name, "native", ms], 3)
                    self.assertEqual(counts[key, name, "shared", ms], 2)

    def test_all_variants_match_independent_oracles_with_ties_and_partial_runs(self):
        with tempfile.TemporaryDirectory() as temp:
            binaries = Path(temp)
            protocol.build(binaries)
            plan = protocol.make_plan(binaries)
            for params in ((1, 1, 1, 515), (3, 39, 7, 41), (1, 2051, 2081, 757)):
                batches, n, alphabet, seed = params
                random = protocol.corpus.numbers(seed)
                answer = 0
                for _ in range(batches):
                    xs = [next(random) % alphabet for _ in range(n)]
                    answer += sum(x > y for i, x in enumerate(xs) for y in xs[i + 1:])
                for role in protocol.VARIANTS:
                    with self.subTest(role=role, params=params):
                        text = " ".join(map(str, params)) + "\n"
                        observed = subprocess.check_output([binaries / ("inversions-" + role)], input=text, text=True)
                        self.assertEqual(observed, f"{answer}\n")
            original = plan["cases"]["original-fast"]
            params = list(map(int, original["stdin"].split()))
            self.assertEqual(f"{protocol.corpus.inversions(*params)}\n", original["expected_stdout"])


if __name__ == "__main__":
    unittest.main()
