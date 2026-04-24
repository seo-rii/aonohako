# Payload Schema

## `POST /compile` — Request

```jsonc
{
  "lang": "CPP17",                           // language identifier (see Supported Languages)
  "sources": [                               // source files to compile (max 512 entries)
    {
      "name": "Main.cpp",                   // filename (relative, no path traversal allowed)
      "data_b64": "<base64>"                // base64-encoded file contents
    }
  ],
  "target": "Main",                          // optional output binary name (default: "Main")
  "entry_point": "src/main.c"                // optional submitted source path to validate as the intended entry file
}
```

`sources` may contain multiple files. Every `name` is interpreted as a relative
path inside the compile workspace; absolute paths and traversal are rejected.
When `entry_point` names a source path, it must exactly match one submitted
source after path cleaning. Native multi-file compilers such as C/C++ still
compile all source files of the language, so `entry_point` is validation
metadata rather than an argument that drops helper sources.

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
  "stdout": "",                              // compiler stdout
  "stderr": "",                              // compiler stderr / warnings
  "reason": ""                               // human-readable error
}
```

## `POST /execute` — Request

```jsonc
{
  "lang": "binary",                          // runtime language: binary|python|pypy|java|javascript|ruby|php|lua|perl|ocaml|elixir|sqlite|julia|uhmlang|csharp|fsharp|text|clojure|racket|groovy|scala|erlang|prolog|lisp|coq|r|whitespace|brainfuck|wasm|aheui
  "binaries": [                              // files to place in work directory (max 512 entries)
    {
      "name": "Main",                       // filename
      "data_b64": "<base64>",               // base64-encoded content
      "mode": "exec"                         // "exec" → chmod 0555; otherwise chmod 0444
    }
  ],
  "stdin": "hello\n",                        // input fed to process stdin (max 16 MiB)
  "expected_stdout": "hello\n",              // expected output for built-in diff (max 16 MiB)
  "limits": {
    "time_ms": 2000,                         // wall-clock time limit, 1..60000 ms
    "memory_mb": 256,                        // memory limit, 1..4096 MB
    "output_bytes": 65536,                   // optional stdout/stderr capture cap, 0..8388608
    "workspace_bytes": 134217728             // optional workspace cap, 0..1073741824
  },
  "enable_network": false,                   // outbound network request flag; Cloud Run embedded helper rejects true, self-hosted helper allows outbound AF_INET/AF_INET6 clients only
  "entry_point": "src/main.py",              // optional submitted file path to run; JVM/BEAM runtimes use class/module entry names
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
primary file to execute. For Java, Scala, Groovy, and Erlang, `entry_point`
keeps its existing class/module meaning instead of selecting a file path; JVM
class names are validated before they are written into generated manifests or
command arguments.

`limits.time_ms` and `limits.memory_mb` are required and bounded at the API
boundary. Optional `limits.output_bytes` and `limits.workspace_bytes` default to
server-side values when `0` or omitted, but values above the hard API caps are
rejected before the request enters the run queue. `spj.limits` uses the same
upper caps; omitted or zero SPJ fields fall back to SPJ defaults.

## `POST /execute` — Response

```jsonc
{
  "status": "Accepted",                     // Accepted|Wrong Answer|Time Limit Exceeded|Memory Limit Exceeded|Workspace Limit Exceeded|Runtime Error|Container Initialization Failed
  "time_ms": 42,                            // compatibility alias for wall_time_ms
  "wall_time_ms": 42,                       // wall-clock time from CLOCK_MONOTONIC (ms)
  "cpu_time_ms": 17,                        // CPU time from process CPU clock when available (ms)
  "memory_kb": 8192,                        // peak RSS from getrusage (KB)
  "exit_code": 0,                           // nullable; process exit code
  "stdout": "",                             // truncated stdout (up to limits.output_bytes, on WA/RE only)
  "stderr": "",                             // truncated stderr (up to limits.output_bytes, on non-zero exit only)
  "stdout_truncated": false,                // true when stdout exceeded the capture cap
  "stderr_truncated": false,                // true when stderr exceeded the capture cap
  "reason": "",                             // human-readable error
  "score": null,                            // nullable float 0.0–1.0 (SPJ score)
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
<spj_binary> <input_file> <expected_output_file> <user_output_file>
```

- The SPJ runs from a clean SPJ-only workspace, not the participant writable
  directory
- The input, expected output, and user output files are read-only for the SPJ
- The SPJ uses `spj.limits` when provided; otherwise it defaults to a fixed
  1000 ms / 256 MiB policy instead of inheriting contestant limits
- User output is also piped to SPJ's stdin
- Exit code 0 → accepted; non-zero → wrong answer
- If `emit_score: true`, SPJ should print a float (0.0–1.0) to stdout

## Supported Languages

### Compile kinds

| Language key | Compile kind | Compiler / tool |
|---|---|---|
| C, C99, C11, C18 | `c` | `gcc -O2 -Wall -lm --static` |
| CPP, CPP03–CPP26 | `cpp` | `g++ -O2 -Wall -lm --static -pipe` |
| RUST, RUST2018–2024 | `rust` | `rustc --edition <ed> -O` |
| GO | `go` | `go build` |
| ZIG | `zig` | `zig build-exe -O ReleaseSafe` |
| ASM | `binary` | `gcc -nostdlib -static -no-pie` |
| NASM | `binary` | `nasm -felf64` + `gcc -nostdlib -static -no-pie` |
| JAVA, JAVA8–15 | `java` | `javac --release <v>` |
| GROOVY | `groovy` | `groovyc -d <dir>` |
| SCALA | `scala` | `scalac -d <dir>` |
| CLOJURE | `clojure` | `clojure` reader parse loop |
| RACKET | `racket` | `raco make` |
| PYTHON3 | `python` | `python3 -I -S -m compileall` |
| PYPY3 | `pypy` | `pypy3 -I -S -m compileall` |
| JAVASCRIPT | `javascript` | `node --check` |
| TYPESCRIPT | `typescript` | `tsc` |
| KOTLIN | `kotlin` | `kotlinc-native` |
| PASCAL | `pascal` | `fpc -O2 -Xs` |
| NIM | `nim` | `nim c -d:release --opt:speed` |
| ADA | `ada` | `gnatmake -O2` |
| DART | `dart` | `dart compile exe` |
| FORTRAN | `fortran` | `gfortran -O2 -pipe` |
| D | `d` | `ldc2 -O3 -release` |
| HASKELL | `haskell` | `ghc -O2` |
| SWIFT | `swift` | `swiftc -O` |
| SQLITE | `sqlite` | Pass-through artifacts (requires at least one `.sql`) |
| JULIA | `julia` | Pass-through artifacts (requires at least one `.jl`) |
| R | `r` | `Rscript --vanilla -e parse(...)` |
| ERLANG | `erlang` | `erlc -o <dir>` |
| PROLOG | `prolog` | `swipl -q -f none -g halt -t halt` |
| LISP | `lisp` | `sbcl --load ... --eval '(quit)'` |
| COQ | `coq` | `coqc -q` |
| OCAML | `ocaml` | `ocamlopt` |
| ELIXIR | `elixir` | `elixir` parse check |
| CSHARP | `csharp` | `dotnet publish` |
| FSHARP | `fsharp` | `dotnet publish` |
| RUBY | `ruby` | `ruby -c` |
| PHP | `php` | `php -l` |
| LUA | `lua` | `luac5.4 -p` |
| PERL | `perl` | `perl -c` |
| WHITESPACE | `whitespace` | Structural validation (whitespace-only source) |
| BF | `brainfuck` | Bracket-balance validation |
| WASM | `wasm` | `wat2wasm` or `wasm-validate` |
| AHEUI | `aheui` | Pass-through artifacts |
| UHMLANG, TEXT | `none` | Pass-through |

### Runtime languages

| Runtime lang | Executor |
|---|---|
| `binary` | Direct execution |
| `clojure` | `clojure <file>` |
| `racket` | `racket <file>` |
| `python` | `python3 <file>` |
| `pypy` | `pypy3 <file>` |
| `groovy` | `groovy -cp <dir> <MainClass>` |
| `scala` | `scala -classpath <dir> <MainClass>` |
| `java` | `java -jar <file>` |
| `erlang` | `erl -noshell -pa <dir> -s <module> <function> -s init stop` |
| `prolog` | `swipl -q -f <file> -g main -t halt` |
| `lisp` | `sbcl --script <file>` |
| `coq` | `coqc -q <file>` |
| `javascript` | `node <file>` |
| `r` | `Rscript --vanilla <file>` |
| `ruby` | `ruby <file>` |
| `php` | `php <file>` |
| `lua` | `lua5.4 <file>` |
| `perl` | `perl <file>` |
| `ocaml` | `env OCAMLRUNPARAM=s=32k <file>` |
| `elixir` | `env ERL_AFLAGS=+MIscs 128 +S 1:1 +A 1 elixir <file>` |
| `sqlite` | `sqlite3 <workspace-db> < <file>` |
| `julia` | `julia --startup-file=no --history-file=no <file>` |
| `uhmlang` | `/usr/bin/umjunsik-lang-go <file>` |
| `csharp`, `fsharp` | `dotnet <file>` or direct |
| `whitespace` | `python3 /usr/local/lib/aonohako/whitespace.py <file>` |
| `brainfuck` | `python3 /usr/local/lib/aonohako/brainfuck.py <file>` |
| `wasm` | `wasmtime run --dir=. <file>` |
| `aheui` | `sh -c 'aheui "$1" ...' sh <file>` |
| `text` | `cat <file>` |

## Resource Enforcement

| Mechanism | What it limits |
|---|---|
| `prlimit --cpu` | CPU seconds (time_ms / 1000 + 1) |
| `prlimit --as` | Virtual address space (memory_mb + 64 MB, min 512 MB) |
| `prlimit --nofile` | Max open file descriptors (64) |
| `prlimit --fsize` | Max file size (workspace_bytes when set, otherwise 128 MB); .NET gets a finite 512 MiB floor for runtime compatibility |
| workspace scanner | Total file bytes plus entry count/depth caps |
| `taskset -c 0` | Pin to single CPU core |
| Context timeout | Wall-clock kill via SIGKILL to process group |

### Runtime Measurements

- `wall_time_ms` uses `CLOCK_MONOTONIC`
- `cpu_time_ms` samples the Linux process CPU clock while the submission is running
- `time_ms` is retained as a compatibility alias for `wall_time_ms`
