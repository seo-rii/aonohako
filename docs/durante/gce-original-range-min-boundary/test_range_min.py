import itertools
from pathlib import Path
import subprocess
import tempfile
import unittest

import prepare


class BlockRangeMinimumTest(unittest.TestCase):
    def test_both_query_paths_against_offline_oracle(self):
        with tempfile.TemporaryDirectory() as temporary:
            binary = Path(temporary) / "Main"
            for cache in (0, 1):
                subprocess.run(["g++", "-std=c++20", "-O2", "-DBLOCK_SIZE=4",
                                f"-DCACHED_EDGES={cache}", prepare.HERE / "variants.cpp", "-o", binary], check=True)
                for n in (1, 4, 7, 12):
                    for low, high in itertools.combinations_with_replacement(range(1, n + 1), 2):
                        args = (n, 27, low, high, 161801)
                        data = " ".join(map(str, args)).encode() + b"\n"
                        actual = int(subprocess.check_output([binary], input=data))
                        self.assertEqual(actual, prepare.corpus.range_min(*args), (cache, args))


if __name__ == "__main__":
    unittest.main()
