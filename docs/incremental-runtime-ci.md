# Incremental runtime CI

`cmd/runtime-impact` describes the current committed build inputs. `scripts/runtime_incremental.py` compares those fingerprints with a verified run snapshot rather than a PR head SHA. Snapshots record the actual checkout commit, which is the synthetic merge commit for pull requests, and the complete runtime inventory.

## Success boundary

The matrix job publishes a plan, not evidence of success. The final `runtime-verified` job requires success from every selected build/test and rejects failed or cancelled prerequisites. Only then does it publish `runtime-snapshot-v2-attempt-N`. Baseline discovery also requires the corresponding workflow run to have succeeded. Snapshots from the same PR/head repository are preferred; eligible ancestor pushes to the base branch are the fallback. Missing, expired, inaccessible, or incompatible snapshots cause a full verification. Snapshots are retained for 30 days; discovery is bounded to 1,000 successful runs per scope.

## Build versus verification inputs

An image fingerprint excludes test-only and workflow-only inputs. Validation fingerprints include workflow/optimizer inputs, and the sandbox fingerprint additionally includes compile/execute test sources. A security-test-only commit therefore still runs the root-backed sandbox suite without selecting unrelated production images. Workflow changes revalidate the image matrices even when their build fingerprints are unchanged. The current CI does not retain complete successful images for cross-run reuse: selected verification jobs may rebuild the image needed to execute their tests. Existing layer caches are retained.

The separate `go-modules/` module is not covered by the root `go test ./...`. Its tests run during Go-containing image builds. Its complete Git tree (including test sources, fixtures and deletions) therefore has a **Go-specific validation fingerprint**. A nested-module test-only change selects `ci-go` and production profiles whose resolved language list contains `go`, even though the image-input fingerprint is unchanged. Unrelated runtimes and the Python sandbox are not selected for that change. After successful verification, another unchanged run skips them again.

## Recovery and refresh

Use **Re-run all jobs** or a new CI execution after a failed attempt. Attempt-qualified plan snapshots deliberately reject mixing evidence from different attempts; re-running failed jobs alone is not the supported recovery path. Existing runtime-binary artifact handling remains unchanged.

The manual `force_runtime_checks` input selects every runtime and propagates refresh through every regular and reusable runtime workflow. The builder receives `AONOHAKO_RUNTIME_REFRESH=true` and passes `--pull --no-cache-filter runtime-foundation,runtime-toolchain` to **both** the cache-target export and final image build. Consequently, apt/pip/npm installation and external download scripts execute again instead of accepting existing RUN layers from a registry cache. The deterministic Go-binary stages remain cacheable. Ordinary runs keep the existing cache behavior.

Outside GitHub Actions, the same operation is available through the builder's `-refresh` boolean flag or `AONOHAKO_RUNTIME_REFRESH=true`. An explicit `-refresh=false` overrides a valid environment default. Malformed environment booleans fail explicitly instead of silently reusing caches. Digest-pinned base images and explicitly pinned package versions remain pinned; refresh is not an automatic version upgrade.

Source fingerprints do not prove that unpinned package repositories have remained unchanged. Vulnerability scanning and its failure policy remain enabled; this optimization must not convert a failed scan into a successful baseline.

## Regression tests

Run `python3 scripts/runtime_incremental_test.py` for baseline selection, selective invalidation, selected-job failure handling and synthetic-merge Git history. Run `python3 scripts/runtime_incremental_go_test.py` for real nested-Go-module test regressions. The nested-module fixture demonstrates that a failing test passes unnoticed by the root module but fails in the nested module, and verifies that add/edit/delete operations select only Go runtimes.

Run `go test ./cmd/runtime-builder ./internal/runtimepacks` for refresh flag parsing, the compiled builder CLI's Docker argument forwarding, and parsed YAML contracts covering every direct/reusable workflow. The CLI regression uses an argv-recording Docker stub; it validates both build invocations but is not a live Docker cache-hit test. Actual runtime images and scans remain covered by GitHub CI.
