"""Seal a two-image transfer without rebuilding or re-fitting the v12 pilot."""
import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

from protocol import (CASES_SHA, ENGINES, FREEZE_SHA, IDENTITY_FILES, ORIGINAL_IMAGE,
                      clock_for, digest, panel, read, require, write)

HERE = Path(__file__).resolve().parent
ORIGINAL = HERE.parent / "gce-qualified-judge"
REPO = HERE.parents[2]
import importlib.util
spec = importlib.util.spec_from_file_location("pilot_prepare", ORIGINAL / "prepare.py")
pilot_prepare = importlib.util.module_from_spec(spec)
sys.path.insert(0, str(ORIGINAL))
spec.loader.exec_module(pilot_prepare)
sys.path.insert(0, str(REPO / "scripts"))
from verify_durante_native_boundaries import model_binding


def prepare(pilot_payload, image, out, registration):
    original = read(ORIGINAL / "registration.json")
    held_file, freeze_file = (ORIGINAL / "refit" / name for name in ("case_manifest.json", "freeze.json"))
    require(digest(held_file.read_bytes()) == CASES_SHA and digest(freeze_file.read_bytes()) == FREEZE_SHA, "wrong inputs")
    out.mkdir()
    for name, expected in original["payload"].items():
        p = pilot_payload / name
        require(pilot_prepare.sha(p) == expected, f"wrong historical pilot file {name}")
        dest = out / "pilot" / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(p, dest)
    for p in (held_file, freeze_file, *(HERE/n for n in ("guest.py","guest.sh","protocol.py","latency_probe.py","counter_probe.S"))):
        shutil.copyfile(p, out / p.name)
    with tempfile.TemporaryDirectory(dir=out.parent) as tmp:
        obj = str(Path(tmp) / "probe.o")
        subprocess.run(["as", "--64", "-o", obj, str(out/"counter_probe.S")], check=True)
        subprocess.run(["ld", "-m", "elf_x86_64", "-static", "--build-id=none", "-e", "_start", "-o", str(out/"counter-probe"), obj], check=True)
    shutil.copyfile(image, out / "v13.squashfs")
    candidate_image = "sha256:"+pilot_prepare.sha(out / "v13.squashfs")
    require(candidate_image != ORIGINAL_IMAGE == original["plan"]["runtime_image"], "different images required")
    files = {}
    for name in IDENTITY_FILES:
        payload = subprocess.check_output(["unsquashfs", "-processors", "1", "-cat", str(image), name])
        files[name] = digest(payload)
        if name == "UPSTREAM.json":
            upstream = json.loads(payload)
            require(upstream["commit"] == ENGINES["v13"][1] and upstream["repository"] == "https://github.com/seo-rii/durante",
                    "candidate image has different source provenance")
        if name == "dynamorio/.durante-runtime.json":
            sdk = json.loads(payload)
            require(sdk["inputs"]["patch_sha256"] == upstream["files"]["toolkit/patches/dynamorio-vfork-lifetime.patch"]
                    and sdk["inputs"]["code_replay_patch_sha256"] == upstream["files"]["toolkit/patches/dynamorio-code-replay.patch"],
                    "candidate SDK does not contain the reviewed patches")
    old = {n: original["plan"]["installed_files"][n] for n in IDENTITY_FILES}
    require(all(files[n] != old[n] for n in ("UPSTREAM.json","server","reference","lib/libdurante.so",
                                               "dynamorio/lib64/release/libdynamorio.so")), "incomplete engine replacement")
    cells = panel(original["corpus"], read(held_file), read(freeze_file))
    plan = {"schema":1, "freeze_sha256":FREEZE_SHA, "cases_sha256":CASES_SHA,
            "arms":{k:dict(image=img, engine=ENGINES[k][0], upstream=ENGINES[k][1], files=f,
                           binding=clock_for(k,img,cells,read(freeze_file),model_binding)["model_binding"])
                    for k,img,f in (("v12",ORIGINAL_IMAGE,old),("v13",candidate_image,files))}}
    # Commit request hashes before collection, so public replay validates HTTP identity without publishing ELFs.
    sys.path.insert(0, str(out/"pilot"))
    import guest as prior
    plan["request_hashes"] = {}
    for arm in ENGINES:
        arm_plan = plan["arms"][arm]
        for key, c in cells.items():
            elf = (out/"pilot/corpus"/(c["problem"]+"-"+c["role"])).read_bytes()
            for kind in (("native","judge") if arm == "v12" else ("judge",)):
                req = prior.request_for(c["problem"], kind, c["time_ms"], c["stdin"].encode(), c["expected_stdout"].encode(),
                                        elf, arm_plan["binding"])
                plan["request_hashes"][arm+"/"+kind+"/"+key] = digest(json.dumps(req).encode())
        req = prior.request_for("transfer-counter-probe","judge",30000,b"",b"ok\n",(out/"counter-probe").read_bytes(),arm_plan["binding"])
        plan["request_hashes"][arm+"/probe"] = digest(json.dumps(req).encode())
    write(out / "plan.json", plan)
    archive = out.with_suffix(".tar.gz")
    pilot_prepare.make_archive(out, archive)
    write(registration, {"schema":1, "archive_sha256":pilot_prepare.sha(archive), "plan":plan,
                         "payload":{str(f.relative_to(out)):pilot_prepare.sha(f) for f in sorted(out.rglob("*")) if f.is_file()}})
    print(json.dumps({"archive":str(archive),"sha256":pilot_prepare.sha(archive),"registration":str(registration)}))


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--pilot-payload", type=Path, required=True)
    cli.add_argument("--v13-image", type=Path, required=True)
    cli.add_argument("--output", type=Path, required=True)
    cli.add_argument("--registration", type=Path, default=HERE/"registration.json")
    args=cli.parse_args()
    prepare(args.pilot_payload, args.v13_image, args.output, args.registration)
