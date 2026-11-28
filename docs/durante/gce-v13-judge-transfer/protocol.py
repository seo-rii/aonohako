"""Frozen program panel and prospective comparison for the opt-in v13 worker."""
from copy import deepcopy
import hashlib
import json
from pathlib import Path

ENGINES = {
    "v12": ("dynamorio-11.3-durante-v12-vm32m", "b43685c76b94a365886031359b032899b009cd1b"),
    "v13": ("dynamorio-11.3-durante-v13-vm32m", "6b4d74f248235f8f616aa5b8e151ca0db5a029de"),
}
ORIGINAL_IMAGE = "sha256:170190aab6b32aac533ee8d34a6f0a348b06158a0f88ec337457bdf68d66044c"
ORIGINAL_BINDING = "work-clock-v1:sha256:c1d4c0c8dcb2e106efd69ccbac0ea56f2ff4efe4f79ddb108454f5f4bcc676b0"
FREEZE_SHA = "65239f86426584c275a44b15038324dfaeeeb34ce8ebaebda2d43275aeddd862"
CASES_SHA = "30e57d6ee28ef1b857e25948fa280abf3f914476b29cd7682221944de32e9e4c"
WIDE_MS = 30000
TIMES = {"range_min": 183, "shortest_paths": 65, "inversions": 203}
EXPECTED = {"fast": "Accepted", "reference": "Accepted", "wrong": "Wrong Answer", "slow": "Time Limit Exceeded"}
IDENTITY_FILES = ("UPSTREAM.json", "server", "reference", "dynamorio/.durante-runtime.json",
                  "dynamorio/bin64/drrun", "dynamorio/lib64/release/libdynamorio.so", "lib/libdurante.so")


def require(test, message):
    if not test:
        raise ValueError(message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read(path):
    return json.loads(Path(path).read_bytes())


def write(path, obj):
    path.write_text(json.dumps(obj, indent=2, sort_keys=True, allow_nan=False) + "\n")


def panel(corpus, held, frozen):
    result = {}
    def add(cell, problem, role, boundary, ms, stdin, oracle, exact=None):
        manifest = corpus[problem]
        result[cell] = dict(problem=problem, role=role, boundary=boundary, time_ms=ms,
                            stdin=stdin, expected_stdout=oracle, binary_sha256=manifest["elf_sha256"][role],
                            expected_solver_stdout_sha256=exact)
    for problem, ms in TIMES.items():
        limits = frozen["candidate"]["cases"][problem]
        require(limits["contest_ms"] == ms and limits["stress_ms"] == 1, "original deadlines changed")
        reference, = [c for c in held["cases"] if c["split"] == "calibration" and c["problem"] == problem]
        for boundary, roles in (("contest", ("fast", "wrong", "slow")), ("stress", ("fast",)),
                                ("low", ("reference",)), ("high", ("reference",))):
            for role in roles:
                add(f"{problem}-{role}-{boundary}", problem, role, boundary, limits[boundary+"_ms"],
                    reference["stdin"], reference["expected_stdout"])
    for case in held["cases"]:
        if case["split"] != "held_out":
            continue
        for boundary in ("low", "high", "ample"):
            ms = WIDE_MS if boundary == "ample" else frozen["deadlines"][case["case_id"]][boundary+"_ms"]
            add(case["case_id"]+"-"+boundary, case["problem"], case["role"], "held-"+boundary, ms,
                case["stdin"], case["expected_stdout"], case["solver_stdout_sha256"])
    require(len(result) == 54, "incomplete registered panel")
    return result


def clock_for(arm, image, cells, frozen, binding_fn):
    clock = deepcopy(frozen["candidate"])
    policy = clock["policy"]
    profile, = clock["time_models"]["profiles"]
    require(policy["runtime_id"] == "CPP20@"+ORIGINAL_IMAGE and clock["model_binding"] == ORIGINAL_BINDING and
            (policy["instruction_ns_numerator"], policy["rep_byte_ns_numerator"], policy["ns_denominator"]) ==
            (1139371945900000, 1139371945900000, 4752214584908649), "unrecognized frozen clock")
    policy.update(runtime_id="CPP20@"+image, engine_profile=ENGINES[arm][0])
    profile.update(runtime_image=image, engine_profile=ENGINES[arm][0])
    binding = binding_fn(policy, clock["runtime_options_id"])
    require((binding == ORIGINAL_BINDING) == (arm == "v12"), "engine change did not segregate the clock")
    clock["model_binding"] = binding
    scopes = sorted({(c["problem"], digest(c["stdin"].encode()), c["time_ms"]) for c in cells.values()} |
                    {("transfer-counter-probe", digest(b""), WIDE_MS)})
    clock["judge_cases"] = {"schema":1, "cases":[dict(problem_id=p, source_language="CPP20", stdin_sha256=i,
                                                       time_ms=ms, model_binding=binding) for p,i,ms in scopes]}
    return clock


def schedule(arm, phase, cells):
    keys = sorted(cells)
    if phase == "repeat":
        keys.reverse()
    for rep in (1, 2, 3):
        for i, key in enumerate(keys[rep:] + keys[:rep]):
            kinds = ["judge", "native"] if (i+rep) % 2 else ["native", "judge"]
            for kind in kinds:
                if (kind == "native" and arm == "v12") or (kind == "judge" and rep < 3):
                    yield key, kind, rep
