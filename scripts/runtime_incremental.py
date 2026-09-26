#!/usr/bin/env python3
"""Plan runtime verification from a successful run's actual input snapshot.

The build fingerprint and validation fingerprint deliberately have different
inputs. GitHub API/run/artifact errors invalidate the baseline, never the plan.
Only the same PR (including its head repository) or an ancestor push to the PR
base/default branch may supply a baseline. No PR head SHA is used as a proxy
for the merge tree that was actually tested.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import zipfile
from urllib.parse import quote

SCHEMA = 2
SHA = re.compile(r"^[0-9a-f]{40}$")
FINGERPRINT = re.compile(r"^sha256:[0-9a-f]{64}$")
ARTIFACT = "runtime-snapshot-v2-attempt-"


def command(*args):
    return subprocess.check_output(args, timeout=60)


def digest(value):
    return "sha256:" + hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def validate_inventory(entries, label):
    if not isinstance(entries, list) or not entries:
        raise ValueError(f"{label}: expected a nonempty inventory")
    result = {}
    for item in entries:
        if not isinstance(item, dict):
            raise ValueError(f"{label}: invalid entry")
        name, fp = item.get("name"), item.get("fingerprint")
        if not isinstance(name, str) or not re.fullmatch(r"(?:type|ci)-[a-z0-9-]+", name):
            raise ValueError(f"{label}: invalid name")
        if not isinstance(fp, str) or not FINGERPRINT.fullmatch(fp) or name in result:
            raise ValueError(f"{label}: invalid/duplicate fingerprint entry: {name}")
        if label == "production" and not isinstance(item.get("languages"), str):
            raise ValueError(f"{label}: missing languages for {name}")
        result[name] = item
    return result


def validation_inputs():
    records = command("git", "ls-tree", "-r", "-z", "HEAD").split(b"\0")
    workflow, sandbox = [], []
    for record in records:
        if not record:
            continue
        _, path_bytes = record.split(b"\t", 1)
        path = path_bytes.decode()
        if path.startswith((".github/", "cmd/runtime-impact/")) or path == "scripts/runtime_incremental.py":
            workflow.append(record.decode())
        if path.endswith("_test.go") and path.startswith(("internal/execute/", "internal/compile/")):
            sandbox.append(record.decode())
    return {"workflow": digest(sorted(workflow)), "sandbox": digest(sorted(sandbox))}


def context(env, event):
    pr = event.get("pull_request") or {}
    repo = event.get("repository") or {}
    return {
        "repository": env["GITHUB_REPOSITORY"],
        "repository_id": repo.get("id"),
        "run_id": str(env["GITHUB_RUN_ID"]),
        "run_attempt": str(env.get("GITHUB_RUN_ATTEMPT", "1")),
        "event": env["GITHUB_EVENT_NAME"],
        "pr_number": pr.get("number"),
        "head_repository_id": (pr.get("head", {}).get("repo") or repo).get("id"),
        "head_branch": env.get("GITHUB_HEAD_REF") or env.get("GITHUB_REF_NAME"),
        "base_branch": pr.get("base", {}).get("ref") or env.get("GITHUB_REF_NAME"),
        "cache_write": env["GITHUB_EVENT_NAME"] == "push" and env.get("GITHUB_REF") == "refs/heads/main",
    }


def eligible_run(run, ctx):
    if str(run.get("id")) == ctx["run_id"] or run.get("conclusion") != "success":
        return False
    if run.get("event") == "pull_request":
        return (ctx["event"] == "pull_request" and run.get("head_branch") == ctx["head_branch"]
                and (run.get("head_repository") or {}).get("id") == ctx["head_repository_id"]
                and any(pr.get("number") == ctx["pr_number"] for pr in run.get("pull_requests", [])))
    if (run.get("event") != "push" or run.get("head_branch") != ctx["base_branch"]
            or (run.get("head_repository") or {}).get("id") != ctx["repository_id"]):
        return False
    sha = run.get("head_sha", "")
    if not SHA.fullmatch(sha):
        return False
    return subprocess.run(["git", "merge-base", "--is-ancestor", sha, "HEAD"],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30).returncode == 0


def api(path):
    return json.loads(command("gh", "api", path))


def load_baseline(ctx):
    """Expired, missing, inaccessible, old-schema or malformed snapshots => full check."""
    root = f"/repos/{ctx['repository']}/actions"
    try:
        scopes = [("push", ctx["base_branch"])]
        if ctx["event"] == "pull_request":
            scopes.insert(0, ("pull_request", ctx["head_branch"]))
        for event, branch in scopes:
            for page in range(1, 11):
                runs = api(f"{root}/workflows/ci.yml/runs?status=success&event={event}&branch={quote(branch, safe='')}&per_page=100&page={page}")["workflow_runs"]
                for run in runs:
                    if not eligible_run(run, ctx):
                        continue
                    attempt = str(run.get("run_attempt", 1))
                    name = ARTIFACT + attempt
                    artifacts = api(f"{root}/runs/{run['id']}/artifacts?name={name}&per_page=100")["artifacts"]
                    matches = [a for a in artifacts if a.get("name") == name and not a.get("expired")]
                    if len(matches) != 1 or matches[0].get("size_in_bytes", 0) > 8_000_000:
                        continue
                    try:
                        data = command("gh", "api", f"{root}/artifacts/{matches[0]['id']}/zip")
                        if len(data) > 8_000_000:
                            continue
                        with zipfile.ZipFile(io.BytesIO(data)) as archive:
                            info = archive.getinfo("runtime-snapshot.json")
                            if info.file_size > 8_000_000:
                                continue
                            snapshot = json.loads(archive.read(info))
                        origin = snapshot["context"]
                        if (snapshot.get("schema_version") != SCHEMA or not SHA.fullmatch(snapshot.get("source_sha", ""))
                                or origin.get("repository") != ctx["repository"]
                                or origin.get("run_id") != str(run["id"]) or origin.get("run_attempt") != attempt
                                or origin.get("event") != run.get("event")
                                or (run["event"] == "pull_request" and origin.get("pr_number") != ctx["pr_number"])):
                            continue
                        for family in ("production", "ci"):
                            validate_inventory(snapshot[family], family)
                        print(f"Using verified runtime snapshot from run {run['id']} ({snapshot['source_sha']})", file=sys.stderr)
                        return snapshot
                    except (OSError, ValueError, KeyError, zipfile.BadZipFile, subprocess.SubprocessError) as error:
                        print(f"Ignoring unusable runtime snapshot: {error}", file=sys.stderr)
                if len(runs) < 100:
                    break
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(f"Runtime baseline unavailable: {error}", file=sys.stderr)
    print("No verified snapshot available; running full runtime verification", file=sys.stderr)
    return None


def plan(impact, inputs, source_sha, ctx, baseline=None, force=False):
    if not SHA.fullmatch(source_sha):
        raise ValueError("expected the actual checkout commit SHA")
    inventories = {family: validate_inventory(impact.get(family), family) for family in ("production", "ci")}
    if "ci-python" not in inventories["ci"] or "type-i" not in inventories["production"]:
        raise ValueError("required sandbox/mixin profiles are absent")
    if baseline is not None:
        try:
            if baseline.get("schema_version") != SCHEMA:
                raise ValueError("unsupported baseline schema")
            old = {f: validate_inventory(baseline[f], f) for f in inventories}
        except (ValueError, KeyError, TypeError):
            baseline = None
    old = {} if baseline is None else old
    snapshot = {"schema_version": SCHEMA, "source_sha": source_sha, "context": ctx,
                "baseline_run_id": baseline["context"]["run_id"] if baseline else None}
    for family, entries in inventories.items():
        snapshot[family] = []
        for name, item in sorted(entries.items()):
            verification = digest([item["fingerprint"], inputs["workflow"]])
            previous = old.get(family, {}).get(name, {})
            snapshot[family].append({**item, "verification": verification,
                                    "changed": force or previous.get("fingerprint") != item["fingerprint"],
                                    "verify": force or previous.get("verification") != verification})
    sandbox = digest([inventories["ci"]["ci-python"]["fingerprint"], inputs["workflow"], inputs["sandbox"]])
    snapshot["checks"] = {"sandbox": sandbox}
    selected = {f: [x for x in snapshot[f] if x["changed"] or x["verify"]] for f in inventories}
    outputs = {"base_sha": baseline.get("source_sha", "") if baseline else ""}
    for family in inventories:
        special = {"ci-idris2", "ci-cuda-ocelot"} if family == "ci" else {"type-a", "type-c", "type-o"}
        rows = [{k: x[k] for k in ("name", "languages", "fingerprint") if k in x} for x in selected[family]]
        outputs[family + "_matrix"] = rows
        for kind in ("regular", "cached"):
            subset = [x for x in rows if (x["name"] in special) == (kind == "cached")]
            outputs[f"{family}_{kind}_matrix"] = subset
            outputs[f"has_{family}_{kind}"] = bool(subset)
    outputs["has_any_runtime"] = any(selected.values())
    outputs["ci_python_changed"] = any(x["name"] == "ci-python" for x in selected["ci"])
    outputs["production_type_i_changed"] = any(x["name"] == "type-i" for x in selected["production"])
    outputs["sandbox_required"] = force or sandbox != (baseline or {}).get("checks", {}).get("sandbox")
    snapshot["outputs"] = outputs
    return snapshot


def verify_jobs(snapshot, needs):
    outputs = snapshot["outputs"]
    required = {"policy", "unit", "supply-chain", "runtime-matrix", "toolchain-summary"}
    conditional = {"runtime-binaries": outputs["has_any_runtime"], "sandbox": outputs["sandbox_required"],
                   "image-sbom": outputs["ci_python_changed"], "mixin-smoke": outputs["production_type_i_changed"],
                   "language-smoke": outputs["has_ci_regular"],
                   "toolchain-profile": outputs["has_production_regular"]}
    for family, job in (("ci", "language-smoke"), ("production", "toolchain-profile")):
        for access in ("read", "write"):
            conditional[f"{job}-cache-{access}"] = outputs[f"has_{family}_cached"] and (access == "write") == snapshot["context"]["cache_write"]
    required |= {name for name, selected in conditional.items() if selected}
    for name in required:
        if needs.get(name, {}).get("result") != "success":
            raise ValueError(f"selected prerequisite {name} did not succeed")
    for name, result in needs.items():
        if result.get("result") not in ("success", "skipped"):
            raise ValueError(f"prerequisite {name} failed or was cancelled")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["plan", "verify"])
    parser.add_argument("--impact", default="runtime-impact.json")
    parser.add_argument("--snapshot", default="runtime-snapshot.json")
    args = parser.parse_args()
    if args.mode == "verify":
        verify_jobs(json.loads(Path(args.snapshot).read_text()), json.loads(os.environ["RUNTIME_NEEDS"]))
        return
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    ctx = context(os.environ, event)
    snapshot = plan(json.loads(Path(args.impact).read_text()), validation_inputs(),
                    command("git", "rev-parse", "HEAD").decode().strip(), ctx,
                    load_baseline(ctx), os.environ.get("FORCE_RUNTIME_CHECKS") == "true")
    Path(args.snapshot).write_text(json.dumps(snapshot, indent=2) + "\n")
    with open(os.environ["GITHUB_OUTPUT"], "a") as stream:
        for key, value in snapshot["outputs"].items():
            stream.write(f"{key}={json.dumps(value, separators=(',', ':')) if not isinstance(value, str) else value}\n")
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a") as stream:
        stream.write("### Runtime verification plan\n\n")
        stream.write(f"Actual checkout: `{snapshot['source_sha']}`; baseline run: `{snapshot['baseline_run_id'] or 'none'}`\n\n")
        for family in ("ci", "production"):
            names = ", ".join(x["name"] for x in snapshot["outputs"][family + "_matrix"]) or "none"
            stream.write(f"{family}: {names}\n\n")


if __name__ == "__main__":
    main()
