# Durante v13 transfer on CPU-pinned GCE judges

## Registered experiment

The frozen v12 pilot covers three exact CPP20 inputs on AMD Milan. The selected clock uses 1139371945900000 / 4752214584908649 ns per instruction and REP byte. The candidate changes the engine to Durante commit `6b4d74f248235f8f616aa5b8e151ca0db5a029de`. The experiment holds the coefficient, solvers, expected outputs, deadlines, and program options fixed. Each arm binds its own worker image, installation, engine and judge clock. [Original selection and qualification](../gce-qualified-judge/refit/README.md)

Two independent, SMT-disabled N2D Milan boots execute target processes on CPU 1. Each receives the same authenticated payload and runs real HTTP workers with read-only runtime mounts. The primary boot runs v12 first and the repeat boot runs v13 first. Every case receives three uninstrumented executions and two executions with each deciding clock on each guest.

- The original panel contains independent fast, wrong, and slow solutions at the contest deadlines, fast solutions at 1 ms, and reference solutions at both frozen boundary limits for each of three inputs.
- The diagnostic panel compares two other input seeds and two implementations per problem at the previously registered low and high limits. All twelve pairs also run at 30 seconds to compare completed counters, stdout and raw CPU overhead.
- The aggregate contains 378 scheduled requests per boot. Two additional executions per arm use a fixed-address freestanding control whose data movement is exactly 4096 MOVS bytes and 4096 STOS bytes. A separate latency-enabled diagnostic records CPU cost and terminal counts on the three fast solvers. Its results do not affect promotion.

### Prospective release criteria

1. On both boots all three arms must produce AC, WA and TLE as registered at all three contest deadlines and TLE at the 1 ms controls. Every observed instrumented timeout in these controls must be an in-flight budget stop.
2. At all original and held-out boundary limits, every pair of v12/v13 judge repetitions on both boots must agree. Record unanimous native errors, false timeouts, false passes and mixed groups separately relative to each engine.
3. Every held-out 30-second execution must terminate with the independently expected stdout and verdict. All completed responses for a given input/program must agree on stdout and both REP byte counters. At most 128 instructions of total spread across completed observations is permitted per input/program. The previous frozen v12 evidence already contained startup variation of up to 42 instructions on identical static C++ binaries. Report exact cross-engine matches and maximum spread separately.
4. Every freestanding control must complete with its fixed output and the exact known REP counts. Instruction count must agree exactly across all eight instrumented invocations. All live and terminal receipts must pass the existing weighted-clock checks and bind the pinned engine, image, installation and runtime options.
5. Require two fresh instance, disk and boot identities, verified CPU placement for every registered judging request, complete evidence digests and deletion of each instance and boot disk. A failed criterion withholds the v13 environment. The v12 release remains usable.

The raw CPU and wall watchdogs remain 120 and 170 seconds. Report median process CPU on completed executions separately from the virtual time used for a verdict. The latency-enabled diagnostic has its own model and timing context. These tests cover cooperative static CPP20 programs with integrity mode off. Cases outside the original three are diagnostics and cannot enter the operator environment.
