# General scripting diagnostic work measurement

This change registers `scripting` as an explicit opt-in durante family. Set
`AONOHAKO_DURANTE_FAMILIES=scripting` and request `work_measurement: true`
for an ordinary single-program run. Registration does not enable measurement by
default or certify every runtime in the family. The common C/C++ integration PR
must be present first; this PR adds family selection, boundary tests and these
qualification requirements.

Shells, Ruby, PHP, Lua/LuaJIT, Perl, Awk, Tcl, sed and bc share this review unit. Shells need complete child-process accounting for pipelines and command substitution; LuaJIT and Ruby may execute generated code. Shell dedicated-image restrictions, startup-file suppression and all existing sandbox restrictions remain unchanged.

The default dispatch profile preserves runtime selection. The
`glibc-2.41-sse2-v1` profile remains restricted to the separately qualified C/C++
experiment. Existing sandbox capabilities are not broadened: support for summed
child accounting does not grant a submission permission to create children.
Incomplete trees, invalid segments and unsupported accounting conditions must
remain explicit measurement failures rather than plausible partial totals.

Measured work is diagnostic software instruction/REP demand, not CPU milliseconds.
All executed runtime and library code contributes; kernel work is outside that
counter. Engine/client memory is included in measured process RSS. Existing
CPU/wall/memory limits still describe the instrumented run and are not evidence
that uninstrumented verdicts are identical. This PR introduces no score-to-time
conversion or trusted enforcement claim.

## Source profiles

`FENNEL`, `SHELL`, `BASH`, `POSIX_SH`, `ZSH`, `FISH`, `AWK`, `TCL`, `RUBY`, `PHP`, `PHP7`, `PHP8`, `LUA`, `LUA54`, `LUAJIT`, `PERL`, `SED`, `BC`.

Accepted unambiguous runtime forms: `awk`, `bash`, `bc`, `fish`, `lua`, `luajit`, `perl`, `php`, `posix-sh`, `ruby`, `sed`, `tcl`, `zsh`.
Canonical source profiles take precedence over runtime aliases. Generic `binary`
remains rejected because it loses source-language provenance.

## Qualification

Selection/configuration tests check representative profiles, other-family
isolation, disabled configuration and rejection of the C/C++-specific dispatch
profile. These are integration-policy tests, not runtime qualification.

Before enabling a runtime operationally, record a complete metered execution of
its actual image-owned command with unchanged output and exit behavior; include
threads/signals, startup and any permitted fork/exec path. Preserve every process
segment and verify that no child work is omitted or double-counted. Repeated
workload measurements, cost, invalid-result rate and limit-boundary behavior must
be reported separately. Representative smoke evidence will be recorded alongside
the PR validation; untested profiles remain unqualified.

## Local compatibility evidence

The frozen core `7039485` with `-vm_size 32M -vmheap_size 32M` completed two local runs of each listed fixture with original output/exit and complete process-tree accounting: `shell-fork`. Exact versions, snapshot hashes, counts, quality flags and retained prototype results are in [scripting-local-smoke.json](scripting-local-smoke.json). These checks do not qualify every source profile or the final deployed image.
The shell fixture exercised a forked background subshell and waited for it; both processes contributed. It does not exercise `vfork` or `posix_spawn`, so this smoke cannot establish their support in the final pinned core.

The fixed compatibility sources, compile/run commands, stdin and expected output
are checked in under [`internal/durante/testdata/scripting`](../../internal/durante/testdata/scripting/manifest.json). They require the indicated local runtimes; missing runtimes
are unavailable coverage, not passing measurements.

These local checks used the external core collector after every process exited.
They do not exercise aonohako sandbox integration; image-specific end-to-end
validation remains a separate requirement.
