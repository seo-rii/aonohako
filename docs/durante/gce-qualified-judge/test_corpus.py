"""Verify all twelve solver binaries on small instances before registering the pilot."""
from pathlib import Path
import subprocess
from tempfile import TemporaryDirectory
import unittest

import corpus


class JudgeCorpus(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        tmp = TemporaryDirectory()
        cls.addClassCleanup(tmp.cleanup)
        cls.binaries = corpus.build(Path(tmp.name))

    def test_algorithms_and_wrong_answer_controls(self):
        for problem in corpus.PROBLEMS:
            # Keep pre-registration tests small. The larger inputs are reserved for the guest.
            small = corpus.SMALL[problem]
            for seed in (small[-1], 51793, 141231):
                parameters = (*small[:-1], seed)
                raw = (" ".join(map(str, parameters)) + "\n").encode()
                expected = f"{corpus.ORACLES[problem](*parameters)}\n".encode()
                with self.subTest(problem=problem, seed=seed):
                    for role, path in self.binaries[problem].items():
                        result = subprocess.run([path], input=raw, capture_output=True, check=True)
                        self.assertEqual(result.stderr, b"")
                        if role == "wrong":
                            self.assertNotEqual(result.stdout, expected)
                        else:
                            self.assertEqual(result.stdout, expected)

    def test_generator_and_small_case_encoding(self):
        rng = corpus.numbers(1)
        self.assertEqual([next(rng) for _ in range(3)], [270369, 67634689, 2647435461])
        for name, parameters in corpus.SMALL.items():
            self.assertEqual(corpus.case(name, True)[0], (" ".join(map(str, parameters)) + "\n").encode())
