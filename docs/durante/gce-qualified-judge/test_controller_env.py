import json
import unittest

from controller_env import REGISTRY, controller_environment


class QualifiedControllerEnvironmentTest(unittest.TestCase):
    def test_each_sealed_worker_has_an_exact_controller_scope(self):
        images = []
        for engine in ("v12", "v13"):
            with self.subTest(engine=engine):
                env = controller_environment(engine)
                self.assertNotIn("AONOHAKO_DURANTE_JUDGE_AUTO", env)
                self.assertEqual(json.loads(env[REGISTRY]), {"schema": 2, "cases": []})
                self.assertEqual(env["AONOHAKO_DURANTE_JUDGE_MODE"], "on")
                images.append(env["AONOHAKO_DURANTE_RUNTIME_IMAGE"])
        self.assertNotEqual(images[0], images[1])


if __name__ == "__main__":
    unittest.main()
