"""Run the registered fixed-limit panel with the authenticated v13 collector."""
import argparse
import importlib.util
from pathlib import Path

ROOT = Path(__file__).resolve().parent


def schedule(plan, phase, thresholds):
    if thresholds or phase not in ("primary", "repeat"):
        raise ValueError("fixed limits on two registered boots required")
    panel = [(key, boundary, ms) for key, case in sorted(plan["cases"].items())
             for boundary, ms in sorted(case["limits"].items())]
    order = ("native", "shared")
    if phase == "repeat":
        panel.reverse()
        order = order[::-1]
    for rep in (1, 2, 3):
        cells = panel[rep - 1:] + panel[:rep - 1]
        for pos, (key, boundary, ms) in enumerate(cells):
            for arm in order if (pos + rep) % 2 == 0 else order[::-1]:
                if arm == "native" or rep < 3:
                    yield key, boundary, arm, rep, ms


def collector(source):
    spec = importlib.util.spec_from_file_location("inversion_guest", source)
    original = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(original)
    original.ARMS = ("shared",)
    original.prelude = lambda plan, phase: ()
    original.schedule = schedule
    return original


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("primary", "repeat"))
    parser.add_argument("--work-root", type=Path, required=True)
    parser.add_argument("--trace-dir", type=Path, required=True)
    parser.add_argument("--frozen", type=Path, required=True)
    args = parser.parse_args()
    if args.frozen.resolve() != (ROOT / "plan.json").resolve():
        raise ValueError("unexpected frozen plan")
    collector(ROOT / "inversion_guest.py").collect(args.phase, args.work_root, args.trace_dir)
