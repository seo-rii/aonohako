"""Produce frozen CPP20 study inputs, independently computed outputs and content hashes."""

import argparse
import hashlib
import json
from pathlib import Path

MASK64 = (1 << 64) - 1
IMAGE = "sha256:5f1fee5fb589c9494f2155478bab0e01af620f73e99ebf2ff9177a09626d5bf3"
FLAGS = ["g++", "Main.cpp", "-o", "Main", "-O2", "-Wall", "-lm", "--static",
         "-pipe", "-DONLINE_JUDGE=1", "-std=c++20"]
BASE = "9e821fe977e92ee02ba9efdca05054d092279d7a"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def affine(n, seed, a, c, modulus):
    # Compose powers of the map to jump n states without following pointers.
    while n:
        if n & 1:
            seed = (a * seed + c) % modulus
        a, c = a * a % modulus, (a + 1) * c % modulus
        n >>= 1
    return seed


def copy_checksum(size, repeats):
    pattern = [(i * 131 + 17) % 256 for i in range(256)]
    blocks = size // len(pattern)
    weighted = blocks * sum((i + 1) * v for i, v in enumerate(pattern))
    weighted += 256 * sum(pattern) * blocks * (blocks - 1) // 2
    last = repeats - 1
    weighted += ((last & 255) ^ 17) - pattern[0]
    weighted += size * ((((last >> 8) & 255) ^ 91) - pattern[-1])
    for r in range(repeats):
        offset = (r * 4099) % size
        sample = pattern[offset % 256]
        if offset == 0:
            sample = (r & 255) ^ 17
        elif offset == size - 1:
            sample = ((r >> 8) & 255) ^ 91
        weighted += (r + 1) * sample
    return weighted


def fill_checksum(size, repeats):
    values = [(r * 73 + 19) % 256 for r in range(repeats)]
    return sum((r + 1) * v for r, v in enumerate(values)) + values[-1] * size * (size + 1) // 2


def scan_checksum(n, passes):
    # Recover the original rank of each screen position by inverting the two odd
    # multipliers and xor-shifts. No branch table or forward shuffle is needed.
    mask = n - 1
    inv1, inv2 = pow(5077, -1, n), pow(193489, -1, n)

    def unxor(x, shift):
        while (1 << shift) < n:
            x ^= x >> shift
            shift *= 2
        return x

    # XOR with p permutes each passes-sized block when the repeat count is a power
    # of two. Sum the independent path values over each block just once.
    assert passes > 0 and not (passes & (passes - 1)) and n % passes == 0
    total = 0
    for base in range(0, n, passes):
        on, one_sum, zero_sum = 0, 0, 0
        for i in range(base, base + passes):
            rank = unxor(unxor(unxor(i, 5) * inv2 & mask, 7) * inv1 & mask, 9)
            on += rank >= n // 2
            one_sum += i * (i + 3) + 17
            zero_sum += (i * 37 + 11) ^ 0x5a5a
        total += on * one_sum + (passes - on) * zero_sum
    return total


def workloads():
    training = "train"
    heldout = "heldout"
    branch_cycle = sum(x * x + 11 if x < 21845 else x**3 + 17 if x < 43690
                       else (x ^ 0x5A5A) + 23 for x in range(65536))
    for s in (1, 2, 4):
        n, seed = (32 << 20) * s, 0x0123456789abcdef
        yield "integer", training, s, [n, seed], affine(n, seed, 6364136223846793005, 1442695040888963407, 1 << 64)
        size, repeats = 4 << 20, 512 * s
        yield "rep_copy", training, s, [size, repeats], copy_checksum(size, repeats)
        n = (16 << 20) * s
        yield "branch", heldout, s, [n, 0xACE1], branch_cycle * (n // 65536)
        n = (512 << 10) * s
        yield "sort", heldout, s, [n], n * (n + 1) * (2 * n + 1) // 6
        yield "rep_fill", heldout, s, [size, repeats], fill_checksum(size, repeats)
    for s in (1, 2):
        active, steps = (8192 if s == 1 else 1 << 24), (1 << 21) + 421
        yield "cycle_walk", heldout, s, [active, steps], affine(steps, 713, 1664525, 1013904223, active)
        n, prime = (1 << 18) * s, 1000003
        checksum = sum((i + 1) * pow(1 + (i * 8191 + 104729) % (prime - 1), -1, prime) for i in range(n))
        yield "mod_inverse", heldout, s, [n], checksum
        n, passes = 1 << 20, 8 * s
        yield "branch_scan", heldout, s, [n, passes], scan_checksum(n, passes)


def write_manifests(root):
    cases = []
    for program, split, scale, values, result in workloads():
        stdin = " ".join(map(str, values)) + "\n"
        stdout = str(result & MASK64) + "\n"
        cases.append({
            "case_id": f"{program}/{scale}", "program": program, "split": split, "scale": scale,
            "stdin": stdin, "stdout": stdout, "input_sha256": digest(stdin.encode()),
            "expected_sha256": digest(stdout.encode()),
            "source_sha256": digest((root / "sources" / f"{program}.cpp").read_bytes()),
            "binary_sha256": digest((root / "bin" / program).read_bytes()),
        })
    (root / "cases.json").write_text(json.dumps(cases, indent=2) + "\n")
    (root / "build.json").write_text(json.dumps({
        "format": 1, "aonohako_base": BASE, "compile_image_id": IMAGE, "compiler_argv": FLAGS,
        "source_name_inside_build": "Main.cpp",
        "reference_programs": ["integer", "rep_copy", "branch", "sort", "rep_fill"],
        "reference_source_directory": "internal/durante/reference/fixtures",
        "new_programs": ["cycle_walk", "mod_inverse", "branch_scan"],
        "cases_sha256": digest((root / "cases.json").read_bytes()),
        "sources": {p.stem: digest(p.read_bytes()) for p in sorted((root / "sources").glob("*.cpp"))},
        "executables": {p.name: digest(p.read_bytes()) for p in sorted((root / "bin").iterdir())},
    }, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path, help="directory containing sources/<name>.cpp and bin/<name>")
    write_manifests(parser.parse_args().root)
