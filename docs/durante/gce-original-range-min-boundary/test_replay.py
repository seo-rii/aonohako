import json
from pathlib import Path
import tempfile
import unittest

import replay


class RangeVerdictEvidenceTest(unittest.TestCase):
    def test_both_sealed_workers_reproduce_the_published_results(self):
        replay.check_four_boots()
        for engine in replay.VERSIONS:
            with self.subTest(engine=engine):
                path = replay.HERE / engine
                result = replay.verify(engine, path / "evidence")
                self.assertEqual(result, replay.read(path / "results.json"))
                self.assertTrue(result["controls_passed"])
                self.assertTrue(result["disqualified"])
                self.assertFalse(result["uniform_rescaling"]["overlap"])
                self.assertEqual(result["repeated_opposition"],
                                 [dict(case=k, direction="false_timeout") for k in ("cached4", "edges256", "edges512")])

    def test_wrong_registration_before_guest_run_is_rejected(self):
        for engine in replay.VERSIONS:
            root = replay.HERE / engine / "evidence"
            with self.subTest(engine=engine), tempfile.TemporaryDirectory() as temp:
                infra = replay.read(root / "infrastructure.json")
                infra["repeat"]["registration_commit"] = "0" * 40
                (Path(temp) / "infrastructure.json").write_text(json.dumps(infra))
                with self.assertRaisesRegex(ValueError, "incomplete GCE lifecycle"):
                    replay.verify(engine, Path(temp))


if __name__ == "__main__":
    unittest.main()
