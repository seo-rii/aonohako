# Payload Schema

## `POST /compile` — Request

```jsonc
{
  "lang": "CPP17",                           // language identifier (see Supported Languages)
  "sources": [                               // source files to compile
    {
      "name": "Main.cpp",                   // filename (relative, no path traversal allowed)
      "data_b64": "<base64>"                // base64-encoded file contents
    }
  ],
  "target": "Main",                          // optional output binary name (default: "Main")
  "entry_point": "Main"                      // optional entry point (used for Java)
}
```

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
  "lang": "binary",                          // runtime language: binary|python|pypy|java|javascript|ruby|php|lua|perl|uhmlang|csharp|text
  "binaries": [                              // files to place in work directory
    {
      "name": "Main",                       // filename
      "data_b64": "<base64>",               // base64-encoded content
      "mode": "exec"                         // "exec" → chmod 500; otherwise chmod 400
    }
  ],
  "stdin": "hello\n",                        // input fed to process stdin
  "expected_stdout": "hello\n",              // expected output for built-in diff
  "limits": {
    "time_ms": 2000,                         // wall-clock time limit (ms)
    "memory_mb": 256                         // memory limit (MB, enforced via prlimit AS)
  },
  "enable_network": false,                   // allow outbound network (default: false)
  "entry_point": "Main",                     // entry class for Java
  "spj": {                                   // optional special judge
    "binary": {                              // pre-compiled SPJ binary
      "name": "checker",
      "data_b64": "<base64>",
      "mode": "exec"
    },
    "lang": "binary",                        // SPJ runtime language
    "emit_score": true                       // SPJ outputs float score to stdout
  },
  "file_outputs": [                          // read program output from file instead of stdout
    {"path": "output.txt"}
  ],
  "sidecar_outputs": [                       // capture extra files after execution
    {"path": "__img__/images.jsonl"}
  ],
  "ignore_tle": false                        // evaluate output even on TLE
}
```

## `POST /execute` — Response

```jsonc
{
  "status": "Accepted",                     // Accepted|Wrong Answer|Time Limit Exceeded|Memory Limit Exceeded|Runtime Error|Container Initialization Failed
  "time_ms": 42,                            // compatibility alias for wall_time_ms
  "wall_time_ms": 42,                       // wall-clock time from CLOCK_MONOTONIC (ms)
  "cpu_time_ms": 17,                        // CPU time from process CPU clock when available (ms)
  "memory_kb": 8192,                        // peak RSS from getrusage (KB)
  "exit_code": 0,                           // nullable; process exit code
  "stdout": "",                             // truncated stdout (max 3000 bytes, on WA/RE only)
  "stderr": "",                             // truncated stderr (on non-zero exit only)
  "reason": "",                             // human-readable error
  "score": null,                            // nullable float 0.0–1.0 (SPJ score)
  "sidecar_outputs": [                      // captured sidecar files
    {"path": "result.txt", "data_b64": "<base64>"}
  ]
}
```

## Output Comparison

The built-in comparator (used when no SPJ is provided):

1. Split both expected and actual output by `\n`
2. Trim trailing whitespace (`\t`, ` `, `\r`) from each line
3. Drop trailing blank lines
4. Compare line-by-line (exact byte match)

## Special Judge (SPJ)

When `spj` is provided, the SPJ binary is invoked as:

```
<spj_binary> <input_file> <expected_output_file> <user_output_file>
```

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
| JAVA, JAVA8–15 | `java` | `javac --release <v>` |
| PYTHON3 | `python` | `python3 -m compileall` |
| PYPY3 | `pypy` | `pypy3 -m compileall` |
| JAVASCRIPT | `javascript` | `node --check` |
| TYPESCRIPT | `typescript` | `tsc` |
| KOTLIN | `kotlin` | `kotlinc-native` |
| CSHARP | `csharp` | `dotnet publish` |
| RUBY | `ruby` | `ruby -c` |
| PHP | `php` | `php -l` |
| LUA | `lua` | `luac5.4 -p` |
| PERL | `perl` | `perl -c` |
| UHMLANG, TEXT | `none` | Pass-through |

### Runtime languages

| Runtime lang | Executor |
|---|---|
| `binary` | Direct execution |
| `python` | `python3 <file>` |
| `pypy` | `pypy3 <file>` |
| `java` | `java -jar <file>` |
| `javascript` | `node <file>` |
| `ruby` | `ruby <file>` |
| `php` | `php <file>` |
| `lua` | `lua5.4 <file>` |
| `perl` | `perl <file>` |
| `uhmlang` | `/usr/bin/umjunsik-lang-go <file>` |
| `csharp` | `dotnet <file>` or direct |
| `text` | `cat <file>` |

## Resource Enforcement

| Mechanism | What it limits |
|---|---|
| `prlimit --cpu` | CPU seconds (time_ms / 1000 + 1) |
| `prlimit --as` | Virtual address space (memory_mb + 64 MB, min 256 MB) |
| `prlimit --nofile` | Max open file descriptors (64) |
| `prlimit --fsize` | Max file size (32 MB) |
| `taskset -c 0` | Pin to single CPU core |
| Context timeout | Wall-clock kill via SIGKILL to process group |

### Runtime Measurements

- `wall_time_ms` uses `CLOCK_MONOTONIC`
- `cpu_time_ms` samples the Linux process CPU clock while the submission is running
- `time_ms` is retained as a compatibility alias for `wall_time_ms`
