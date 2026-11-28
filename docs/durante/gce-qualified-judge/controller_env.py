"""Export the qualified pilot settings for a controller supporting judge-case schema 2."""
import argparse
import json
from pathlib import Path
import subprocess
import sys

from verify import NOMINAL

HERE = Path(__file__).resolve().parent
REGISTRY = "AONOHAKO_DURANTE_JUDGE_CASES"


def controller_environment(engine):
    # Each exporter replays the pilot and checks the recorded nominal limits
    # against NOMINAL before it releases the worker's three case bindings.
    if engine == "v12":
        exporter = [HERE / "operator_env.py"]
    elif engine == "v13":
        exporter = [HERE.parent / "gce-v13-judge-transfer" / "verify.py", "--operator-env"]
    else:
        raise ValueError("expected a v12 or v13 pilot worker")

    environment = json.loads(subprocess.check_output([sys.executable, "-B", *exporter]))
    registry = json.loads(environment[REGISTRY])
    if registry["schema"] != 1 or not registry["cases"]:
        raise ValueError("expected the sealed worker's schema-1 registry")
    limits = {name: NOMINAL[name] for name in ("memory_mb", "wall_time_ms")}
    environment[REGISTRY] = json.dumps({
        "schema": 2,
        "cases": [{**case, **limits} for case in registry["cases"]],
    })
    return environment


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--engine", choices=("v12", "v13"), required=True)
    args = parser.parse_args()
    print(json.dumps(controller_environment(args.engine), indent=2, sort_keys=True))
