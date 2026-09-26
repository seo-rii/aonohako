# Incremental runtime CI

`cmd/runtime-impact` describes the current committed build inputs. `scripts/runtime_incremental.py` compares those fingerprints with a verified run snapshot rather than a PR head SHA. Snapshots record the actual checkout commit, which is the synthetic merge commit for pull requests, and the complete runtime inventory.

## Success boundary

The matrix job publishes a plan, not evidence of success. The final `runtime-verified` job requires success from every selected build/test and rejects failed or cancelled prerequisites. Only then does it publish `runtime-snapshot-v2-attempt-N`. Baseline discovery also requires the corresponding workflow run to have succeeded. Snapshots from the same PR/head repository are preferred; eligible ancestor pushes to the base branch are the fallback. Missing, expired, inaccessible, or incompatible snapshots cause a full verification. Snapshots are retained for 30 days; discovery is bounded to 1,000 successful runs per scope.

## Build versus verification inputs

An image fingerprint excludes test-only and workflow-only inputs. Validation fingerprints include workflow/optimizer inputs, and the sandbox fingerprint additionally includes compile/execute test sources. A security-test-only commit therefore still runs the root-backed sandbox suite without selecting unrelated production images. Workflow changes revalidate the image matrices even when their build fingerprints are unchanged. The current CI does not retain complete successful images for cross-run reuse: selected verification jobs may rebuild the image needed to execute their tests. Existing layer caches are retained.

## Recovery and refresh

Use **Re-run all jobs** or a new CI execution after a failed attempt. Attempt-qualified plan snapshots deliberately reject mixing evidence from different attempts; re-running failed jobs alone is not the supported recovery path. Existing runtime-binary artifact handling remains unchanged.

The manual `force_runtime_checks` input rebuilds and revalidates every runtime, including external package refreshes. Source fingerprints do not prove that unpinned package repositories have remained unchanged. Vulnerability scanning and its failure policy remain enabled; this optimization must not convert a failed scan into a successful baseline.

Run the standalone regression tests with `python3 scripts/runtime_incremental_test.py`. They cover missing/foreign/failed baselines, selective changes, security-test-only changes, selected-job failures, and a real synthetic-merge Git history.
