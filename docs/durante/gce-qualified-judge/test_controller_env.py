import json
import unittest

from controller_env import REGISTRY, controller_environment


class QualifiedControllerEnvironmentTest(unittest.TestCase):
    def test_each_sealed_worker_has_an_exact_controller_scope(self):
        bindings = {}
        for engine in ("v12", "v13"):
            with self.subTest(engine=engine):
                env = controller_environment(engine)
                self.assertNotIn("AONOHAKO_DURANTE_JUDGE_AUTO", env)
                registry = json.loads(env[REGISTRY])
                self.assertEqual(registry["schema"], 2)
                cases = registry["cases"]
                expected = [("range_min", 183, 768, 60000), ("shortest_paths", 65, 768, 60000)]
                if engine == "v12":
                    expected.insert(0, ("inversions", 203, 768, 60000))
                self.assertEqual(
                    sorted((c["problem_id"], c["time_ms"], c["memory_mb"], c["wall_time_ms"]) for c in cases),
                    expected,
                )
                bindings[engine] = {c["model_binding"] for c in cases}
                self.assertEqual(len(bindings[engine]), 1)
        self.assertTrue(bindings["v12"].isdisjoint(bindings["v13"]))


if __name__ == "__main__":
    unittest.main()
