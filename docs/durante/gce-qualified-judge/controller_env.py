"""Export the qualified pilot settings for a controller supporting judge-case schema 2."""
import argparse
import json
from pathlib import Path
import subprocess
import sys

HERE = Path(__file__).resolve().parent
REGISTRY = "AONOHAKO_DURANTE_JUDGE_CASES"


def controller_environment(engine):
    # Replay the sealed worker configuration before exporting its matching controller settings.
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
    # Every historical pilot case has a repeated native/clock disagreement.
    # Keep an explicit scope: omitting this key would unrestrict explicit judging.
    environment[REGISTRY] = json.dumps({"schema": 2, "cases": []})
    return environment


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--engine", choices=("v12", "v13"), required=True)
    args = parser.parse_args()
    print(json.dumps(controller_environment(args.engine), indent=2, sort_keys=True))
