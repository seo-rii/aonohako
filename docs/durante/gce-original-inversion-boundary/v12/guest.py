"""Run the exact-input fixed schedule with the sealed v12 clock identity."""
import argparse
from pathlib import Path

from fixed_guest import collector

ROOT = Path(__file__).resolve().parent


def original_policy(plan, policy, arm):
    if (arm != "shared" or policy["runtime_id"] != "CPP20@" + plan["image"]
            or policy["engine_profile"] != "dynamorio-11.3-durante-v12-vm32m"):
        raise ValueError("expected the original v12 clock")
    return dict(policy)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("primary", "repeat"))
    parser.add_argument("--work-root", type=Path, required=True)
    parser.add_argument("--trace-dir", type=Path, required=True)
    parser.add_argument("--frozen", type=Path, required=True)
    args = parser.parse_args()
    if args.frozen.resolve() != ROOT / "plan.json":
        raise ValueError("unexpected frozen plan")
    study = collector(ROOT / "inversion_guest.py")
    study.clock_policy = original_policy
    study.collect(args.phase, args.work_root, args.trace_dir)
