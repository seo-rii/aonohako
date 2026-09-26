# UHMLANG judge compatibility patch

The runtime remains the Go v2 interpreter from
[rycont/umjunsik-lang](https://github.com/rycont/umjunsik-lang), pinned by
`REVISION`. `judge.patch` is applied to that exact checkout before building;
patch or regression failures abort the image build. Upstream's MIT license is
preserved in `LICENSE.upstream`.

## Fixes

* Preserve every blank source line, including the newline after the header.
  An empty `엄` assignment may already consume its newline; do not consume a
  second one. The same accounting applies to LF, CRLF, and `~` separators.
* Convert the one-based `준` target to the zero-based AST index while accounting
  for the evaluator's loop increment (`target - 2`, not `target - 3`).
* Retain aonohako's existing `fmt.Scan` input fix. Upstream `Scanln` loses values
  when multiple integers share an input line. This was already fixed in the
  catalog; it is consolidated here, not a new fix for the reported timeout.

* Count each `.` / `,` suffix token exactly once. The infix parser must inspect
  `peekToken` before advancing; inspecting `curToken` again double-counts the
  first sign and drops the last sign. For example, after `엄....`, both
  `식어.,!` and `식어,.!` must print `4`, not `6` and `2`. This is a separate
  pre-existing arithmetic bug, not the source-line/jump bug described below.

Jungol submission **13681383** still times out with the previous catalog's
input-only fix. With this patch, its recorded input produces `5`, `9`, `3`, and
`8` on separate lines and terminates normally. The tests use an independently
written repeated-addition program rather than publishing the submitted source.

## Build and test

Run from the repository root with Git and Go installed:

```sh
AONOHAKO_GO="$(command -v go)" \
  bash third_party/umjunsik-lang-go/build.sh /tmp/umjunsik-lang-go
```

The helper fetches the pinned upstream source, checks/applies the patch, builds
a stdlib-only binary, runs the CLI regressions, and installs only after success.
It makes no server/API changes. Both `/usr/local/bin/umjunsik-lang-go` and the
existing `/usr/bin/umjunsik-lang-go` symlink remain unchanged in runtime images.
Existing `GOMEMLIMIT`, `GOGC`, sandbox policy, and time limits are retained.

To test an already-built interpreter without network access:

```sh
GO111MODULE=off UHMLANG_BINARY=/tmp/umjunsik-lang-go \
  go test -count=1 -timeout=60s -v \
  third_party/umjunsik-lang-go/regression_test.go
```

The build tag excludes these external-binary tests from ordinary `go test ./...`.
The explicit-file command above includes the file deliberately and fails if
`UHMLANG_BINARY` is missing. The dedicated workflow and runtime-image installer
both execute it with the freshly compiled binary.

Coverage includes exact stdout/stderr/exit status; forward, backward, conditional,
variable and blank-line jump targets; empty assignments; mixed-whitespace integer
input; LF/CRLF/tilde source variants; nine blank-line padding patterns; zero and
100-case loops; and an actual infinite loop stopped by the caller's timeout.
Finite programs have a two-second timeout per process; no instruction counter
or artificial termination rule is added to the interpreter.

Arithmetic coverage adds the two minimal mixed-sign reproducers and suffixes in
assignments, input, multiplication, conditions, and jump targets. It also checks
all 511 suffixes of length 0 through 8 for each of three starting values
(`-4`, `0`, `4`), four expression contexts, and LF/CRLF/tilde separators:
**18,396 generated arithmetic results**, batched into 36 bounded CLI invocations.
Expected results use `count(".") - count(",")`, independently of the parser.
Including the focused cases and the existing regressions, the suite has
**125 leaf cases and 179 interpreter invocations**.

## Deployment

After merging, rebuild and deploy the UHMLANG runtime image (`type-f` in the
production catalog), then rejudge affected submissions. A PR or a local passing
regression does not change an existing Jungol verdict by itself.
