"""Build the v12 exact-input challenge from two independently registered GCE payloads."""
import argparse
import base64
import gzip
import hashlib
import json
from pathlib import Path
import shutil
import tarfile

HERE = Path(__file__).resolve().parent
V13 = HERE.parent
PILOT = V13.parent / "gce-qualified-judge"
TRANSFER = V13.parent / "gce-v13-judge-transfer"
ROLES = ("fast", "wrong", "slow", "hybrid1024", "sqrt1024")


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read(path):
    return json.loads(path.read_bytes())


def write(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def plan():
    source = read(V13 / "plan.json")
    model = read(TRANSFER / "registration.json")["plan"]["arms"]["v12"]
    cases = {}
    for role in ROLES:
        key = "original-" + role
        case = source["cases"][key]
        limits = dict(contest=203)
        if case["split"] == "variant":
            limits["ample"] = 30_000
        cases[key] = dict(case, limits=limits)
    return dict(schema=1, image=model["image"], baseline_binding=model["binding"],
                original_input_sha256=source["original_input_sha256"], cases=cases,
                decision="All native and metered controls must reproduce fast AC, wrong WA and live slow TLE, "
                         "and both ample variants must return their exact AC answer. A unanimous false pass "
                         "or false timeout on the same variant at 203 ms on both fresh boots disqualifies "
                         "the original inversion input from v12 automatic clock routing.")


def request_hashes(design, payload):
    result = {}
    nominal = dict(time_ms=30000, wall_time_ms=60000, memory_mb=768, output_bytes=8 << 20)
    hard = dict(cpu_time_ms=120000, wall_time_ms=170000, memory_mb=1024)
    for key, case in design["cases"].items():
        binary = (payload / case["binary"]).read_bytes()
        if sha(binary) != case["binary_sha256"]:
            raise ValueError("different submission ELF")
        artifact = dict(name="Main", mode="exec", data_b64=base64.b64encode(binary).decode())
        for boundary, ms in case["limits"].items():
            request = dict(problem_id=case["problem"], lang="CPP20", binaries=[artifact], stdin=case["stdin"],
                           expected_stdout=case["expected_stdout"], limits={**nominal, "time_ms": ms}, emit_logs=True)
            result[f"{key}/{boundary}/native"] = sha(json.dumps(request).encode())
            request.update(work_measurement=True, work_measurement_limits=hard, work_measurement_time=True,
                           work_measurement_judge=True, work_measurement_judge_case_required=True,
                           work_measurement_judge_clock_binding=design["baseline_binding"])
            result[f"{key}/{boundary}/shared"] = sha(json.dumps(request).encode())
    return result


def prepare(exact, image, out):
    out.mkdir(parents=True, exist_ok=True)
    payload = out / "payload"
    payload.mkdir()
    previous = read(V13 / "registration.json")["payload"]
    design = plan()
    files = read(PILOT / "registration.json")["plan"]["installed_files"]
    policy = read(PILOT / "refit/freeze.json")["candidate"]["policy"]
    write(payload / "plan.json", design)
    write(payload / "installation.json", dict(files=files, policy=policy, model_binding=design["baseline_binding"]))
    shutil.copyfile(HERE / "guest.py", payload / "guest.py")
    shutil.copyfile(image, payload / "runtime.squashfs")
    if "sha256:" + sha((payload / "runtime.squashfs").read_bytes()) != design["image"]:
        raise ValueError("expected the sealed v12 runtime")
    for name in ("guest.py", "guest.sh", "inversion_guest.py", "plan.py", "primary-thresholds.json",
                 "pilot/guest.py", "pilot/guest.sh", "pilot/v12_guest.py", "pilot/scripts/verify_durante_observation.py",
                 "pilot/scripts/verify_durante_native_boundaries.py", *(c["binary"] for c in design["cases"].values())):
        target = payload / ("fixed_guest.py" if name == "guest.py" else name)
        target.parent.mkdir(exist_ok=True, parents=True)
        shutil.copyfile(exact / name, target)
        if sha(target.read_bytes()) != previous[name]:
            raise ValueError("different authenticated parent payload member " + name)
    manifest = {}
    archive = out / "payload.tar.gz"
    with archive.open("xb") as raw, gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as zipped:
        with tarfile.open(fileobj=zipped, mode="w|", format=tarfile.USTAR_FORMAT) as tar:
            for source in sorted(p for p in payload.rglob("*") if p.is_file()):
                name = source.relative_to(payload).as_posix()
                manifest[name] = sha(source.read_bytes())
                info = tarfile.TarInfo(name)
                info.mode = 0o755 if name.endswith(".sh") or name.startswith("pilot/corpus/") else 0o644
                info.size = source.stat().st_size
                with source.open("rb") as stream:
                    tar.addfile(info, stream)
    write(out / "registration.json", dict(schema=1, payload=manifest, archive_sha256=sha(archive.read_bytes()),
                                         expected_rows={"clock":35}, requests=request_hashes(design, payload)))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--exact-payload", type=Path, required=True)
    parser.add_argument("--v12-image", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    prepare(args.exact_payload, args.v12_image, args.out)
