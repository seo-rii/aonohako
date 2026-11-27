"""Verify the CPU-pinned case-scoped judge pilot from its three guests' evidence."""
import argparse
import base64
from collections import Counter, defaultdict
from fractions import Fraction
import gzip
import hashlib
import json
from pathlib import Path
import re
from statistics import median
import sys

import corpus

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[2] / "scripts"))
from verify_durante_native_boundaries import CPU_SOURCES, model_binding

AC, WA, TLE = "Accepted", "Wrong Answer", "Time Limit Exceeded"
EXPECTED = {"fast": AC, "wrong": WA, "slow": TLE}
PROFILE = "dynamorio-11.3-durante-v12-vm32m"
MILLION = 1_000_000
HARD = {"cpu_time_ms": 120000, "wall_time_ms": 170000, "memory_mb": 1024}
NOMINAL = {"time_ms": 30000, "wall_time_ms": 60000, "memory_mb": 768, "output_bytes": 8 << 20}
TRACE = re.compile(r"\[(\d+)\].*?sched_process_exec: filename=(.*?) pid=(\d+) old_pid=")


def require(ok, message):
    if not ok:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def positive(value):
    return type(value) is int and value > 0


def evidence(root, name):
    path = root / name
    if path.is_file():
        require(not (root / (name + ".gz")).exists(), f"ambiguous evidence file {name}")
        return path.read_bytes()
    return gzip.decompress((root / (name + ".gz")).read_bytes())


def expected_trials(calibration):
    expected = set()
    for problem in corpus.PROBLEMS:
        groups = [("calibration", "reference", {"native": 5, "raw": 3})] if calibration else [
            (boundary, role, {"native": 3, "judge": 2})
            for boundary, roles in (("contest", EXPECTED), ("stress", ("fast",)),
                                    ("low", ("reference",)), ("high", ("reference",)))
            for role in roles]
        for boundary, role, kinds in groups:
            for kind, repeats in kinds.items():
                expected.update((problem, role, boundary, kind, i) for i in range(1, repeats + 1))
    return expected


def read_windows(data):
    windows = []
    for line in data.decode().splitlines():
        if line.startswith("# trial "):
            require(line == f"# trial {len(windows)}", "out-of-order trace window")
            windows.append([])
        elif line.strip():
            m = TRACE.search(line)
            require(windows and m, "invalid kernel exec event")
            cpu, path, pid = m.groups()
            windows[-1].append(dict(cpu=int(cpu), path=path, pid=int(pid)))
    return windows


def check_placement(row, window):
    require(row["execs"] == window and all(e["cpu"] == 1 and positive(e["pid"]) for e in window),
            "trial disagrees with CPU 1 kernel exec trace")
    if row["kind"] == "native":
        targets = [e for e in window if Path(e["path"]).name == "Main"]
        require(not any(Path(e["path"]).name == "drrun" for e in window), "native run started a measured launcher")
    else:
        targets = [e for e in window if e["path"] == "/opt/durante/dynamorio/bin64/drrun"]
    require(len(targets) == 1, "missing or duplicate CPU 1 target")


def index_trials(rows, windows, manifest, calibration):
    expected = expected_trials(calibration)
    require(len(rows) == len(windows) == len(expected), "incomplete trial or trace count")
    trials = {}
    for i, (r, window) in enumerate(zip(rows, windows, strict=True)):
        key = tuple(r[k] for k in ("problem", "role", "boundary", "kind", "rep"))
        require(key in expected and key not in trials and r["sequence"] == i and type(r["rep"]) is int,
                f"unexpected or duplicate trial at {i}: {key}")
        problem, role, *_ = key
        case = manifest[problem]
        require(r["binary_sha256"] == case["elf_sha256"][role] and r["stdin_sha256"] == case["stdin_sha256"] and
                re.fullmatch("[0-9a-f]{64}", r["request_sha256"]) and re.fullmatch("[0-9a-f]{64}", r["stdout_sha256"]),
                f"changed trial identity at {i}")
        check_placement(r, window)
        trials[key] = r
    require(trials.keys() == expected, "missing planned trial")
    return trials


def check_report(report, plan, complete):
    installation = report["installation"]
    files = plan["installed_files"]
    for field, path in (("launcher_sha256", "dynamorio/bin64/drrun"), ("client_sha256", "lib/libdurante.so"),
                        ("release_core_sha256", "dynamorio/lib64/release/libdynamorio.so")):
        require(installation[field] == files[path], "different Durante runtime installation")
    require(installation["unchanged_before_after"] is True and report["authority"] == "diagnostic_only" and
            report["integrity_verified"] is False and report["engine_profile"] == PROFILE and
            report["family"] == "cpp" and report["complete"] is complete, "wrong measurement contract")
    if complete:
        require(report["cpuid_profile"] == "x86-64-v2" and report["quality"] == "uninterrupted" and
                report["processes"] == report["images"] == 1 and report["signals_observed"] == 0 and
                report["unsupported_rep_entries"] == 0 and report["rep_telemetry_status"] == "movs_stos_only" and
                positive(report["instructions"]), "ineligible terminal counters")
        rep = report["rep_breakdown"]
        require(all(type(rep[k]) is int and rep[k] >= 0 for k in ("movs_bytes", "stos_bytes")) and
                report["rep_bytes"] == rep["movs_bytes"] + rep["stos_bytes"], "inconsistent REP counts")


def check_native(row, oracle):
    receipt = row["receipt"]
    status, ns, limit = receipt["status"], receipt["cpu_time_ns"], row["limit_ms"]
    require(positive(ns) and receipt["cpu_time_ms"] == -(-ns // MILLION) and
            not any(k.startswith("work_measurement") for k in receipt) and
            not receipt.get("cpu_time_normalization") and not receipt.get("cpu_time_sampling") and
            not receipt.get("stdout_truncated") and not receipt.get("stderr_truncated"), "invalid native CPU or output")
    accounting = receipt["cpu_accounting"]
    process = accounting["process_clock_peak_ns"]
    waited = accounting["wait_target_ns"]
    require(waited == accounting["wait_user_ns"] + accounting["wait_system_ns"] - accounting["rusage_baseline_ns"],
            "inconsistent native wait accounting")
    source = accounting["selected_source"]
    if source == "cgroup":
        require(ns >= max(process, waited), "cgroup CPU below target CPU")
    else:
        require(source in ("wait_rusage", "process_clock") and
                ns == (waited if source == "wait_rusage" else process) == max(waited, process), "invalid native CPU source")
    if status in (AC, WA):
        require(receipt["verdict_source"] == "stdout" and receipt.get("exit_code") == 0 and ns <= limit * MILLION and
                (row["stdout_sha256"] == oracle) == (status == AC), "native verdict or output mismatch")
    else:
        require(status == TLE and receipt["verdict_source"] in CPU_SOURCES and ns > limit * MILLION,
                "expected a native CPU time limit")
    return ns


def calibration_fit(trials, manifest, plan):
    cases, ratios = {}, []
    for problem in corpus.PROBLEMS:
        oracle = manifest[problem]["stdout_sha256"]
        samples, work = [], []
        for (p, _, _, kind, _), row in trials.items():
            if p != problem:
                continue
            receipt = row["receipt"]
            require(row["limit_ms"] == 30000 and row["stdout_sha256"] == oracle, "calibration input or output mismatch")
            if kind == "native":
                require(receipt["status"] == AC, "reference was not accepted")
                samples.append(check_native(row, oracle))
            else:
                env, report = receipt["work_measurement_execution"], receipt["work_measurement"]
                require(receipt["status"] == "Container Initialization Failed" and not receipt.get("work_measurement_judgment")
                        and receipt.get("exit_code") == 0 and env["state"] == "complete" and env["termination_source"] == "stdout"
                        and env["output_matches"] is True, "calibration must contain complete raw diagnostics")
                check_report(report, plan, True)
                clock, rep = env["clock"], report["rep_breakdown"]
                require(env["raw_limits"] == HARD and env["nominal_limits"] == NOMINAL and
                        clock == {"version": 1, "state": "observed", "metric": "instructions_plus_movs_stos_bytes_v1",
                                  "budget": (1 << 63) - 1, "progress": {"instructions": report["instructions"],
                                  "movs_bytes": rep["movs_bytes"], "stos_bytes": rep["stos_bytes"]}},
                        "calibration live and terminal counts disagree")
                work.append(report["instructions"] + report["rep_bytes"])
        require(len(samples) == 5 and len(work) == 3, "missing reference runs")
        n, w = median(samples), median(work)
        ratios.append(Fraction(n, w))
        cases[problem] = dict(native_median_ns=n, work_median=w, contest_ms=max(10, -(-4*n // MILLION)),
                              low_ms=max(1, 4*n // (5*MILLION)), high_ms=-(-6*n // (5*MILLION)), stress_ms=1)
    return median(ratios), cases


def check_registration(registration):
    plan, manifest, files = registration["plan"], registration["corpus"], registration["payload"]
    plan_bytes = (json.dumps(plan, indent=2, sort_keys=True) + "\n").encode()
    require(sha(plan_bytes) == files["plan.json"] and plan["runtime_image"] == "sha256:" + plan["runtime_sha256"]
            and plan["runtime_sha256"] == files["runtime.squashfs"] and
            all(plan[name + "_sha256"] == plan["installed_files"][name] for name in ("server", "reference")) and
            plan["go_build_flags"] == ["-tags", "durante_v12", "-trimpath", "-buildvcs=false"] and plan["cgo_enabled"] is False,
            "registered plan and runtime disagree")
    require(set(manifest) == set(corpus.PROBLEMS), "wrong registered problem set")
    distinct_binaries = set()
    for p, definition in manifest.items():
        stdin, expected = corpus.case(p)
        require(sha(stdin) == definition["stdin_sha256"] == files[f"corpus/{p}.stdin"] and
                sha(expected) == definition["stdout_sha256"] == files[f"corpus/{p}.stdout"], f"independent oracle differs for {p}")
        require(definition["elf_sha256"].keys() == set(corpus.ROLES), f"missing submissions for {p}")
        for role, digest in definition["elf_sha256"].items():
            require(digest == files[f"corpus/{p}-{role}"] and re.fullmatch("[a-f0-9]{64}", digest)
                    and digest not in distinct_binaries, "submission identity reused, missing or changed")
            distinct_binaries.add(digest)
    require(plan["manifest_sha256"] == files["corpus/manifest.json"], "wrong registered corpus manifest")
    for name, path in (("guest.py", HERE / "guest.py"), ("guest.sh", HERE / "guest.sh"),
                       ("v12_guest.py", HERE.parent / "gce-v12-clock-transfer" / "guest.py")):
        require(sha(path.read_bytes()) == files[name], f"published collector differs from registered {name}")


def check_frozen(frozen, calibration, host, registration, journal_digest):
    manifest, plan = registration["corpus"], registration["plan"]
    coeff, cases = calibration_fit(calibration, manifest, plan)
    require(frozen["schema"] == 1 and frozen["cases"] == cases and
            frozen["calibration_boot_sha256"] == host["boot_sha256"] and
            frozen["calibration_trials_sha256"] == journal_digest and frozen["runtime_sha256"] == plan["runtime_sha256"]
            and frozen["manifest_sha256"] == plan["manifest_sha256"] and frozen["plan_sha256"] == host["plan_sha256"],
            "frozen fit references different calibration, runtime or limits")
    dispatch, = {r["receipt"]["work_measurement"]["dispatch_profile"] for r in calibration.values() if r["kind"] == "raw"}
    policy = frozen["policy"]
    weights = dict(instruction_ns_numerator=coeff.numerator, rep_byte_ns_numerator=coeff.numerator, ns_denominator=coeff.denominator)
    identity = dict(model_id="pinned-judge-pilot", model_version=1)
    formula = dict(method="weighted-work-nanoseconds-v1", reference_id="three-reference-algorithms")
    runtime = dict(engine_profile=PROFILE, dispatch_profile=dispatch, family="cpp", cpuid_profile="x86-64-v2")
    require(policy == {**weights, **identity, **formula, **runtime, "runtime_id": "CPP20@" + plan["runtime_image"]},
            "coefficients or runtime policy differ from reference-only fit")
    models = frozen["time_models"]
    require(models["schema"] == 2 and models["models"] == [{**identity, **formula, "coefficients": weights}]
            and len(models["profiles"]) == 1, "wrong model registry")
    profile, = models["profiles"]
    require(all(profile[k] == v for k,v in {**identity, **runtime, "source_language": "CPP20", "runtime_image": plan["runtime_image"]}.items()),
            "wrong model profile")
    options = profile["runtime_options"]
    descriptor = {k: options[k] for k in ("version", "argv", "env", "memory_mb")}
    require(descriptor == options and descriptor["version"] == 1 and options["memory_mb"] == HARD["memory_mb"],
            "bad effective launcher options")
    encoded = json.dumps(descriptor, separators=(",", ":"), ensure_ascii=False)
    for ch in "<>&\u2028\u2029":
        encoded = encoded.replace(ch, "\\u%04x" % ord(ch))
    options_id = "runtime-options-v1:sha256:" + sha(encoded.encode())
    binding = model_binding(policy, options_id)
    require(frozen["runtime_options_id"] == options_id and frozen["model_binding"] == binding, "wrong frozen clock binding")
    scope = frozen["judge_cases"]
    expected = [dict(problem_id=p, source_language="CPP20", stdin_sha256=manifest[p]["stdin_sha256"], time_ms=ms, model_binding=binding)
                for p in corpus.PROBLEMS for ms in {cases[p][k + "_ms"] for k in ("contest", "low", "high", "stress")}]
    sort = lambda items: sorted(items, key=lambda r: (r["problem_id"], r["time_ms"]))
    require(scope["schema"] == 1 and sort(scope["cases"]) == sort(expected), "wrong frozen case scope")


def check_judge(row, frozen, plan, oracle):
    r, nominal = row["receipt"], row["limit_ms"]
    status, e, j, report = r["status"], r["work_measurement_execution"], r["work_measurement_judgment"], r["work_measurement"]
    policy = frozen["policy"]
    clock, cause = e["clock"], e["termination_source"]
    require(r["verdict_source"] == "durante_judge" and j["verdict"] == status and j["nominal_time_ms"] == nominal and
            j["termination_source"] == cause and j["state"] == e["state"] and
            j["authority"] == e["authority"] == "diagnostic_only" and j["integrity_mode"] == "off" and not j.get("integrity_flagged")
            and e["raw_limits"] == HARD and e["nominal_limits"] == {**NOMINAL, "time_ms": nominal} and
            positive(r["cpu_time_ns"]) and r["cpu_time_ns"] <= HARD["cpu_time_ms"] * MILLION and
            0 <= r["wall_time_ms"] <= HARD["wall_time_ms"] and 0 <= r["memory_kb"] <= HARD["memory_mb"] * 1024 and
            not r.get("cpu_time_normalization") and not r.get("cpu_time_sampling"), "invalid judge policy or raw envelope")
    require(clock["version"] == 1 and clock["model_binding"] == frozen["model_binding"] and
            clock["metric"] == "model_weighted_nanoseconds_v1" and clock["state"] == "observed" and
            clock["budget"] == nominal * MILLION, "wrong weighted model or budget")
    progress = clock["progress"]
    counts = [progress[k] for k in ("instructions", "movs_bytes", "stos_bytes")]
    require(not progress.get("overflow_mask") and all(type(c) is int and 0 <= c < 2**64 for c in counts),
            "invalid pilot clock progress")
    num, den = sum(counts) * policy["instruction_ns_numerator"], policy["ns_denominator"]
    require(policy["instruction_ns_numerator"] == policy["rep_byte_ns_numerator"] and den > 0, "invalid combined-work model")
    crossed = num > den * clock["budget"]
    if status == TLE:
        require(crossed and cause in ("durante_work_clock", "durante_work_clock_final") and
                e["state"] == "budget_exceeded" and r.get("score") is None, "TLE did not come from the weighted budget")
    else:
        require(status in (AC, WA) and not crossed and cause == "stdout" and e["state"] == "complete" and
                (r.get("score") == 1 if status == AC else r.get("score") is None) and e["output_matches"] is (status == AC),
                "invalid completed clock verdict")
    live = cause == "durante_work_clock"
    check_report(report, plan, not live)
    require(report["dispatch_profile"] == policy["dispatch_profile"], "wrong runtime dispatch")
    eligible = e["time_estimate_eligibility"]["eligible"]
    if live:
        require(r.get("exit_code") is None and e.get("output_matches") is None and e.get("time_estimate") is None and
                eligible is False, "live clock stop claimed a completed program")
        return
    rep, estimate = report["rep_breakdown"], e["time_estimate"]
    ns = -(-num // den)
    require(counts == [report["instructions"], rep["movs_bytes"], rep["stos_bytes"]] and r["exit_code"] == 0 and
            e["output_matches"] is (row["stdout_sha256"] == oracle) and eligible is True and
            not r.get("stdout_truncated") and not r.get("stderr_truncated") and
            estimate["runtime_options_id"] == frozen["runtime_options_id"] and estimate["authority"] == "diagnostic_only" and
            estimate["nanoseconds"] == ns and estimate["milliseconds"] == -(-ns // MILLION) and
            all(estimate[k] == v for k,v in policy.items()) and e["nominal_time_exceeded"] is crossed,
            "terminal weighted time, output or budget does not reconcile")


def check_requests(trials, binding, manifest, hashes, payload):
    for (problem, role, _, kind, _), row in trials.items():
        key = (problem, role, kind, row["limit_ms"])
        digest = row["request_sha256"]
        if key in hashes:
            require(hashes[key] == digest, "request bytes changed across repeats or guests")
            continue
        if payload:
            artifact = (payload / "corpus" / f"{problem}-{role}").read_bytes()
            require(sha(artifact) == manifest[problem]["elf_sha256"][role], "payload ELF differs from registration")
            stdin, expected = corpus.case(problem)
            req = dict(problem_id=problem, lang="CPP20", binaries=[dict(name="Main", mode="exec",
                       data_b64=base64.b64encode(artifact).decode())], stdin=stdin.decode(), expected_stdout=expected.decode(),
                       limits={**NOMINAL, "time_ms": row["limit_ms"]}, emit_logs=True)
            if kind != "native":
                req.update(work_measurement=True, work_measurement_limits=HARD)
            if kind == "raw":
                req["work_measurement_clock"] = {"version": 1, "budget": (1 << 63) - 1}
            if kind == "judge":
                req.update(work_measurement_time=True, work_measurement_judge=True,
                           work_measurement_judge_case_required=True, work_measurement_judge_clock_binding=binding)
            require(sha(json.dumps(req).encode()) == digest, "HTTP bytes differ from registered request")
        require(digest not in hashes.values(), "different requests have identical recorded bytes")
        hashes[key] = digest


def read_guest(root, registration):
    host = json.loads(evidence(root, "platform.json"))
    require(host["phase"] in ("calibrate", "primary", "repeat") and host["cpu"] == 1 and
            host["affinity"] == [1] and host["cpuset_cpus_effective"] == host["cpu1_thread_siblings"] == "1" and
            re.fullmatch("[0-9a-f]{64}", host["boot_sha256"]) and host["plan_sha256"] == registration["payload"]["plan.json"] and
            0 <= host["steal_ticks_before"] <= host["steal_ticks_after"], "guest identity, cpuset or SMT mismatch")
    calibration = host["phase"] == "calibrate"
    workers = host["workers"]
    require(set(workers) == {"native", "raw" if calibration else "judge"} and
            all(map(positive, workers.values())) and len(set(workers.values())) == len(workers), "missing distinct workers")
    journal = evidence(root, "trials.jsonl")
    rows = [json.loads(line) for line in journal.splitlines()]
    trials = index_trials(rows, read_windows(evidence(root, "exec-trace.txt")), registration["corpus"], calibration)
    for row in rows:
        require(sha(row["stdout"].encode()) == row["stdout_sha256"], "recorded stdout does not match its hash")
    return host, trials, sha(journal)


def summarize_group(rows):
    native = [r["receipt"] for r in rows if r["kind"] == "native"]
    judge = [r["receipt"] for r in rows if r["kind"] == "judge"]
    labels = {AC: "AC", WA: "WA", TLE: "TLE"}
    counts = lambda samples: dict(sorted(Counter(labels[r["status"]] for r in samples).items()))
    causes = Counter(r["work_measurement_execution"]["termination_source"] for r in judge)
    result = dict(native=counts(native), durante=counts(judge), live_stops=causes["durante_work_clock"])
    if causes["durante_work_clock_final"]:
        result["exit_time_stops"] = causes["durante_work_clock_final"]
    # CPU-limited native samples are censored. Compare only actual completions.
    # A completed clock over budget still has an exact exit estimate.
    n = [r["cpu_time_ns"] for r in native if r.get("exit_code") == 0 and
         r["verdict_source"] in ("stdout", "cpu_time_final", "cpu_time_cgroup_final")]
    j = [r["work_measurement_execution"]["time_estimate"]["nanoseconds"] for r in judge
         if r["work_measurement_execution"].get("time_estimate") and r.get("exit_code") == 0]
    if n and j:
        nd, jd = median(n), median(j)
        result["completed"] = dict(native_runs=len(n), durante_runs=len(j), native_median_ms=round(nd/MILLION, 3),
                                   durante_median_ms=round(jd/MILLION, 3), signed_error_pct=round(100*(jd-nd)/nd, 2))
    return result


def summarize(acceptance, frozen):
    results, allowed = {}, []
    for p in corpus.PROBLEMS:
        case = frozen["cases"][p]
        guests = {}
        qualifies = True
        for name, trials in sorted(acceptance.items()):
            grouped = defaultdict(list)
            for (problem, role, boundary, _, _), row in trials.items():
                if p == problem:
                    grouped[(boundary, role)].append(row)
            summary = {}
            for (boundary, role), rows in sorted(grouped.items()):
                group = summarize_group(rows)
                summary[role if boundary == "contest" else boundary] = group
                if boundary == "contest" or boundary == "stress":
                    expected = TLE if boundary == "stress" else EXPECTED[role]
                    qualifies &= all(r["receipt"]["status"] == expected for r in rows)
                    if boundary == "contest" and role == "slow":
                        qualifies &= group["live_stops"] > 0
            guests[name] = summary
        results[p] = dict(contest_ms=case["contest_ms"], low_ms=case["low_ms"], high_ms=case["high_ms"],
                          qualifies=qualifies, guests=guests)
        if qualifies:
            contest, = [entry for entry in frozen["judge_cases"]["cases"]
                        if entry["problem_id"] == p and entry["time_ms"] == case["contest_ms"]]
            allowed.append(contest)
    return dict(schema=1, registered_contest_cases=len(corpus.PROBLEMS), qualified_contest_cases=len(allowed),
                judge_cases={"schema": 1, "cases": allowed}, results=results)


def check_infrastructure(infrastructure, observations, registration):
    require(set(infrastructure) == set(observations) == {"calibrate", "primary", "repeat"}, "missing infrastructure phase")
    pinned = dict(zone="asia-northeast3-a", machine_type="n2d-standard-4", cpu_platform="AMD Milan",
                  threads_per_core=1, image="debian-12-bookworm-v20261117")
    for phase, entry in infrastructure.items():
        require(all(entry[k] == value for k, value in pinned.items()) and type(entry["threads_per_core"]) is int and
                entry["disk_auto_delete"] is True and entry["external_ip"] is False and entry["service_account"] is False,
                f"{phase}: different GCE platform or instance configuration")
        require(entry["cleanup_http"] == {"instance": 404, "boot_disk": 404}, f"{phase}: incomplete instance or boot disk cleanup")
        require(entry["archive_sha256"] == registration["archive_sha256"] and
                all(entry[k] == value for k, value in observations[phase].items()), f"{phase}: returned evidence digest or identity differs")
    for key in ("instance_id_sha256", "disk_id_sha256", "boot_sha256"):
        identities = {entry[key] for entry in infrastructure.values()}
        require(len(identities) == 3 and all(re.fullmatch("[0-9a-f]{64}", digest) for digest in identities),
                f"missing or reused GCE identity: {key}")


def verify(registration, calibration_dir, validation_dirs, payload=None, infrastructure=None):
    check_registration(registration)
    host, calibration, journal_digest = read_guest(calibration_dir, registration)
    require(host["phase"] == "calibrate" and len(validation_dirs) == 2, "expected one calibration and two acceptance guests")
    frozen_bytes = evidence(calibration_dir, "frozen.json")
    frozen = json.loads(frozen_bytes)
    check_frozen(frozen, calibration, host, registration, journal_digest)
    acceptance, hashes, boots = {}, {}, {host["boot_sha256"]}
    check_requests(calibration, None, registration["corpus"], hashes, payload)
    observations = {}
    def record(root, platform, trials, digest):
        observations[platform["phase"]] = dict(boot_sha256=platform["boot_sha256"], trial_count=len(trials),
            remote_frozen_sha256=sha(frozen_bytes), trials_sha256=digest, exec_trace_sha256=sha(evidence(root, "exec-trace.txt")))

    record(calibration_dir, host, calibration, journal_digest)
    for directory in validation_dirs:
        guest, trials, digest = read_guest(directory, registration)
        phase = guest["phase"]
        require(phase in ("primary", "repeat") and phase not in acceptance and guest["boot_sha256"] not in boots and
                guest["frozen_sha256"] == sha(frozen_bytes) and evidence(directory, "frozen.json") == frozen_bytes,
                "reused acceptance guest or changed frozen configuration")
        boots.add(guest["boot_sha256"])
        record(directory, guest, trials, digest)
        check_requests(trials, frozen["model_binding"], registration["corpus"], hashes, payload)
        for (p, _, boundary, kind, _), row in trials.items():
            require(row["limit_ms"] == frozen["cases"][p][boundary + "_ms"], "changed held-out limit")
            oracle = registration["corpus"][p]["stdout_sha256"]
            if kind == "native":
                check_native(row, oracle)
            else:
                check_judge(row, frozen, registration["plan"], oracle)
        acceptance[phase] = trials
    if infrastructure is not None:
        check_infrastructure(infrastructure, observations, registration)
    result = summarize(acceptance, frozen)
    result["calibration_trials"] = len(calibration)
    result["acceptance_trials"] = sum(map(len, acceptance.values()))
    result["frozen_sha256"] = sha(frozen_bytes)
    return result


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("--registration", type=Path, required=True)
    cli.add_argument("--calibration", type=Path, required=True)
    cli.add_argument("--validation", type=Path, action="append", required=True)
    cli.add_argument("--payload", type=Path, help="also reconstruct each full HTTP request from registered ELF files")
    cli.add_argument("--infrastructure", type=Path, help="verify GCE instance identities, machine configuration, transfer digests and cleanup")
    cli.add_argument("--output", type=Path)
    args = cli.parse_args()
    infrastructure = json.loads(args.infrastructure.read_bytes()) if args.infrastructure else None
    summary = verify(json.loads(args.registration.read_bytes()), args.calibration, args.validation, args.payload, infrastructure)
    output = json.dumps(summary, indent=2, sort_keys=True, allow_nan=False) + "\n"
    if args.output:
        args.output.write_text(output)
    else:
        print(output, end="")
