# Payload Schema

The canonical numeric request and capture limits are generated from code in
[Public request limits](limits.md).

## `POST /compile` — Request

```jsonc
{
  "lang": "CPP17",                           // language identifier (see Supported Languages)
  "version": "20",                           // optional C/C++ std, Java/Kotlin-JVM release, or Rust edition override
  "sources": [                               // source files to compile (max 512 entries)
    {
      "name": "Main.cpp",                   // filename (relative, no path traversal allowed)
      "data_b64": "<base64>"                // base64-encoded file contents
    }
  ],
  "target": "Main",                          // optional output binary name (default: "Main")
  "entry_point": "src/main.c",               // optional submitted source path to validate as the intended entry file
  "problem_id": "contest-1/a",               // optional problem policy key for server-selected runtime profile
  "runtime_profile": "low-memory",           // optional operator-defined runtime tuning profile
  "emit_logs": true                          // optional compiler stdout/stderr log SSE events; defaults to true
}
```

`sources` may contain multiple files. Every `name` is interpreted as a relative
path inside the compile workspace; absolute paths and traversal are rejected.
When `entry_point` names a source path, it must exactly match one submitted
source after path cleaning. Native multi-file compilers such as C/C++ still
compile all source files of the language, so `entry_point` is validation
metadata rather than an argument that drops helper sources.
`emit_logs=false` omits compiler stdout/stderr `log` SSE events. Compiler
diagnostics remain in the final `result`; a failed compile still emits a
structural `error` event before that result without duplicating the diagnostic
output.

`version` is optional and currently applies to C, C++, Java, Kotlin/JVM, and
Rust compile profiles. C/C++ values select the language standard (`"c99"`,
`"c23"`, `"17"`, `"gnu++23"`), Java values select `javac --release` (`"8"`,
`"11"`, `"17"`, `"21"`), Kotlin/JVM values select the matching
`kotlinc -jvm-target` and Java-source `javac --release` (`"17"` or
`"jvm-target=17"`), and Rust values select the edition (`"2018"`, `"2021"`,
`"2024"`). Existing language aliases such as `C11`, `CPP20`, `JAVA17`,
`KOTLIN_JVM17`, and `RUST2021` remain supported.

## `POST /compile` — Response

```jsonc
{
  "status": "OK",                            // "OK" | "Compile Error" | "Timeout" | "Invalid Request" | "Internal Error"
  "artifacts": [                             // compiled outputs
    {
      "name": "Main",
      "data_b64": "<base64>",               // base64-encoded binary / bytecode
      "mode": "exec"                         // "exec" for executables, "" for data files
    }
  ],
  "stdout": "",                              // compiler stdout, capped at 1 MiB
  "stderr": "",                              // compiler stderr / warnings, capped at 1 MiB
  "stdout_truncated": false,                 // true when compiler stdout exceeded the capture cap
  "stderr_truncated": false,                 // true when compiler stderr exceeded the capture cap
  "reason": "",                              // human-readable error
  "reason_code": ""                          // optional machine-readable reason, e.g. "memory_limit_exceeded"
}
```

Compiler `stdout` and `stderr` are intentionally user-facing diagnostics and
are returned as produced, subject only to the 1 MiB capture caps. Redacting
normal compiler diagnostics is a non-goal. For `Internal Error` only, the
human-readable `reason` may replace deployment-specific compile workspace paths
with `$WORKDIR`; operators should use server logs for the raw infrastructure
error.

## `POST /execute` — Request

```jsonc
{
  "lang": "binary",                          // runtime language key (see Supported Languages)
  "binaries": [                              // files to place in work directory (max 512 entries)
    {
      "name": "Main",                       // filename
      "data_b64": "<base64>",               // base64-encoded content
      "mode": "exec"                         // "exec" → chmod 0555; otherwise chmod 0444
    }
  ],
  "stdin": "hello\n",                        // input fed to process stdin (max 64 MiB)
  "expected_stdout": "hello\n",              // expected output for built-in diff (max 64 MiB)
  "limits": {
    "time_ms": 2000,                         // wall-clock time limit, 1..600000 ms
    "memory_mb": 256,                        // memory limit, 1..4096 MB
    "output_bytes": 65536,                   // optional stdout/stderr capture cap, 0..67108864
    "workspace_bytes": 134217728             // optional workspace cap, 0..1073741824
  },
  "capture_limits": {                        // optional response/log clipping; each member is 0..8388608
    "stdout_bytes": 1024,                    // explicit 0 suppresses returned/logged stdout
    "stderr_bytes": 1024                     // explicit 0 suppresses returned/logged stderr
  },
  "problem_id": "contest-1/a",               // optional problem policy key for server-selected runtime profile
  "runtime_profile": "low-memory",           // optional operator-defined runtime tuning profile
  "python_library_mode": "stdlib",           // optional request-wide Python imports: "stdlib" | "installed"
  "enable_network": false,                   // outbound network request flag; honored only when server policy allows request-controlled network
  "emit_logs": true,                         // optional contestant stdout/stderr log SSE events; defaults to true
  "entry_point": "src/main.py",              // optional submitted file path to run; some runtimes use a class/module/procedure/top-level name
  "spj": {                                   // optional special judge
    "binary": {                              // pre-compiled SPJ binary
      "name": "checker",
      "data_b64": "<base64>",
      "mode": "exec"
    },
    "lang": "binary",                        // SPJ runtime language
    "emit_score": true,                      // SPJ outputs float score to stdout
    "limits": {                              // optional SPJ-specific limits
      "time_ms": 1000,
      "memory_mb": 256
    }
  },
  "interactor": {                            // optional interactive IO judge
    "lang": "binary",                        // interactor runtime language
    "binaries": [                            // pre-compiled interactor artifacts
      {"name": "interactor", "data_b64": "<base64>", "mode": "exec"}
    ],
    "entry_point": "interactor.py",          // optional interactor entry point
    "limits": {                              // optional interactor-specific limits
      "time_ms": 2000,
      "memory_mb": 256
    }
  },
  "file_outputs": [                          // read program output from file instead of stdout (at most one path)
    {"path": "output.txt"}
  ],
  "sidecar_outputs": [                       // capture extra files after execution (max 64 paths)
    {"path": "__img__/images.jsonl"}
  ],
  "ignore_tle": false                        // evaluate output even on TLE
}
```

`binaries` may contain multiple files. They are materialized into the same
working directory, so scripts can read adjacent data files such as CSV fixtures
by relative path. For path-based runtimes (`binary`, Python, Ruby, JavaScript,
text, and similar), `entry_point` must be a submitted file path and selects the
primary file to execute. Java, Scala, and Groovy use it as a class name; Erlang
uses a module/function entry; GDL uses a procedure name; and VHDL uses a top
level. These non-path values are validated before they reach generated
manifests or command arguments. Because GDL sources are compiled through its
interactive command channel, submitted GDL paths are limited to ASCII letters,
digits, `.`, `_`, `-`, and `/`.

`limits.time_ms` and `limits.memory_mb` are required and bounded at the API
boundary. Optional `limits.output_bytes` and `limits.workspace_bytes` default to
server-side values when `0` or omitted, but values above the hard API caps are
rejected before the request enters the run queue. `spj.limits` uses the same
upper caps; omitted or zero SPJ fields fall back to SPJ defaults.
`interactor.limits` uses the same caps; omitted values inherit the contestant
time/output policy and use safe memory/workspace defaults where needed.
`capture_limits` is separate from `limits.output_bytes`. An omitted object or
member retains the legacy response cap of `min(limits.output_bytes, 64 KiB)`;
an explicit `0` returns no content for that stream. These limits clip
top-level and step `stdout`/`stderr` fields and `/execute` `log` events only.
They do not change output comparison, SPJ input, output-limit verdicts, or
step handoff bytes. `stdout_truncated` and `stderr_truncated` are true when
either the execution boundary or the smaller response capture boundary was
exceeded. Output-derived interactive and sandbox-initialization reasons obey
the stderr response cap; explicit zero retains a short structural reason
instead of copying stderr.
`emit_logs` defaults to `true` for backward compatibility. Setting it to
`false` suppresses contestant stdout/stderr `log` SSE events for the whole
request without changing output capture, judging, or the final `result`
payload.
`runtime_profile`, when present, must name a profile configured by the runner
operator through `AONOHAKO_RUNTIME_TUNING_PROFILES`; it selects only bounded
numeric tuning values and cannot pass arbitrary runtime flags. Non-dev servers
accept it only when `AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE=true`, so public
entry points can keep profile selection behind trusted problem policy.
`problem_id`, when present, is a policy key looked up in
`AONOHAKO_PROBLEM_RUNTIME_PROFILES`; mapped problems receive their configured
profile before the request enters the stream or runner queue.

`python_library_mode` applies to every Python target in the request, including
two-step programs, interactors, and Python SPJs. `stdlib` runs Python with
environment, user-site, and automatic `site` loading disabled; submitted
sibling modules and the Python standard library remain importable. `installed`
also exposes packages installed in the runtime image. The server defaults to
`stdlib`, unless `AONOHAKO_DEFAULT_PYTHON_LIBRARY_MODE` says otherwise.
When that default is `stdlib`, request-selected `installed` mode is accepted
only when `AONOHAKO_ALLOW_REQUEST_PYTHON_INSTALLED_LIBRARIES=true` or when
`AONOHAKO_PROBLEM_PYTHON_LIBRARY_MODES` maps the request's `problem_id` to
`installed`. A request value that conflicts with a problem mapping is rejected
before stream and queue admission. Supplying this field when no Python target
exists is also invalid.

Source, binary, expected-output, and stdin fields that support a `*_url` or
`data_url` alternative may fetch only public HTTP(S) destinations. URL
credentials and destinations resolving to loopback, private, link-local,
multicast, unspecified, or reserved address space are rejected at connection
time and after every redirect. Execute binaries in the top-level, `programs`,
SPJ, and interactor fields share one request-wide 48 MiB decoded budget across
inline base64 and URL-backed payloads; invalid collection counts or paths are
rejected before any outbound request is made.

### Two-step execute mode

`/execute` also accepts a two-step pipeline shape without a separate endpoint.
When `programs` or `steps` is present, the legacy top-level `lang`, `binaries`,
`stdin`, `limits`, `entry_point`, and `enable_network` fields must be omitted.
Top-level `expected_stdout`, `spj`, `file_outputs`, `sidecar_outputs`,
`capture_limits`, `emit_logs`, `ignore_tle`, `problem_id`, and
`runtime_profile` still apply to the request or final step as appropriate.
`capture_limits` clips exposed step/result fields but never reduces
`handoff.max_bytes`. `interactor` is not supported with two-step execute mode.

```jsonc
{
  "programs": [
    {
      "id": "encoder",
      "lang": "binary",
      "binaries": [{"name": "encoder", "data_b64": "<base64>", "mode": "exec"}]
    },
    {
      "id": "decoder",
      "lang": "python",
      "binaries": [{"name": "decoder.py", "data_b64": "<base64>"}],
      "entry_point": "decoder.py"
    }
  ],
  "steps": [
    {
      "id": "encode",
      "program_id": "encoder",
      "stdin": "original input\n",
      "limits": {"time_ms": 1000, "memory_mb": 256},
      "handoff": {
        "id": "encoded",
        "from": "stdout",
        "max_bytes": 1048576
      }
    },
    {
      "id": "decode",
      "program_id": "decoder",
      "stdin_from": "encoded",
      "limits": {"time_ms": 1000, "memory_mb": 256}
    }
  ],
  "expected_stdout": "answer\n"
}
```

The current API intentionally supports exactly two steps. Each step runs in a
fresh sandbox workspace; the first step handoff is stored in a bounded
temporary handoff file and streamed into the second step stdin. `handoff.from`
defaults to `stdout`; `file`/`file_output` handoff
requires `handoff.path` and captures that file through the same symlink-safe
output path as `file_outputs`. `handoff.max_bytes` defaults to the step output
capture limit and is capped at 64 MiB. A step that sets `stdin_from` must not
also set `stdin`; the handoff stream is the only stdin source for that step.
All `stdin_url` and text-part `data_url` downloads used by the two steps share
one cumulative 60-second download budget. Time spent running a sandbox between
completed downloads does not consume that budget.

### Communication execute mode

`communication-v1` runs one private manager and 2–64 independent participant
processes. The request contains each compiled artifact only once; the runner
materializes the participant program once and copies its immutable bytes to
distinct inodes in separate workspaces before launching the requested number
of processes. The executable basename and sandbox profile are selected by the
server, not by submitted artifact names.

```jsonc
{
  "programs": [
    {
      "id": "participant",
      "lang": "binary",
      "binaries": [{"name": "Main", "data_b64": "<base64>", "mode": "exec"}]
    },
    {
      "id": "manager",
      "lang": "binary",
      "binaries": [{"name": "manager", "data_b64": "<base64>", "mode": "exec"}]
    }
  ],
  "communication": {
    "version": 1,
    "participant_program_id": "participant",
    "manager_program_id": "manager",
    "participant_count": 64,
    "result_protocol": "manager-result-v1",
    "input_url": "https://storage.example/input",
    "answer_url": "https://storage.example/answer"
  },
  "limits": {"time_ms": 2000, "memory_mb": 256}
}
```

Only native `binary` programs are accepted. Communication mode is mutually
exclusive with legacy execute fields, steps, SPJ, interactive mode, network,
file/sidecar output capture, and `ignore_tle`. `input`/`input_url` and the
optional `answer`/`answer_url` pairs are mutually exclusive. `limits` applies
to every participant. Manager and aggregate ceilings are runner policy: the
manager currently receives 512 MiB and up to the participant time plus one
second, within the public time cap.

Every participant receives its zero-based participant ID as the final command
argument. The manager receives these arguments in order:

1. private input path;
2. private answer path (an empty file when no answer is supplied);
3. `/proc/self/fd/<n>` for its dedicated result descriptor;
4. participant count;
5. for each participant from ID 0 upward, a read descriptor path for that
   participant's stdout followed by a write descriptor path for its stdin.

The manager must write exactly one UTF-8 JSON object of at most 64 KiB to the
result descriptor and close it before exiting successfully:

```json
{"verdict":"accepted","score":0.725,"message":""}
```

`verdict` is `accepted` or `wrong_answer`, `score` is required and finite in
`[0,1]`, and `message` is at most 8 KiB. A manager start/runtime failure or an
invalid/missing result is `Runtime Error` with a `manager:*` verdict source.
Participant runtime, time, and memory failures retain the ordinary RE/TLE/MLE
statuses. This protocol does not introduce a judge-error status.

Clients must first require `communication-v1` from `GET /capabilities`. The
capability is advertised by Cloud Run embedded helpers only when the dedicated
runner sets `AONOHAKO_COMMUNICATION_ENABLED=true`; they use process-group
cleanup and the outer container as the aggregate resource boundary, and by
self-hosted embedded helpers with a configured cgroup v2 parent. Remote
control-plane-only instances and self-hosted helpers without a cgroup do not
advertise it and must not receive communication requests. A Cloud Run runner
must be provisioned for all participant memory limits, the 512 MiB manager,
server overhead, and its bounded work-root volume; an outer-container OOM may
terminate the instance before a per-participant verdict can be returned.
Startup requires a positive `AONOHAKO_COMMUNICATION_MEMORY_BUDGET_MB` when the
Cloud Run capability is enabled. Before starting processes, Aonohako requires
`participant_count * limits.memory_mb + 512` to fit that budget; the planned
32 GiB service uses `24576` MiB so the remainder stays available to the server,
workspaces, and runtime overhead. `AONOHAKO_COMMUNICATION_MAX_PARTICIPANTS`
defaults to the protocol maximum of 64 and may be lowered for a smaller
dedicated runner tier; requests above the configured limit fail admission
before any process starts.
Cloud Run also requires `AONOHAKO_COMMUNICATION_CPU_COUNT` to match the process
`GOMAXPROCS` value and a positive `AONOHAKO_COMMUNICATION_WALL_BUDGET_MS`.
The wall allowance reserves one configured CPU for supervision, rounds the
remaining targets into scheduling waves, and adds 15% or at least one second
of slack. Each participant and manager keeps its original CPU watchdog and
`RLIMIT_CPU`; a request whose adjusted allowance exceeds the session budget is
rejected before launch. Communication targets use a fixed native-binary
profile, cannot create threads or processes, and receive distinct artifact
inodes. On Cloud Run, participants are capped at 8 MiB of total workspace,
the manager at 128 MiB, and admission reserves 20% of the authoritative work
root. Self-hosted cgroup timing is unchanged.

## `POST /execute` — Response

```jsonc
{
  "status": "Accepted",                     // Accepted|Wrong Answer|Time Limit Exceeded|Memory Limit Exceeded|Workspace Limit Exceeded|Runtime Error|Container Initialization Failed
  "time_ms": 42,                            // compatibility alias for wall_time_ms
  "wall_time_ms": 42,                       // wall-clock time from CLOCK_MONOTONIC (ms)
  "cpu_time_ms": 17,                        // CPU time from process CPU clock when available (ms)
  "memory_kb": 8192,                        // best observed target peak memory (RSS/cgroup, KB)
  "exit_code": 0,                           // nullable; process exit code
  "stdout": "",                             // truncated stdout (up to capture_limits.stdout_bytes when set, on WA/RE only)
  "stderr": "",                             // truncated stderr (up to capture_limits.stderr_bytes when set, on non-zero exit only)
  "stdout_truncated": false,                // true when stdout exceeded the capture cap
  "stderr_truncated": false,                // true when stderr exceeded the capture cap
  "reason": "",                             // human-readable error
  "verdict_source": "stdout",               // source that selected the final verdict, when known
  "score": null,                            // nullable float 0.0–1.0 (SPJ, interactive, or communication score)
  "started_participants": 64,               // communication-v1 only; participant targets whose exec was confirmed
  "steps": [                                // present for two-step or interactive execute mode
    {
      "id": "encode",
      "program_id": "encoder",
      "status": "Accepted",
      "wall_time_ms": 12,
      "cpu_time_ms": 8,
      "memory_kb": 4096,
      "handoff_bytes": 128
    },
    {
      "id": "decode",
      "program_id": "decoder",
      "status": "Accepted",
      "wall_time_ms": 30,
      "cpu_time_ms": 22,
      "memory_kb": 8192
    }
  ],
  "sidecar_outputs": [                      // captured sidecar files
    {"path": "result.txt", "data_b64": "<base64>"}
  ],
  "sidecar_errors": [                       // optional diagnostics for rejected sidecars
    {"path": "debug.txt", "reason": "file too large"}
  ]
}
```

## Output Comparison

The built-in comparator (used when no SPJ is provided):

1. Split both expected and actual output by `\n`
2. Trim trailing whitespace (`\t`, ` `, `\r`) from each line
3. Drop trailing blank lines
4. Compare line-by-line (exact byte match)

When `file_outputs` is present:

- at most one path is supported
- the captured file replaces process stdout for judging and returned `stdout`
- capture failure is reported as `Runtime Error` instead of silently falling
  back to process stdout

## Special Judge (SPJ)

When `spj` is provided, the SPJ binary is invoked as:

```
<spj_binary> <input_file> <user_output_file> <expected_output_file>
```

- The SPJ runs from a clean SPJ-only workspace, not the participant writable
  directory
- The input, user output, and expected output files are read-only for the SPJ
- The SPJ uses `spj.limits` when provided; otherwise it defaults to a fixed
  1000 ms / 256 MiB policy instead of inheriting contestant limits
- User output is passed through the second argv file path, not duplicated on
  SPJ stdin
- Exit code 0 → accepted; non-zero → wrong answer
- If `emit_score: true`, SPJ should print a float (0.0–1.0) to stdout

## Interactive IO Judge

When `interactor` is provided, `aonohako` starts the contestant and interactor
at the same time in separate sandbox workspaces. Contestant stdout is streamed
to interactor stdin, and interactor stdout is streamed to contestant stdin.

The interactor is invoked as:

```
<interactor_command> <input_file> <output_file> <answer_file>
```

- `input_file` contains the request `stdin`
- `answer_file` contains `expected_stdout`
- `output_file` is an interactor-writable path for optional protocol logging
- Interactor exit code `0` → accepted
- Interactor exit code `3` → runtime error / interactor failure
- Other non-zero interactor exit codes → wrong answer
- `spj`, `file_outputs`, `ignore_tle`, and two-step `programs`/`steps` cannot
  be combined with `interactor`
- `sidecar_outputs` still capture files from the contestant workspace

## Supported Languages

### Compile kinds

| Language key | Compile kind | Compiler / tool |
|---|---|---|
| C, C89, C99, C11, C17, C18, C23 | `c` | `gcc -O2 -Wall -lm --static -DONLINE_JUDGE=1 -std=<std>` |
| CPP, CPP03–CPP26 | `cpp` | `g++ -O2 -Wall -lm --static -pipe -DONLINE_JUDGE=1 -std=<std>` |
| RUST, RUST2015–2024 | `rust` | `rustc --edition <ed> -O --cfg ONLINE_JUDGE` |
| MOONBIT | `moonbit` | `moon build --target native --release --strip --frozen --jobs 1` in a fixed dependency-free module |
| FENNEL | `fennel` | `fennel --no-fennelrc --require-as-include --compile <source>` via the hardened artifact writer |
| CHAPEL | `chapel` | `CHPL_COMM=none CHPL_TASKS=qthreads CHPL_TARGET_CPU=none chpl --local --fast -o <target> <source>` |
| ALGOL68 | `algol68` | `a68g --quiet --no-compile -O0 --check --file <source> --no-pragmats` |
| KOKA | `koka` | `koka --compile -O2 --no-debug -j1 -v0 --console=raw --no-autoinstall --cc=/usr/bin/gcc-16 "--ccopts=-march=x86-64 -mtune=generic" "--cclinkopts=-march=x86-64 -mtune=generic" --builddir=.aonohako-koka-build --output=Main main.kk` |
| PONY | `pony` | `ponyc --cpu=generic --output=. --bin-name=Main .` |
| GO | `go` | `go build -tags=online_judge,ONLINE_JUDGE` |
| ZIG | `zig` | `zig build-exe -O ReleaseSafe -femit-bin=<target>` |
| ASM | `binary` | `gcc -nostdlib -static -no-pie` |
| NASM | `binary` | `nasm -felf64 -dONLINE_JUDGE=1` + `gcc -nostdlib -static -no-pie` |
| JAVA, JAVA8–21 | `java` | `javac --release <v>` |
| GROOVY | `groovy` | `groovyc -d <dir>` |
| SCALA | `scala` | `scalac -d <dir>` |
| CLOJURE | `clojure` | `clojure` reader parse loop |
| RACKET | `racket` | `raco make` |
| TCL | `tcl` | Pass-through artifacts (requires at least one `.tcl`) |
| PYTHON3 | `python` | `python3 -I -S -m compileall` |
| PYPY3 | `pypy` | `pypy3 -I -S -m compileall` |
| JAVASCRIPT | `javascript` | `node --check` |
| COFFEESCRIPT | `coffeescript` | `coffee --compile --bare` |
| RESCRIPT | `rescript` | `rescript build` |
| PURESCRIPT | `purescript` | `spago build` plus a Node wrapper for `Main.main` |
| TYPESCRIPT | `typescript` | `tsc` |
| DENO | `deno` | `deno check --v8-flags=--max-old-space-size=...` |
| ELM | `elm` | `elm make <source> --output <target>` plus a Node wrapper for `stdin`/`stdout`/`stderr`/`exit` ports |
| KOTLIN | `kotlin` | `kotlinc-native -J-Xms64m -J-Xmx<compiler cap> -J-Xss1m -J-XX:+UseSerialGC -J-XX:ReservedCodeCacheSize=32m -J-XX:MaxMetaspaceSize=192m -J-XX:CompressedClassSpaceSize=64m -opt` |
| KOTLIN_JVM, KOTLIN_JAVA, KOTLIN_JVM8–21, KOTLIN_JAVA8–21 | `kotlin-jvm` | `kotlinc -J-Xms64m -J-Xmx<compiler cap> -J-Xss1m -J-XX:+UseSerialGC -jvm-target <v>` plus `javac --release <v>` for submitted `.java` files |
| PASCAL | `pascal` | `fpc -O2 -Xs -dONLINE_JUDGE` |
| DELPHI | `delphi` | `fpc -Mdelphi -O2 -Xs -dONLINE_JUDGE` |
| OBJECTPASCAL | `objectpascal` | `fpc -Mobjfpc -O2 -Xs -dONLINE_JUDGE` |
| NIM | `nim` | `nim c -d:release -d:ONLINE_JUDGE --opt:speed` |
| ADA | `ada` | `gnatmake -O2` |
| COBOL, GNUCOBOL | `cobol` | `cobc -x -free -O2` |
| CYTHON | `cython` | `cython3 --embed` + `gcc -O2 -pipe -DONLINE_JUDGE=1` |
| DART | `dart` | `dart compile exe -D ONLINE_JUDGE=true` |
| FORTRAN | `fortran` | `gfortran -O2 -pipe` |
| D | `d` | `ldc2 -O3 -release --d-version=ONLINE_JUDGE` |
| OBJECTIVE_C, OBJC | `objective-c` | `clang -x objective-c -O2 -pipe -DONLINE_JUDGE=1 -lobjc` |
| OBJECTIVE_CPP, OBJCPP | `objective-cpp` | `clang++ -x objective-c++ -O2 -pipe -DONLINE_JUDGE=1 -lobjc` |
| HASKELL | `haskell` | `ghc -O2` |
| IDRIS2 | `idris2` | `idris2 --cg chez -o <target>` |
| SML | `sml` | `mlton -output <target>` |
| HAXE | `haxe` | `haxe -D ONLINE_JUDGE -main Main -neko <target>` |
| SWIFT | `swift` | `swiftc -O -D ONLINE_JUDGE -module-cache-path <workdir>/.cache/swift-module-cache` |
| SQLITE | `sqlite` | Pass-through artifacts (requires at least one `.sql`) |
| DUCKDB | `duckdb` | Pass-through artifacts (requires at least one `.sql`) |
| JULIA | `julia` | Pass-through artifacts (requires at least one `.jl`) |
| RAKU | `raku` | `raku -c` |
| R | `r` | `/usr/lib/R/bin/exec/R --vanilla --slave -e parse(file=commandArgs(TRUE)[1]) --args <file>` |
| ERLANG | `erlang` | `erlc -o <dir>` |
| MERCURY | `mercury` | `mmc --make --grade hlc.gc -o <target>` |
| PROLOG | `prolog` | `swipl -q -f none -g halt -t halt` |
| LISP | `lisp` | `sbcl --load ... --eval '(quit)'` |
| PICOLISP | `picolisp` | Pass-through artifacts (requires at least one `.l`) |
| COQ, ROCQ | `rocq` | `coqc -q` / Rocq-compatible proof check |
| LEAN, LEAN4 | `lean4` | `lean` check |
| AGDA | `agda` | `agda` check |
| DAFNY | `dafny` | `dafny verify --cores 1` |
| TLA, TLAPLUS | `tla` | TLA+ model/tooling pass-through plus runner wrapper |
| WHY3, WHYML | `why3` | `why3` proof wrapper |
| ISABELLE | `isabelle` | `isabelle process_theories -o naproche_server=false -D .` |
| FSTAR | `fstar` | `fstar.exe` verification |
| ALLOY | `alloy` | Alloy CLI execution plus receipt validation |
| ACL2 | `acl2` | ACL2 batch proof wrapper |
| KFRAMEWORK | `kframework` | `kompile --backend llvm` definition check |
| OCAML | `ocaml` | `ocamlopt` |
| ELIXIR | `elixir` | `elixir` parse check |
| CSHARP | `csharp` | `dotnet publish -p:DefineConstants=ONLINE_JUDGE` or direct `csc -define:ONLINE_JUDGE` |
| FSHARP | `fsharp` | `dotnet publish -p:DefineConstants=ONLINE_JUDGE` or direct `fsc --define:ONLINE_JUDGE` |
| VBNET, VB | `vbnet` | `dotnet publish -p:DefineConstants=ONLINE_JUDGE` or direct `vbc -define:ONLINE_JUDGE=True` |
| RUBY | `ruby` | `ruby -c` |
| PHP | `php` | `php -l` |
| LUA | `lua` | `luac5.4 -p` |
| PERL | `perl` | `perl -c` |
| VB6 | `vb6` | Pass-through VB6 source artifacts |
| FREEBASIC | `freebasic` | `fbc -d ONLINE_JUDGE -x` |
| CLASSIC_BASIC, QBASIC | `classic-basic` | `fbc -lang qb -d ONLINE_JUDGE -x` |
| SMALLTALK, GST | `smalltalk` | Pass-through Smalltalk source artifacts |
| GOLFSCRIPT | `golfscript` | Pass-through GolfScript source artifacts |
| MOJO | `mojo` | `mojo build -o <target>` |
| GLEAM | `gleam` | `gleam build` |
| CUDA_OCELOT | `cuda-ocelot` | `aonohako-cuda-ocelot-build` |
| CARBON | `carbon` | `carbon compile --optimize=speed --no-debug-info` followed by `carbon --prebuilt-runtimes=<image runtime tree> link` with the image's prebuilt Core objects |
| GRAPHQL | `graphql` | Pass-through `.graphql` artifacts |
| GDL | `gdl` | Pass-through `.pro` artifacts |
| OCTAVE | `octave` | Pass-through `.m` artifacts |
| VHDL | `vhdl` | `ghdl -a`, `ghdl -e` |
| VERILOG, SYSTEMVERILOG | `verilog` | `iverilog -g2012 -DONLINE_JUDGE=1` |
| CRYSTAL | `crystal` | `crystal build --release --no-debug --define ONLINE_JUDGE` |
| VALA | `vala` | `valac -O --define=ONLINE_JUDGE -o <target>` |
| VLANG | `vlang` | `v -d ONLINE_JUDGE -o <target>` |
| ODIN | `odin` | `odin build . -define:ONLINE_JUDGE=true` |
| C3 | `c3` | `c3c compile -D ONLINE_JUDGE` |
| HARE | `hare` | `hare build -o <target>` |
| SED | `sed` | `sed -n -f` syntax check |
| BC | `bc` | Pass-through artifacts (requires at least one `.bc`) |
| BEFUNGE | `befunge` | Pass-through artifacts (requires `.bef` or `.bf93`) |
| APECODE | `apecode` | `apecc -o <target> <source.ape>` |
| FORTH, GFORTH | `forth` | Pass-through artifacts (requires `.fs`, `.fth`, or `.4th`) |
| BQN | `bqn` | Pass-through BQN source artifacts |
| APL, GNU_APL | `apl` | Pass-through APL source artifacts |
| J | `j` | Pass-through J source artifacts |
| UIUA | `uiua` | Pass-through Uiua source artifacts |
| JANET | `janet` | Pass-through Janet source artifacts |
| WHITESPACE | `whitespace` | Structural validation (whitespace-only source) |
| BF | `brainfuck` | Bracket-balance validation |
| LOLCODE | `lolcode` | Pass-through artifacts (requires `.lol`) |
| WASM | `wasm` | `wat2wasm` or `wasm-validate` |
| AHEUI | `aheui` | Pass-through artifacts |
| UHMLANG, TEXT | `none` | Pass-through |

### Runtime languages

| Runtime lang | Executor |
|---|---|
| `binary` | Direct native execution, including linked Carbon submissions |
| `go-binary` | Direct execution for compiled Go artifacts, with Go-compatible virtual-memory policy |
| `clojure` | `java <JVM memory flags> -cp /usr/share/java/clojure-1.12.jar clojure.main <file>` |
| `racket` | `racket <file>` |
| `scheme` | `chibi-scheme <file>` |
| `awk` | `gawk --sandbox -f <file>` |
| `tcl` | `tclsh <file>` |
| `python` | `python3 <file>` |
| `pypy` | `pypy3 <file>` |
| `groovy` | `java <JVM memory flags> -cp <classes:groovy jars> <MainClass>` |
| `scala` | `java <JVM memory flags> -cp <classes:scala jars> <MainClass>` |
| `java` | `java <JVM memory flags> -jar <file>` |
| `kotlin-jvm` | `java <JVM memory flags> -jar <file>` |
| `erlang` | `env ERL_AFLAGS=... erlexec/erl +S ... +A ... -noshell -pa <dir> -s <module> <function> -s init stop` |
| `prolog` | `swipl -q -f <file> -g main -t halt` |
| `lisp` | `sbcl --script <file>` |
| `picolisp` | `pil <file> -bye` |
| `rocq` | `true` (verification is completed during compile) |
| `lean4` | `true` (verification is completed during compile) |
| `agda` | `true` (verification is completed during compile) |
| `dafny` | `true` (verification is completed during compile) |
| `tla` | `aonohako-tla-run <file>` |
| `why3` | `true` (verification is completed during compile) |
| `isabelle` | `true` (verification is completed during compile) |
| `fstar` | `true` (verification is completed during compile) |
| `alloy` | `true` (verification is completed during compile) |
| `acl2` | `true` (verification is completed during compile) |
| `kframework` | `true` (verification is completed during compile) |
| `javascript` | `node --disable-wasm-trap-handler --max-old-space-size=... --max-semi-space-size=... --stack-size=2048 <file>` |
| `deno` | `deno run --no-prompt --v8-flags=--max-old-space-size=... <file>` |
| `gdl` | `aonohako-gdl-run <file> <entry>` |
| `octave` | `octave-cli --quiet --no-gui --no-history --no-init-file --no-init-path <file>` |
| `r` | `Rscript --vanilla <file>` |
| `raku` | `raku <file>` |
| `ruby` | `ruby <file>` |
| `php` | `php <file>` |
| `lua` | `lua5.4 <file>` |
| `perl` | `perl <file>` |
| `ocaml` | `env OCAMLRUNPARAM=s=32k <file>` |
| `elixir` | `env ERL_AFLAGS=... erlexec ... -s elixir start_cli -extra <file>` or `elixir <file>` fallback |
| `gleam` | `aonohako-gleam-run <workspace>` |
| `haxe` | `neko <file>` |
| `sqlite` | `sqlite3 <workspace-db> ".read /dev/stdin" ".read <file>"`; setup SQL from stdin runs before the submitted file |
| `duckdb` | `aonohako-duckdb-run <file>`; setup SQL from stdin runs before the submitted file |
| `sed` | `sed -f <file>` |
| `bc` | `bc -q <file>` |
| `forth` | `gforth <file> -e bye` |
| `julia` | `julia --startup-file=no --history-file=no <file>` |
| `uhmlang` | `env GOMEMLIMIT=... GOGC=... /usr/bin/umjunsik-lang-go <file>` |
| `csharp`, `fsharp`, `vbnet` | `env DOTNET_GCHeapHardLimit=... DOTNET_EnableDiagnostics=0 COMPlus_EnableDiagnostics=0 dotnet <file>` or direct |
| `vb6` | `aonohako-vb6-run <file>` |
| `vhdl` | `aonohako-vhdl-run <workspace> <entity>` |
| `verilog` | `vvp <file>` |
| `c3` | direct compiled binary execution with C3 runtime allowances |
| `cuda-ocelot` | CUDA Ocelot emulator wrapper |
| `graphql` | GraphQL validation wrapper |
| `smalltalk` | `gst <file>` |
| `golfscript` | GolfScript runner wrapper |
| `bqn` | `bqn <file>` |
| `apl` | `node --disable-wasm-trap-handler --max-old-space-size=... --max-semi-space-size=... --stack-size=... /usr/local/bin/apl --script -f <file>` |
| `j` | `aonohako-j <file>` |
| `uiua` | `uiua run <file> --no-format` |
| `janet` | `janet <file>` |
| `whitespace` | `python3 /usr/local/lib/aonohako/whitespace.py <file>` |
| `befunge` | `python3 /usr/local/lib/aonohako/befunge.py <file>` |
| `brainfuck` | `python3 /usr/local/lib/aonohako/brainfuck.py <file>` |
| `lolcode` | `lci <file>` |
| `wasm` | `wasmtime run --dir=. -O memory-reservation=... -O memory-reservation-for-growth=0 -O memory-guard-size=65536 -W max-memory-size=... -W max-memories=1 -W max-instances=1 -W max-tables=1 -W max-wasm-stack=1048576 <file>` |
| `aheui` | `python3 -c '<aheui entry_point wrapper>' <file>` |
| `text` | `cat <file>` |

The `COFFEESCRIPT` source profile compiles submissions to JavaScript and
therefore normalizes to the `javascript` execute key, regardless of identifier
case. The CoffeeScript compiler remains part of the corresponding runtime image
for the compile phase and toolchain smoke tests.

## Resource Enforcement

| Mechanism | What it limits |
|---|---|
| `RLIMIT_CPU` | CPU seconds (`time_ms / 1000 + 1`) as a helper-side hard stop |
| `RLIMIT_AS` | Virtual address space using language-specific headroom; compiled Go, Java, and .NET runtimes are compatibility exceptions because they reserve large virtual regions before user code |
| `RLIMIT_STACK` | Native stack and argument/environment footprint. The default uses the inherited hard stack limit, usually unlimited. |
| `RLIMIT_NOFILE` | Max open file descriptors (64 by default, raised only for known runtime needs) |
| `RLIMIT_NPROC` | Sandbox UID task count sized from the configured thread limit |
| `RLIMIT_FSIZE` | Per-file growth tied to workspace policy; .NET/Dafny get a high finite 2 TiB floor for CoreCLR/F# compatibility, so their practical disk-burst guard is workspace scanning plus bounded work-root/container storage |
| procfs RSS watchdog | Live RSS sampling through `statm`, refined with `smaps_rollup` near limits |
| optional cgroup v2 | `memory.max`, `pids.max`, `memory.swap.max=0` when available, `memory.oom.group=1`, and `cpu.max=100000 100000` |
| workspace scanner | Total file bytes plus entry count/depth caps |
| context timeout | Wall-clock kill via SIGKILL to process group |

### Runtime Measurements

- `wall_time_ms` uses `CLOCK_MONOTONIC`
- `cpu_time_ms` samples the Linux process CPU clock while the submission is
  running and uses cgroup `cpu.stat` when per-run cgroups are enabled
- `time_ms` is retained as a compatibility alias for `wall_time_ms`
- `verdict_source` is diagnostic and non-authoritative. Local helper runners
  may report values such as `stdout`, `file_output`, `spj`, `exit_code`,
  `signal`, `wait_status`, `sandbox_init`, `wall_time`, `cpu_time`,
  `cpu_time_final`, `cpu_time_cgroup`, `cpu_time_cgroup_final`, `cpu_rlimit`,
  `memory_rss`, `memory_cgroup`, `memory_cgroup_final`, `memory_reported`,
  `address_space`, `pids_cgroup`, `pids_cgroup_final`, `workspace_bytes`, `workspace_entries`,
  `workspace_depth`, or `workspace_scan` to explain which measurement or judge
  step selected the final status.
