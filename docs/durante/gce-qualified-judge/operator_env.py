"""Print the verified judging settings for the exact pinned pilot worker as a JSON environment map."""
import json
from pathlib import Path

import verify
from refit.verify import verify as verify_refit

ROOT = Path(__file__).resolve().parent


def operator_env():
    evidence = ROOT / "evidence"
    registration = json.loads((ROOT / "registration.json").read_bytes())
    results = verify.verify(registration, evidence / "calibrate", [evidence / "primary", evidence / "repeat"],
                            infrastructure=json.loads((evidence / "infrastructure.json").read_bytes()))
    if results != json.loads((ROOT / "results.json").read_bytes()) or not results["judge_cases"]["cases"]:
        raise ValueError("published results do not identify an eligible case")
    refit = ROOT / "refit"
    comparison = verify_refit(refit, infrastructure=refit / "evidence/infrastructure.json")
    if comparison != json.loads((refit / "results.json").read_bytes()):
        raise ValueError("published model comparison differs from the verified live trials")
    selected = comparison["release"]
    frozen = json.loads((evidence / "calibrate/frozen.json").read_bytes())
    policy = frozen["policy"]
    image = registration["plan"]["runtime_image"]
    return {
        "AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT": image,
        "AONOHAKO_DURANTE_RUNTIME_IMAGE": image,
        "AONOHAKO_DURANTE_FAMILIES": "cpp",
        "AONOHAKO_DURANTE_CPUID_PROFILE": policy["cpuid_profile"],
        "AONOHAKO_DURANTE_DISPATCH_PROFILE": policy["dispatch_profile"],
        "AONOHAKO_DURANTE_LATENCY_BOUND": "off",
        "AONOHAKO_DURANTE_MEMORY_OPERAND_BYTES": "off",
        "AONOHAKO_DURANTE_RUNTIME_DETERMINISM": "off",
        "AONOHAKO_DURANTE_JUDGE_MODE": "on",
        "AONOHAKO_DURANTE_JUDGE_INTEGRITY": "off",
        "AONOHAKO_DURANTE_JUDGE_FAMILIES": "cpp",
        "AONOHAKO_DURANTE_RESOURCE_POLICY": json.dumps(
            {"max_limits": verify.HARD, "memory_budget_mb": 2048, "reserved_memory_mb": 512}),
        "AONOHAKO_MAX_ACTIVE_RUNS": "1",
        "AONOHAKO_WORK_ROOT_MAX_BYTES": str(256 << 20),
        "AONOHAKO_DURANTE_TIME_MODELS": json.dumps(selected["time_models"]),
        "AONOHAKO_DURANTE_JUDGE_CASES": json.dumps(selected["judge_cases"]),
    }


if __name__ == "__main__":
    print(json.dumps(operator_env(), indent=2))
