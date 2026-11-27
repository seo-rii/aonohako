"""Build the static judge-pilot solvers and generate the seeded inputs and answers."""
import argparse
from collections import deque
from functools import cache
import hashlib
import json
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent
PROBLEMS = ("range_min", "shortest_paths", "inversions")
ROLES = ("reference", "fast", "slow", "wrong")
# A short input defines a large, reproducible problem instance via xorshift32.
INPUTS = {
    "range_min": (65536, 480000, 256, 4096, 1878167739),
    "shortest_paths": (22000, 175000, 31, 303217),
    "inversions": (256, 3072, 4096, 658417),
}
SMALL = {
    "range_min": (123, 197, 1, 46, 1878167739),
    "shortest_paths": (61, 131, 15, 303217),
    "inversions": (3, 52, 19, 658417),
}
FLAGS = ("-std=c++20", "-O2", "-static", "-s", "-march=x86-64-v2", "-Wl,--build-id=none")


def numbers(seed):
    while True:
        seed ^= (seed << 13) & 0xffffffff
        seed ^= seed >> 17
        seed ^= (seed << 5) & 0xffffffff
        yield seed


def range_min(n, queries, min_width, max_width, seed):
    """Offline range minima with a monotonic stack and disjoint-set successor links."""
    rng = numbers(seed)
    values = [1 + next(rng) % 1000003 for _ in range(n)]
    ending = [[] for _ in range(n)]
    for _ in range(queries):
        width = min_width + next(rng) % (max_width - min_width + 1)
        left = next(rng) % (n - width + 1)
        ending[left + width - 1].append(left)
    parent = list(range(n))
    stack = []
    answer = 0
    for right, value in enumerate(values):
        while stack and value < values[stack[-1]]:
            parent[stack.pop()] = right
        stack.append(right)
        for i in ending[right]:
            while parent[i] != i:
                parent[i] = parent[parent[i]]
                i = parent[i]
            answer += values[i]
    return answer


def shortest_paths(n, extra_edges, max_cost, seed):
    """Queue-based distance relaxation until no distance can decrease."""
    rng = numbers(seed)
    graph = [[((i + 1) % n, 1 + next(rng) % max_cost)] for i in range(n)]
    for _ in range(extra_edges):
        u = next(rng) % n
        v = (u + 1 + next(rng) % (n - 1)) % n
        graph[u].append((v, 1 + next(rng) % max_cost))
    distance = [n * max_cost + 1] * n
    distance[0] = 0
    queue = deque([0])
    queued = {0}
    while queue:
        u = queue.popleft()
        queued.remove(u)
        for v, cost in graph[u]:
            candidate = distance[u] + cost
            if candidate < distance[v]:
                distance[v] = candidate
                if v not in queued:
                    queue.append(v)
                    queued.add(v)
    return sum(distance)


def inversions(batches, n, alphabet, seed):
    """Each inverted pair is counted at its most significant differing bit."""
    rng = numbers(seed)
    result = 0
    for _ in range(batches):
        groups = [[next(rng) % alphabet for _ in range(n)]]
        for bit in reversed(range((alphabet - 1).bit_length())):
            next_groups = []
            for group in groups:
                ones, zeros = [], []
                for key in group:
                    if key & (1 << bit):
                        ones.append(key)
                    else:
                        result += len(ones)
                        zeros.append(key)
                next_groups.extend(part for part in (ones, zeros) if len(part) > 1)
            groups = next_groups
    return result


ORACLES = {"range_min": range_min, "shortest_paths": shortest_paths, "inversions": inversions}


@cache
def case(problem, small=False):
    parameters = (SMALL if small else INPUTS)[problem]
    stdin = " ".join(map(str, parameters)) + "\n"
    return stdin.encode("ascii"), f"{ORACLES[problem](*parameters)}\n".encode("ascii")


def sha(data):
    return hashlib.sha256(data).hexdigest()


def build(output_dir, compiler="g++"):
    """Compile the three correct algorithms and one incorrect algorithm per problem."""
    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    binaries = {}
    for problem in PROBLEMS:
        binaries[problem] = {}
        for index, role in enumerate(ROLES, start=1):
            binary = output_dir / f"{problem}-{role}"
            subprocess.run([compiler, *FLAGS, f"-DSOLUTION={index}", HERE / "corpus" / f"{problem}.cpp",
                            "-o", binary], check=True)
            binaries[problem][role] = binary
    return binaries


def prepare(out, compiler):
    paths = build(out, compiler)
    manifest = {}
    for problem, binaries in paths.items():
        stdin, expected = case(problem)
        (out / f"{problem}.stdin").write_bytes(stdin)
        (out / f"{problem}.stdout").write_bytes(expected)
        manifest[problem] = {
            "stdin_sha256": sha(stdin),
            "stdout_sha256": sha(expected),
            "elf_sha256": {role: sha(path.read_bytes()) for role, path in binaries.items()},
        }
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--compiler", default="g++")
    args = parser.parse_args()
    prepare(args.output, args.compiler)
