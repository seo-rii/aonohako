# Language test coverage audit

Audited against `4f36f63` on 2026-10-08: all 139 catalog languages, 25 production profiles, and 192 source profiles. Every catalog language already had a compile/execute fixture, but those fixtures exercised only 191 distinct source profiles; `TEXT` was missing.

## Why GolfScript escaped

Before PR #53, the catalog smoke and API fixture both printed a string literal. The A+B guard explicitly exempted GolfScript because its compatibility runner only supported literals. That exemption described the incomplete implementation instead of requiring the language's input and arithmetic semantics. A literal-only runner could therefore pass every test. Most other languages already ran two input-dependent A+B examples, but `sed` also hardcoded the answers for exactly those two inputs.

The catalog checks had a second weakness: shell sequences and pipelines often discarded compiler/runtime exit statuses, and `grep` accepted one correct line amid unexpected stdout. Command substitutions inside `test` could discard a runtime failure even after enabling `set -e`.

## Strengthened checks

- All 120 A+B languages reuse the same compiled artifacts for different inputs, including zero, independent operands and decimal carry. Single-byte assembly/WASM fixtures use their explicitly bounded one-digit sum domain. Brainfuck and Uiua use two-digit operands; Brainfuck now handles carry and sums through 198. Befunge uses integer output instead of a two-character result.
- Every baseline source profile executes an intentionally incorrect expected output and must return `Wrong Answer`. Successful compilation must return artifacts. HTTP regression tests reject old-input-only runtimes, unconditional `Accepted`, empty artifacts, and unconditional compile success.
- Every A+B exception has additional conformance cases. Proof/definition checkers must reject invalid proofs or definitions; special runtimes exercise arithmetic, source selection, transformations or explicit rejection. GolfScript also checks compact `~+`, signed/tab-separated input and `+` stack underflow.
- Gleam, PureScript and Kotlin perform arithmetic in the submitted language while retaining the existing FFI/mixed-source checks.
- Shell smokes preserve exit status and compare complete solution stdout. Actual catalog scripts are tested with failing and noisy toolchain stand-ins. Installation configuration is unchanged.
- Inventory tests derive required source profiles from `profiles.All()` and require every A+B exception to have conformance fixtures. `TEXT` is executed explicitly in the `plain` image.

The matrix below lists every audited catalog language. The input count is per source profile; the last column counts additional conformance fixtures, each compiled/executed with that language's source variants. Every row also has the wrong-answer control.

## Input and contract limits

The shared A+B corpus focuses on zero, input dependence and carry; it is not a complete language conformance suite. Only the dedicated GolfScript case adds signed/whitespace input in this change. The one-digit assembly/WASM fixtures and two-digit Brainfuck/Uiua fixtures retain explicit operand-format bounds. APL and Zerolang use source-dependent arithmetic when their current runners expose no stable stdin contract. VB6 remains a literal console subset, and GraphQL exposes a fixed schema; the tests verify those boundaries rather than claiming general arithmetic support.

TLA has an additional valid two-state invariant case. A separate exit-status defect was found in its runtime wrapper and reported for scope approval; this test-only change does not yet add a failing-invariant assertion.

## Complete language inventory

| Catalog language | Source profiles exercised | Positive input contract/count | Extra conformance fixtures |
| --- | --- | --- | --- |
| `ada` | `ADA`, `ADA2012`, `ADA2022` | token input: 7 | 0 |
| `plain` | `C11`, `TEXT` | token input: 7; verbatim text | 1 |
| `c` | `C11`, `C`, `C89`, `C99`, `C17`, `C18`, `C23` | token input: 7 | 0 |
| `cpp` | `CPP17`, `CPP`, `CPP98`, `CPP03`, `CPP11`, `CPP14`, `CPP20`, `CPP23`, `CPP26` | token input: 7 | 0 |
| `aheui` | `AHEUI` | token input: 7 | 0 |
| `awk` | `AWK` | token input: 7 | 0 |
| `tcl` | `TCL` | token input: 7 | 0 |
| `asm` | `ASM` | single-digit input/sum: 6 | 0 |
| `bf` | `BF` | two-digit input: 8 | 0 |
| `befunge` | `BEFUNGE` | token input: 7 | 0 |
| `lolcode` | `LOLCODE` | line input: 7 | 0 |
| `apecode` | `APECODE` | documented exception: 1 | 2 |
| `j` | `J` | token input: 7 | 0 |
| `clojure` | `CLOJURE` | token input: 7 | 0 |
| `coq` | `COQ` | documented exception: 1 | 1 |
| `rocq` | `ROCQ` | documented exception: 1 | 1 |
| `lean4` | `LEAN4`, `LEAN` | documented exception: 1 | 1 |
| `agda` | `AGDA` | documented exception: 1 | 1 |
| `dafny` | `DAFNY` | documented exception: 1 | 1 |
| `tla` | `TLA`, `TLAPLUS` | documented exception: 1 | 1 |
| `why3` | `WHY3`, `WHYML` | documented exception: 1 | 1 |
| `isabelle` | `ISABELLE` | documented exception: 1 | 1 |
| `fstar` | `FSTAR` | documented exception: 1 | 1 |
| `alloy` | `ALLOY` | documented exception: 1 | 1 |
| `acl2` | `ACL2` | documented exception: 1 | 1 |
| `kframework` | `KFRAMEWORK` | documented exception: 1 | 1 |
| `csharp` | `CSHARP` | token input: 7 | 0 |
| `crystal` | `CRYSTAL` | token input: 7 | 0 |
| `cobol` | `COBOL` | token input: 7 | 0 |
| `gnucobol` | `GNUCOBOL` | token input: 7 | 0 |
| `cython` | `CYTHON` | token input: 7 | 0 |
| `objective-c` | `OBJECTIVE_C`, `OBJC` | token input: 7 | 0 |
| `objective-cpp` | `OBJECTIVE_CPP`, `OBJCPP` | token input: 7 | 0 |
| `vlang` | `VLANG` | token input: 7 | 0 |
| `vala` | `VALA` | token input: 7 | 0 |
| `odin` | `ODIN` | token input: 7 | 0 |
| `c3` | `C3` | token input: 7 | 0 |
| `hare` | `HARE` | token input: 7 | 0 |
| `d` | `D` | token input: 7 | 0 |
| `dart` | `DART` | token input: 7 | 0 |
| `elixir` | `ELIXIR` | token input: 7 | 0 |
| `erlang` | `ERLANG` | token input: 7 | 0 |
| `mercury` | `MERCURY` | token input: 7 | 0 |
| `malbolge` | `MALBOLGE` | documented exception: 1 | 3 |
| `fortran` | `FORTRAN`, `FORTRAN95`, `FORTRAN2003`, `FORTRAN2008`, `FORTRAN2018` | token input: 7 | 0 |
| `fsharp` | `FSHARP` | token input: 7 | 0 |
| `gdl` | `GDL` | documented exception: 1 | 1 |
| `gleam` | `GLEAM` | token input: 7 | 0 |
| `sml` | `SML` | token input: 7 | 0 |
| `mlton` | `MLTON` | token input: 7 | 0 |
| `smlnj` | `SMLNJ` | token input: 7 | 0 |
| `go` | `GO` | token input: 7 | 0 |
| `groovy` | `GROOVY` | token input: 7 | 0 |
| `haskell` | `HASKELL` | token input: 7 | 0 |
| `idris2` | `IDRIS2` | line input: 7 | 0 |
| `haxe` | `HAXE` | token input: 7 | 0 |
| `java` | `JAVA11`, `JAVA`, `JAVA8`, `JAVA15`, `JAVA17`, `JAVA21` | token input: 7 | 0 |
| `javascript` | `JAVASCRIPT` | token input: 7 | 0 |
| `coffeescript` | `COFFEESCRIPT` | token input: 7 | 0 |
| `julia` | `JULIA` | token input: 7 | 0 |
| `kotlin` | `KOTLIN` | token input: 7 | 0 |
| `lisp` | `LISP` | token input: 7 | 0 |
| `picolisp` | `PICOLISP` | token input: 7 | 0 |
| `lua` | `LUA`, `LUA54` | token input: 7 | 0 |
| `luajit` | `LUAJIT` | token input: 7 | 0 |
| `nasm` | `NASM` | single-digit input/sum: 6 | 0 |
| `nim` | `NIM` | token input: 7 | 0 |
| `ocaml` | `OCAML` | token input: 7 | 0 |
| `octave` | `OCTAVE` | token input: 7 | 0 |
| `pascal` | `PASCAL` | token input: 7 | 0 |
| `delphi` | `DELPHI` | token input: 7 | 0 |
| `objectpascal` | `OBJECTPASCAL` | token input: 7 | 0 |
| `perl` | `PERL` | token input: 7 | 0 |
| `php` | `PHP`, `PHP7`, `PHP8` | token input: 7 | 0 |
| `prolog` | `PROLOG` | token input: 7 | 0 |
| `gnu-prolog` | `GNU_PROLOG` | token input: 7 | 0 |
| `pypy` | `PYPY3` | token input: 7 | 0 |
| `python` | `PYTHON3` | token input: 7 | 0 |
| `r` | `R` | token input: 7 | 0 |
| `raku` | `RAKU` | token input: 7 | 0 |
| `racket` | `RACKET` | token input: 7 | 0 |
| `ruby` | `RUBY` | token input: 7 | 0 |
| `rust` | `RUST2024`, `RUST`, `RUST2015`, `RUST2018`, `RUST2021` | token input: 7 | 0 |
| `scala` | `SCALA` | token input: 7 | 0 |
| `sqlite` | `SQLITE` | SQL input row: 7 | 0 |
| `sed` | `SED` | token input: 7 | 0 |
| `bc` | `BC` | line input: 7 | 0 |
| `scheme` | `SCHEME` | token input: 7 | 0 |
| `chez-scheme` | `CHEZ_SCHEME` | token input: 7 | 0 |
| `guile` | `GUILE` | token input: 7 | 0 |
| `chicken-scheme` | `CHICKEN_SCHEME` | token input: 7 | 0 |
| `swift` | `SWIFT` | token input: 7 | 0 |
| `typescript` | `TYPESCRIPT` | token input: 7 | 0 |
| `uhmlang` | `UHMLANG` | token input: 7 | 0 |
| `vbnet` | `VBNET`, `VB` | token input: 7 | 0 |
| `vb6` | `VB6` | documented exception: 1 | 2 |
| `freebasic` | `FREEBASIC` | token input: 7 | 0 |
| `classic-basic` | `CLASSIC_BASIC` | token input: 7 | 0 |
| `qbasic` | `QBASIC` | token input: 7 | 0 |
| `vhdl` | `VHDL` | token input: 7 | 0 |
| `verilog` | `VERILOG` | token input: 7 | 0 |
| `systemverilog` | `SYSTEMVERILOG` | token input: 7 | 0 |
| `cuda-ocelot` | `CUDA_OCELOT` | token input: 7 | 0 |
| `carbon` | `CARBON` | token input: 7 | 0 |
| `graphql` | `GRAPHQL` | documented exception: 1 | 2 |
| `smalltalk` | `SMALLTALK`, `GST` | token input: 7 | 0 |
| `golfscript` | `GOLFSCRIPT` | token input: 7 | 2 |
| `mojo` | `MOJO` | token input: 7 | 0 |
| `moonbit` | `MOONBIT` | token input: 7 | 0 |
| `fennel` | `FENNEL` | token input: 7 | 0 |
| `chapel` | `CHAPEL` | token input: 7 | 0 |
| `algol68` | `ALGOL68` | token input: 7 | 0 |
| `koka` | `KOKA` | token input: 7 | 0 |
| `pony` | `PONY` | token input: 7 | 0 |
| `bash` | `SHELL`, `BASH` | token input: 7 | 0 |
| `posix-sh` | `POSIX_SH` | token input: 7 | 0 |
| `zsh` | `ZSH` | token input: 7 | 0 |
| `fish` | `FISH` | token input: 7 | 0 |
| `powershell` | `POWERSHELL` | token input: 7 | 0 |
| `deno` | `DENO`, `TYPESCRIPT_DENO`, `JAVASCRIPT_DENO` | token input: 7 | 0 |
| `bun` | `JAVASCRIPT_BUN`, `TYPESCRIPT_BUN` | token input: 7 | 0 |
| `quickjs` | `JAVASCRIPT_QUICKJS` | token input: 7 | 0 |
| `elm` | `ELM` | token input: 7 | 0 |
| `rescript` | `RESCRIPT` | token input: 7 | 0 |
| `purescript` | `PURESCRIPT` | token input: 7 | 0 |
| `kotlin-jvm` | `KOTLIN_JVM`, `KOTLIN_JVM8`, `KOTLIN_JVM11`, `KOTLIN_JVM17`, `KOTLIN_JVM21`, `KOTLIN_JAVA`, `KOTLIN_JAVA8`, `KOTLIN_JAVA11`, `KOTLIN_JAVA17`, `KOTLIN_JAVA21` | token input: 7 | 0 |
| `duckdb` | `DUCKDB` | SQL input row: 7 | 0 |
| `bqn` | `BQN` | line input: 7 | 0 |
| `apl` | `APL`, `GNU_APL` | documented exception: 1 | 2 |
| `uiua` | `UIUA` | two-digit input: 8 | 0 |
| `janet` | `JANET` | line input: 7 | 0 |
| `forth` | `FORTH` | token input: 7 | 0 |
| `gforth` | `GFORTH` | token input: 7 | 0 |
| `wasm` | `WASM` | single-digit input/sum: 6 | 0 |
| `assemblyscript` | `ASSEMBLYSCRIPT` | token input: 7 | 0 |
| `factor` | `FACTOR` | token input: 7 | 0 |
| `whitespace` | `WHITESPACE` | token input: 7 | 0 |
| `zerolang` | `ZEROLANG` | documented exception: 1 | 1 |
| `zig` | `ZIG` | token input: 7 | 0 |
