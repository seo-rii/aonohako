"""Audit the live baseline/candidate comparison and apply its prospective release gates."""
import argparse
import base64
from collections import Counter, defaultdict
import json
from pathlib import Path
import sys

# Keep this program importable as refit/verify.py even when the original
# pilot verifier is already on sys.path.
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import freeze
from freeze import pilot

ARMS = ("native", "baseline", "candidate")
CONTROLS = ("low", "high")
FREEZE_SHA = "65239f86426584c275a44b15038324dfaeeeb34ce8ebaebda2d43275aeddd862"


def schedule(frozen, held):
    original = [("contest", r) for r in ("fast", "slow", "wrong")] + [("stress","fast"), ("low","reference"), ("high","reference")]
    problems = pilot.corpus.PROBLEMS
    for rep in range(1,4):
        for i in range(len(original)):
            boundary, role = original[(i + rep - 1) % len(original)]
            for j in range(3):
                problem = problems[(j + rep + i - 1) % 3]
                kinds = ("native", "candidate") if (j + rep + i) % 2 else ("candidate", "native")
                limit = frozen["candidate"]["cases"][problem][boundary + "_ms"]
                for arm in kinds:
                    if rep <= 2 or arm == "native":
                        yield "original", problem, problem, role, boundary, arm, rep, limit
    for rep in range(1,4):
        for b, boundary in enumerate(CONTROLS):
            for i in range(len(held)):
                case = held[(i + 3*(rep-1) + b) % len(held)]
                start = (rep+i+b) % len(ARMS)
                for arm in ARMS[start:] + ARMS[:start]:
                    if rep <= 2 or arm == "native":
                        limit = frozen["deadlines"][case["case_id"]][boundary+"_ms"]
                        yield "held_out", case["case_id"], case["problem"], case["role"], boundary, arm, rep, limit


def tight_summary(groups):
    totals = {arm: Counter() for arm in ARMS[1:]}
    matched = {arm: Counter() for arm in ARMS[1:]}
    details = []
    for (phase,case,boundary), samples in sorted(groups.items()):
        pilot.require({arm:len(rows) for arm,rows in samples.items()} == {"native":3,"baseline":2,"candidate":2},
                      "missing tight comparison arm or repeat")
        votes = {arm:sum(row["receipt"]["status"] == pilot.TLE for row in rows) for arm,rows in samples.items()}
        expected = votes["native"] == 3
        labels = {}
        for arm in ARMS[1:]:
            count = totals[arm]
            count["groups"] += 1
            count["mixed_native"] += 0 < votes["native"] < 3
            count["mixed_judge"] += votes[arm] == 1
            if votes["native"] == 1 or votes["native"] == 2 or votes[arm] == 1:
                continue
            actual = votes[arm] == 2
            label = "agreement" if actual == expected else ("false_timeout" if actual else "false_pass")
            labels[arm] = label
            count["comparable"] += 1
            count[label] += 1
        if len(labels) == 2:
            for arm, label in labels.items():
                matched[arm]["comparable"] += 1
                matched[arm][label] += 1
        details.append(dict(guest=phase, case_id=case, boundary=boundary, tle_votes=votes))
    def counts(counter, keys):
        return {k:counter[k] for k in keys}
    metrics = ("comparable","agreement","false_timeout","false_pass")
    return dict(all_groups={a:counts(c, ("groups","mixed_native","mixed_judge",*metrics)) for a,c in totals.items()},
                paired_unanimous={a:counts(c,metrics) for a,c in matched.items()}, comparisons=details)


def release_gates(original, previously, tight):
    old_opp = previously["reference_near_limits"].get("unanimous_opposition", 0)
    new_opp = original["reference_near_limits"].get("unanimous_opposition", 0)
    all_base, all_candidate = (tight["all_groups"][key] for key in ARMS[1:])
    baseline,candidate = (tight["paired_unanimous"][key] for key in ARMS[1:])
    old_errors = baseline["false_timeout"] + baseline["false_pass"]
    new_errors = candidate["false_timeout"] + candidate["false_pass"]
    return {
        "original_contest_and_stress": original["qualified_contest_cases"] == previously["qualified_contest_cases"] == 3,
        "no_more_reference_boundary_opposition": new_opp <= old_opp,
        "fewer_paired_tight_errors": new_errors < old_errors,
        "no_more_false_timeouts": candidate["false_timeout"] <= baseline["false_timeout"],
        "no_more_false_passes": candidate["false_pass"] <= baseline["false_pass"],
        "no_more_mixed_judge_groups": all_candidate["mixed_judge"] <= all_base["mixed_judge"],
        "at_least_as_many_comparable_groups": all_candidate["comparable"] >= all_base["comparable"],
    }


def request_digest(payload, original, case, limit, kind, clock):
    directory = payload / "pilot/corpus"
    binary = (directory / case["binary"]).read_bytes()
    pilot.require(pilot.sha(binary) == case["binary_sha256"], "wrong payload ELF")
    if original:
        inp = (directory / (case["problem"] + ".stdin")).read_text()
        out = (directory / (case["problem"] + ".stdout")).read_text()
    else:
        inp, out = case["stdin"], case["expected_stdout"]
    pilot.require(pilot.sha(inp.encode()) == case["stdin_sha256"] and pilot.sha(out.encode()) == case["stdout_sha256"], "wrong payload I/O")
    req = dict(problem_id=case["problem"], lang="CPP20", binaries=[dict(name="Main",mode="exec",data_b64=base64.b64encode(binary).decode())],
               stdin=inp,expected_stdout=out,limits={**pilot.NOMINAL,"time_ms":limit},emit_logs=True)
    if kind != "native":
        req.update(work_measurement=True,work_measurement_limits=pilot.HARD,work_measurement_time=True,work_measurement_judge=True,
                   work_measurement_judge_case_required=True,work_measurement_judge_clock_binding=clock["model_binding"])
    return pilot.sha(json.dumps(req).encode())


def read_guest(directory, frozen, held, original, reg, freeze_digest, hashes, payload):
    host = json.loads(pilot.evidence(directory, "platform.json"))
    phase = host["phase"]
    pilot.require(phase in ("primary","repeat") and host["affinity"] == [1] and host["cpu"] == 1 and
                  host["cpuset_cpus_effective"] == host["cpu1_thread_siblings"] == "1" and set(host["workers"]) == set(ARMS) and
                  all(pilot.positive(pid) for pid in host["workers"].values()) and len(set(host["workers"].values())) == 3
                  and host["freeze_sha256"] == freeze_digest and host["case_manifest_sha256"] == frozen["case_manifest_sha256"]
                  and host["plan_sha256"] == original["payload"]["plan.json"] and host["server_sha256"] == original["plan"]["server_sha256"]
                  and host["runtime_sha256"] == original["plan"]["runtime_sha256"] and 0 <= host["steal_ticks_before"] <= host["steal_ticks_after"],
                  "wrong guest placement, workload, clock or runtime")
    trace = pilot.evidence(directory, "exec-trace.txt")
    journal = pilot.evidence(directory, "trials.jsonl")
    rows = map(json.loads, journal.splitlines())
    held_dict = {c["case_id"]:c for c in held}
    wide, tight = {}, defaultdict(lambda:defaultdict(list))
    for seq,(planned,r,events) in enumerate(zip(schedule(frozen,held), rows, pilot.read_windows(trace), strict=True)):
        study,id_,problem,role,boundary,arm,rep,limit = planned
        pilot.require(r["sequence"] == seq and tuple(r[k] for k in ("study","case_id","problem","role","boundary","kind","rep","limit_ms")) == planned,
                      f"wrong slot or budget at {phase}/{seq}")
        is_original = study == "original"
        if is_original:
            d = original["corpus"][problem]
            case = dict(problem=problem,binary=f"{problem}-{role}", binary_sha256=d["elf_sha256"][role],
                        stdin_sha256=d["stdin_sha256"], stdout_sha256=d["stdout_sha256"])
        else:
            case = held_dict[id_]
        pilot.require(r["binary_sha256"] == case["binary_sha256"] == reg["payload"]["pilot/corpus/"+case["binary"]] and
                      r["stdin_sha256"] == case["stdin_sha256"] and r["expected_stdout_sha256"] == case["stdout_sha256"] and
                      pilot.sha(r["stdout"].encode()) == r["stdout_sha256"], "receipt file or stdout identity differs")
        key = (problem,role,case["stdin_sha256"],limit,arm)
        if key not in hashes:
            actual = request_digest(payload,is_original,case,limit,arm,frozen.get(arm)) if payload else r["request_sha256"]
            pilot.require(len(actual) == 64 and all(c in "0123456789abcdef" for c in actual) and actual not in hashes.values(), "invalid request digest")
            hashes[key] = actual
        pilot.require(hashes[key] == r["request_sha256"], "request bytes differ")
        pilot.check_placement(r, events)
        if arm == "native":
            pilot.check_native(r, case["stdout_sha256"])
        else:
            pilot.check_judge(r, frozen[arm], original["plan"], case["stdout_sha256"])
        if not is_original and r["receipt"]["status"] != pilot.TLE:
            pilot.require(r["receipt"]["status"] == case["expected_status"] and r["stdout_sha256"] == case["solver_stdout_sha256"],
                          "completed held-out answer differs from the independent oracle")
        if is_original:
            kind = "judge" if arm == "candidate" else "native"
            wide[problem,role,boundary,kind,rep] = {**r,"kind":kind}
        else:
            tight[phase,id_,boundary][arm].append(r)
    pilot.require(len(wide) == 90 and len(tight) == 24, "incomplete guest trial set")
    raw = dict(boot_sha256=host["boot_sha256"],trials_sha256=pilot.sha(journal),exec_trace_sha256=pilot.sha(trace))
    return phase, wide, tight, raw


def check_cloud(cloud, observations, source_cloud, registration):
    pilot.require(set(cloud) == set(observations) == {"primary","repeat"}, "missing a fresh validation guest")
    for phase, meta in cloud.items():
        pilot.require(meta["zone"] == "asia-northeast3-a" and meta["machine_type"] == "n2d-standard-4" and
                      meta["cpu_platform"] == "AMD Milan" and type(meta["threads_per_core"]) is int and meta["threads_per_core"] == 1
                      and meta["image"] == "debian-12-bookworm-v20261117" and meta["disk_auto_delete"] is True
                      and meta["external_ip"] is False and meta["service_account"] is False and
                      meta["cleanup_http"] == {"instance":404,"boot_disk":404}, "wrong GCE topology or incomplete cleanup")
        pilot.require(meta["trial_count"] == 258 and meta["freeze_sha256"] == FREEZE_SHA and meta["archive_sha256"] == registration["archive_sha256"]
                      and all(meta[k] == v for k,v in observations[phase].items()), "per-guest evidence digest mismatch")
    for key in ("boot_sha256","instance_id_sha256","disk_id_sha256"):
        digests = {m[key] for m in cloud.values()}
        pilot.require(len(digests) == 2 and all(len(d) == 64 and all(c in "0123456789abcdef" for c in d) for d in digests) and
                      digests.isdisjoint(m[key] for m in source_cloud), "guest or storage identity reused")


def verify(root=HERE, *, payload=None, feature_study=None, infrastructure=None):
    """Return the verified report. release.time_models and release.judge_cases select the operator configuration."""
    root = Path(root)
    previous = root.parent
    original_reg = json.loads((previous / "registration.json").read_bytes())
    original_bytes = (previous / "results.json").read_bytes()
    old = json.loads(original_bytes)
    raw = (root / "freeze.json").read_bytes()
    frozen = json.loads(raw)
    reg = json.loads((root / "registration.json").read_bytes())
    files = reg["payload"]
    pilot.require(reg["feature_study_commit"] == frozen["feature_commit"] == freeze.FEATURE_COMMIT and
                  pilot.sha(original_bytes) == frozen["pilot_results_sha256"] == freeze.ORIGINAL_RESULT and
                  pilot.sha(raw) == FREEZE_SHA == files["freeze.json"] and reg["pilot_registration_sha256"] ==
                  pilot.sha((previous / "registration.json").read_bytes()) and frozen["feature_results_sha256"] == freeze.FEATURE_RESULT,
                  "frozen clocks or sources changed")
    manifest = (root / "case_manifest.json").read_bytes()
    pilot.require(pilot.sha(manifest) == frozen["case_manifest_sha256"] == files["case_manifest.json"] and
                  all(pilot.sha((root/n).read_bytes()) == files[n] for n in ("guest.py","guest.sh")), "collector or manifest differs")
    held = [c for c in json.loads(manifest)["cases"] if c["split"] == "held_out"]
    pilot.require(len(held) == len(frozen["deadlines"]) == 12, "wrong held-out panel")
    source_cloud = list(json.loads((previous / "evidence/infrastructure.json").read_bytes()).values())
    if feature_study is not None:
        feature_study = Path(feature_study)
        pilot.require(freeze.build(feature_study) == frozen, "live policy differs from the reference-only refit")
        feature_meta = json.loads((feature_study / "evidence/infrastructure.json").read_bytes())
        pilot.require(feature_meta["boot_sha256"] == frozen["candidate"]["calibration_boot_sha256"], "different feature guest")
        source_cloud.append(feature_meta)
    by_guest, all_tight, observations, hashes = {}, {}, {}, {}
    for phase in ("primary","repeat"):
        guest, wide, tight, obs = read_guest(root / "evidence" / phase, frozen, held, original_reg, reg, FREEZE_SHA, hashes, payload)
        pilot.require(guest == phase, "primary and repeat guest phases differ from archive")
        observations[phase], by_guest[phase] = obs, wide
        all_tight.update(tight)
    new_boots = {r["boot_sha256"] for r in observations.values()}
    pilot.require(len(new_boots) == 2 and new_boots.isdisjoint(m["boot_sha256"] for m in source_cloud) and
                  frozen["candidate"]["calibration_boot_sha256"] not in new_boots, "reused calibration, baseline or validation boot")
    if infrastructure is not None:
        check_cloud(json.loads(Path(infrastructure).read_bytes()),observations,source_cloud,reg)
    original = pilot.summarize(by_guest, frozen["candidate"])
    tight = tight_summary(all_tight)
    pilot.require(len(all_tight) == 48, "incomplete tight comparison panel")
    gates = release_gates(original, old, tight)
    promoted = all(gates.values())
    selected = "candidate" if promoted else "baseline"
    clock = frozen[selected]
    release_cases = original["judge_cases"] if promoted else old["judge_cases"]
    pilot.require(len(release_cases["cases"]) == 3 and all(r["model_binding"] == clock["model_binding"] for r in release_cases["cases"]),
                  "release scope or selected clock binding differs")
    return dict(schema=1, validation_trials=516, trial_count_per_guest={"original":90,"held_out":168},
                freeze_sha256=FREEZE_SHA, pilot_results_sha256=freeze.ORIGINAL_RESULT, feature_results_sha256=freeze.FEATURE_RESULT,
                model_bindings={arm:frozen[arm]["model_binding"] for arm in ARMS[1:]},
                original_reference_baseline=old["reference_near_limits"], original_candidate=original,
                tight_controls=tight, gates=gates, promoted=promoted,
                release=dict(selected=selected,model_binding=clock["model_binding"],time_models=clock["time_models"],judge_cases=release_cases))


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--root", type=Path, default=HERE)
    cli.add_argument("--feature-study", type=Path, help="independently reconstruct the exact fit and thresholds from Durante #109")
    cli.add_argument("--payload", type=Path, help="independently reconstruct each full HTTP request")
    cli.add_argument("--infrastructure", type=Path, help="verify instance identities and GCE cleanup")
    cli.add_argument("--output", type=Path)
    args = cli.parse_args()
    result = verify(args.root,payload=args.payload,feature_study=args.feature_study,infrastructure=args.infrastructure)
    output = json.dumps(result, indent=2, sort_keys=True, allow_nan=False) + "\n"
    if args.output:
        args.output.write_text(output)
    else:
        print(output,end="")
