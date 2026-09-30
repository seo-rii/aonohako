# UHMLANG runtime source

The Go interpreter is built directly from `seo-rii/umjunsik-lang`, pinned in
`runtime-images.yml` to `683e4c04ef48ecec27e5c950eba2c36eab794887` (fork PR #1).
The source contains the source-line/jump, whitespace-input, and mixed-sign
arithmetic corrections. Aonohako does not apply a source patch or track a moving
branch. Other language implementations in that fork are not used by this change.

The installer verifies the checkout SHA, builds the CLI, and runs the fork's
native parser and CLI regressions against that exact binary before installing
it. Temporary source and Go caches are removed; the upstream MIT license is
installed under `/usr/local/share/licenses/umjunsik-lang-go/LICENSE`.

The existing `/usr/bin/umjunsik-lang-go` entry point, language identifier, memory
tuning, sandbox rules, and judging limits are unchanged. No UHMLANG assets are
mounted into the common Docker install layer.

## Validation

```sh
# Offline catalog contract and real-shell smoke failure injection tests.
go test -count=1 ./internal/runtimepacks -run '^TestUHMLANG'

# Requires GitHub access. Builds the pinned fork with the actual catalog script,
# relocating installation paths to a temporary directory, then runs its smoke.
AONOHAKO_TEST_UHMLANG_LIVE=1 \
  go test -count=1 -timeout=8m -v ./internal/runtimepacks -run '^TestUHMLANG'

# Actual language image and production profile (requires Docker).
./scripts/build_runtime_images.sh -mode ci -only ci-uhmlang -tag-prefix aonohako-ci
docker run --rm aonohako-ci:ci-uhmlang aonohako-smoke
./scripts/build_runtime_images.sh -mode production -only type-f
```

The smoke failure tests inject a wrong result, a nonzero exit after correct
output, and a real timeout at each of the five smoke invocations. Capturing
output and comparing it are separate shell commands under `set -euo pipefail`.
The fork itself provides exact stdout/stderr/exit and control-flow tests.

## Updates and deployment

Review and test a fork revision before changing the catalog pin. A branch move
alone does not change the installed interpreter. Keep the license and native
regression gate when updating the revision or returning to upstream.

After merging the consumer PR, rebuild and deploy `type-f`; rejudge affected
submissions as a separate operation. Creating or merging a source PR alone does
not deploy a runtime or change any existing judge verdict.
