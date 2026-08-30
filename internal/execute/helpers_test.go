package execute

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/isolation/cgroup"
	"aonohako/internal/model"
)

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestCgroupLimitBreachSinceIgnoresBaselineHelperEvents(t *testing.T) {
	baseline := cgroup.Stats{
		MemoryEvents: map[string]int64{"max": 2, "oom": 1},
		PidsEvents:   map[string]int64{"max": 3},
	}
	if got := cgroupLimitBreachSince(baseline, baseline, true); got != cgroup.LimitBreachNone {
		t.Fatalf("cgroupLimitBreachSince() = %q, want none", got)
	}

	memoryStats := cgroup.Stats{
		MemoryEvents: map[string]int64{"max": 3, "oom": 1},
		PidsEvents:   map[string]int64{"max": 3},
	}
	if got := cgroupLimitBreachSince(memoryStats, baseline, true); got != cgroup.LimitBreachMemory {
		t.Fatalf("cgroupLimitBreachSince() = %q, want memory", got)
	}

	pidsStats := cgroup.Stats{
		MemoryEvents: map[string]int64{"max": 2, "oom": 1},
		PidsEvents:   map[string]int64{"max": 4},
	}
	if got := cgroupLimitBreachSince(pidsStats, baseline, true); got != cgroup.LimitBreachPids {
		t.Fatalf("cgroupLimitBreachSince() = %q, want pids", got)
	}
}

func TestClassifySandboxSignalRequiresAttributedSIGKILL(t *testing.T) {
	tests := []struct {
		name             string
		status           string
		signal           syscall.Signal
		ctxErr           error
		parentKillReason string
		wantStatus       string
		wantReason       string
		wantSource       string
	}{
		{
			name:       "unattributed sigkill",
			status:     "OK",
			signal:     syscall.SIGKILL,
			wantStatus: model.RunStatusRE,
			wantReason: "process received SIGKILL without a recorded sandbox limit",
			wantSource: "signal_unattributed",
		},
		{
			name:       "confirmed context deadline",
			status:     "OK",
			signal:     syscall.SIGKILL,
			ctxErr:     context.DeadlineExceeded,
			wantStatus: model.RunStatusTLE,
			wantReason: "wall time limit exceeded",
			wantSource: "wall_time",
		},
		{
			name:             "recorded cpu kill",
			status:           "OK",
			signal:           syscall.SIGKILL,
			parentKillReason: "cpu_time",
			wantStatus:       model.RunStatusTLE,
			wantReason:       "cpu time limit exceeded",
			wantSource:       "cpu_time",
		},
		{
			name:       "kernel cpu signal",
			status:     "OK",
			signal:     syscall.SIGXCPU,
			wantStatus: model.RunStatusTLE,
			wantReason: "cpu time limit exceeded",
			wantSource: "cpu_rlimit",
		},
		{
			name:       "ordinary fatal signal",
			status:     "OK",
			signal:     syscall.SIGSEGV,
			wantStatus: model.RunStatusRE,
			wantSource: "signal",
		},
		{
			name:       "prior resource verdict wins",
			status:     model.RunStatusMLE,
			signal:     syscall.SIGKILL,
			wantStatus: model.RunStatusMLE,
			wantReason: "memory limit exceeded",
			wantSource: "memory_rss",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason := ""
			source := ""
			if tc.status == model.RunStatusMLE {
				reason = tc.wantReason
				source = tc.wantSource
			}
			status, reason, source := classifySandboxSignal(tc.status, reason, source, tc.signal, tc.ctxErr, tc.parentKillReason)
			if status != tc.wantStatus || reason != tc.wantReason || source != tc.wantSource {
				t.Fatalf("classifySandboxSignal() = (%q, %q, %q), want (%q, %q, %q)", status, reason, source, tc.wantStatus, tc.wantReason, tc.wantSource)
			}
		})
	}
}

func forceDirectMode(t *testing.T) {
	t.Helper()
	requireSandboxSupport(t)
}

func requireSandboxSupport(t *testing.T) {
	t.Helper()
	if os.Getenv("AONOHAKO_EXECUTION_MODE") == "" {
		t.Setenv("AONOHAKO_EXECUTION_MODE", "local-root")
	}
	if os.Getenv("AONOHAKO_WORK_ROOT") == "" {
		workRoot := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-test-work-%d", time.Now().UnixNano()))
		if err := os.MkdirAll(workRoot, 0o755); err != nil {
			t.Fatalf("mkdir work root: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(workRoot) })
		t.Setenv("AONOHAKO_WORK_ROOT", workRoot)
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "probe.sh",
			DataB64: b64("#!/bin/sh\nexit 0\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status == model.RunStatusAccepted {
		return
	}
	if strings.Contains(resp.Stderr, "sandbox-init:") || strings.Contains(resp.Reason, "sandbox-init:") || strings.Contains(resp.Reason, "sandbox requires root") {
		if os.Getenv("AONOHAKO_ENFORCE_SANDBOX_TESTS") != "" {
			t.Fatalf("sandbox isolation must be available on this runner: %+v", resp)
		}
		t.Skipf("sandbox isolation is unavailable on this runner: %+v", resp)
	}
}

func sandboxAccessibleTempDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("AONOHAKO_WORK_ROOT")
	if root == "" {
		root = os.TempDir()
	}
	dir, err := os.MkdirTemp(root, "aonohako-direct-sandbox-*")
	if err != nil {
		t.Fatalf("mkdir sandbox temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod sandbox temp dir: %v", err)
	}
	return dir
}

func buildCTestBinary(t *testing.T, source string, args ...string) string {
	t.Helper()

	cc, err := exec.LookPath("cc")
	if err != nil {
		cc, err = exec.LookPath("gcc")
	}
	if err != nil {
		t.Skip("C compiler is unavailable on this runner")
	}

	workDir := t.TempDir()
	binPath := filepath.Join(workDir, "runner")
	cmdArgs := []string{"-O2", "-x", "c", "-", "-o", binPath}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command(cc, cmdArgs...)
	cmd.Stdin = strings.NewReader(source)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compile C helper: %v\n%s", err, string(output))
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read compiled helper: %v", err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func TestPonyBinaryAllowsLargeVirtualReservationUnderSmallRSSLimit(t *testing.T) {
	forceDirectMode(t)
	binary := buildCTestBinary(t, `#define _GNU_SOURCE
#include <stdio.h>
#include <sys/mman.h>

int main(void) {
  void *arena = mmap(NULL, 1024ULL * 1024 * 1024, PROT_NONE,
    MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE, -1, 0);
  if (arena == MAP_FAILED) return 2;
  puts("ok");
  return 0;
}
`)
	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "pony-binary",
		Binaries: []model.Binary{{
			Name:    "Main",
			DataB64: binary,
			Mode:    "exec",
		}},
		ExpectedStdout: "ok\n",
		Limits:         model.Limits{TimeMs: 2000, MemoryMB: 64, OutputBytes: 1024},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("Pony virtual reservation failed: %+v", resp)
	}
}

// --------------- #8: buildCommand Java -Xmx dynamic ---------------

func TestBuildCommandJavaXmxDynamic(t *testing.T) {
	tests := []struct {
		name     string
		memoryMB int
		wantXmx  string
	}{
		{"below_32_floors_to_32", 16, "-Xmx32m"},
		{"exactly_32", 32, "-Xmx32m"},
		{"above_32", 128, "-Xmx64m"},
		{"large_memory", 512, "-Xmx256m"},
		{"zero_floors_to_32", 0, "-Xmx32m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &model.RunRequest{Limits: model.Limits{MemoryMB: tc.memoryMB}}
			args := buildCommand("/tmp/Main.jar", "java", req)
			found := false
			for _, a := range args {
				if a == tc.wantXmx {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected %s in args, got %v", tc.wantXmx, args)
			}
			// jar path should be the last arg
			if args[len(args)-1] != "/tmp/Main.jar" {
				t.Errorf("expected jar path as last arg, got %s", args[len(args)-1])
			}
		})
	}
}

func TestBuildCommandJavaAlwaysHasJarFlag(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 128}}
	args := buildCommand("/tmp/Main.jar", "java", req)
	hasJar := false
	for i, a := range args {
		if a == "-jar" && i+1 < len(args) && args[i+1] == "/tmp/Main.jar" {
			hasJar = true
			break
		}
	}
	if !hasJar {
		t.Errorf("expected '-jar /tmp/Main.jar' in args, got %v", args)
	}
}

func TestBuildCommandJavaBoundsOffHeapMemory(t *testing.T) {
	tests := []struct {
		name          string
		memoryMB      int
		wantDirect    string
		wantMetaspace string
	}{
		{"small_memory_floors", 64, "-XX:MaxDirectMemorySize=16m", "-XX:MaxMetaspaceSize=64m"},
		{"medium_memory_scales", 512, "-XX:MaxDirectMemorySize=64m", "-XX:MaxMetaspaceSize=128m"},
		{"large_memory_caps_metaspace", 2048, "-XX:MaxDirectMemorySize=256m", "-XX:MaxMetaspaceSize=192m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &model.RunRequest{Limits: model.Limits{MemoryMB: tc.memoryMB}}
			args := buildCommand("/tmp/Main.jar", "java", req)
			if !containsArg(args, tc.wantDirect) {
				t.Fatalf("java command missing %s: %v", tc.wantDirect, args)
			}
			if !containsArg(args, tc.wantMetaspace) {
				t.Fatalf("java command missing %s: %v", tc.wantMetaspace, args)
			}
		})
	}
}

// --------------- buildCommand all languages ---------------

func TestBuildCommandAllLanguages(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}
	tests := []struct {
		lang      string
		path      string
		wantFirst string
		wantPath  bool
	}{
		{"binary", "/tmp/a.out", "/tmp/a.out", true},
		{"go-binary", "/tmp/Main", "/tmp/Main", true},
		{"mojo-binary", "/tmp/Main", "/tmp/Main", true},
		{"pony-binary", "/tmp/Main", "/tmp/Main", true},
		{"bash", "/tmp/Main.sh", "/bin/bash", true},
		{"posix-sh", "/tmp/Main.sh", "/bin/dash", true},
		{"powershell", "/tmp/Main.ps1", "pwsh", true},
		{"chapel-binary", "/tmp/Main", "env", true},
		{"algol68", "/tmp/Main.a68", "a68g", true},
		{"aheui", "/tmp/sol.aheui", "python3", true},
		{"apecode", "/tmp/Main.ape", "apecc", true},
		{"clojure", "/tmp/sol.clj", "java", true},
		{"coq", "/tmp/Main.v", "true", false},
		{"ocaml", "/tmp/sol", "env", true},
		{"elixir", "/tmp/sol.exs", "env", true},
		{"python", "/tmp/sol.py", "python3", true},
		{"pypy", "/tmp/sol.py", "pypy3", true},
		{"racket", "/tmp/sol.rkt", "racket", true},
		{"scheme", "/tmp/sol.scm", "chibi-scheme", true},
		{"awk", "/tmp/sol.awk", "gawk", true},
		{"tcl", "/tmp/sol.tcl", "tclsh", true},
		{"gdl", "/tmp/sol.pro", "aonohako-gdl-run", true},
		{"octave", "/tmp/sol.m", "octave-cli", true},
		{"vhdl", "/tmp/box", "aonohako-vhdl-run", true},
		{"verilog", "/tmp/Main.vvp", "vvp", true},
		{"erlang", "/tmp/beam", "erl", true},
		{"prolog", "/tmp/sol.pl", "swipl", true},
		{"groovy", "/tmp/classes", "java", false},
		{"lisp", "/tmp/sol.lisp", "sbcl", true},
		{"picolisp", "/tmp/Main.l", "pil", true},
		{"scala", "/tmp/classes", "java", false},
		{"fsharp", "/tmp/App.dll", "dotnet", true},
		{"javascript", "/tmp/sol.js", "node", true},
		{"julia", "/tmp/sol.jl", "julia", true},
		{"raku", "/tmp/sol.raku", "raku", true},
		{"r", "/tmp/sol.R", "/usr/lib/R/bin/exec/R", true},
		{"whitespace", "/tmp/sol.ws", "python3", true},
		{"befunge", "/tmp/sol.bef", "python3", true},
		{"brainfuck", "/tmp/sol.bf", "python3", true},
		{"malbolge", "/tmp/sol.mal", "python3", true},
		{"lolcode", "/tmp/sol.lol", "lci", true},
		{"wasm", "/tmp/sol.wasm", "wasmtime", true},
		{"assemblyscript", "/tmp/sol.wasm", "wasmtime", true},
		{"factor", "/tmp/sol.factor", "/opt/factor/factor", true},
		{"ruby", "/tmp/sol.rb", "ruby", true},
		{"php", "/tmp/sol.php", "php", true},
		{"lua", "/tmp/sol.lua", "lua5.4", true},
		{"perl", "/tmp/sol.pl", "perl", true},
		{"sqlite", "/tmp/sol.sql", "sh", true},
		{"uhmlang", "/tmp/sol.umm", "env", true},
		{"vbnet", "/tmp/App.dll", "dotnet", true},
		{"vb6", "/tmp/Main.bas", "aonohako-vb6-run", true},
		{"gleam", "/tmp/gleam-project", "aonohako-gleam-run", true},
		{"cuda-ocelot", "/tmp/Main", "aonohako-cuda-ocelot-run", true},
		{"graphql", "/tmp/Main.graphql", "aonohako-graphql-run", true},
		{"rocq", "/tmp/Main.v", "true", false},
		{"lean4", "/tmp/Main.lean", "true", false},
		{"agda", "/tmp/Main.agda", "true", false},
		{"dafny", "/tmp/Main.dfy", "true", false},
		{"tla", "/tmp/Main.tla", "aonohako-tla-run", true},
		{"why3", "/tmp/Main.mlw", "true", false},
		{"isabelle", "/tmp/box", "true", false},
		{"fstar", "/tmp/Main.fst", "true", false},
		{"alloy", "/tmp/Main.als", "true", false},
		{"acl2", "/tmp/Main.lisp", "true", false},
		{"kframework", "/tmp/Main.k", "true", false},
		{"smalltalk", "/tmp/Main.st", "gst", true},
		{"golfscript", "/tmp/Main.gs", "ruby", true},
		{"haxe", "/tmp/Main.n", "neko", true},
		{"deno", "/tmp/Main.ts", "deno", true},
		{"kotlin-jvm", "/tmp/Main.jar", "java", true},
		{"duckdb", "/tmp/Main.sql", "aonohako-duckdb-run", true},
		{"bqn", "/tmp/Main.bqn", "bqn", true},
		{"apl", "/tmp/Main.apl", "node", true},
		{"j", "/tmp/Main.ijs", "aonohako-j", true},
		{"uiua", "/tmp/Main.ua", "uiua", true},
		{"janet", "/tmp/Main.janet", "janet", true},
		{"sed", "/tmp/Main.sed", "sed", true},
		{"bc", "/tmp/Main.bc", "bc", true},
		{"forth", "/tmp/Main.fs", "gforth", true},
		{"text", "/tmp/data.txt", "cat", true},
		{"unknown_lang", "/tmp/a.out", "/tmp/a.out", true},
	}
	for _, tc := range tests {
		t.Run(tc.lang, func(t *testing.T) {
			args := buildCommand(tc.path, tc.lang, req)
			if len(args) == 0 {
				t.Fatalf("buildCommand returned empty args for %s", tc.lang)
			}
			if tc.lang == "erlang" {
				if args[0] == "env" {
					found := false
					for _, a := range args {
						if filepath.Base(a) == "erlexec" {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("buildCommand(%s) missing erlexec in %v", tc.lang, args)
					}
				} else if filepath.Base(args[0]) != tc.wantFirst {
					t.Errorf("buildCommand(%s) first arg = %q, want basename %q", tc.lang, args[0], tc.wantFirst)
				}
			} else if args[0] != tc.wantFirst {
				t.Errorf("buildCommand(%s) first arg = %q, want %q", tc.lang, args[0], tc.wantFirst)
			}
			// path should be in args
			if tc.wantPath {
				found := false
				for _, a := range args {
					if a == tc.path {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("buildCommand(%s) missing path %q in %v", tc.lang, tc.path, args)
				}
			}
			if tc.lang == "ocaml" && !containsArg(args, "OCAMLRUNPARAM=s=32k") {
				t.Errorf("buildCommand(%s) missing OCAMLRUNPARAM in %v", tc.lang, args)
			}
			if tc.lang == "clojure" && !containsArg(args, "-XX:CompressedClassSpaceSize=64m") {
				t.Errorf("buildCommand(%s) missing clojure JVM class-space guard in %v", tc.lang, args)
			}
			if tc.lang == "clojure" && !containsArg(args, "-DONLINE_JUDGE=1") {
				t.Errorf("buildCommand(%s) missing ONLINE_JUDGE JVM property in %v", tc.lang, args)
			}
			if tc.lang == "java" && (!containsArg(args, "-XX:MaxDirectMemorySize=16m") || !containsArg(args, "-XX:MaxMetaspaceSize=64m")) {
				t.Errorf("buildCommand(%s) missing Java off-heap guards in %v", tc.lang, args)
			}
			if tc.lang == "java" && !containsArg(args, "-DONLINE_JUDGE=1") {
				t.Errorf("buildCommand(%s) missing ONLINE_JUDGE JVM property in %v", tc.lang, args)
			}
			if tc.lang == "groovy" && !containsArg(args, "-XX:CompressedClassSpaceSize=64m") {
				t.Errorf("buildCommand(%s) missing groovy JVM class-space guard in %v", tc.lang, args)
			}
			if tc.lang == "groovy" && !containsArg(args, "-DONLINE_JUDGE=1") {
				t.Errorf("buildCommand(%s) missing ONLINE_JUDGE JVM property in %v", tc.lang, args)
			}
			if tc.lang == "scala" && !containsArg(args, "-DONLINE_JUDGE=1") {
				t.Errorf("buildCommand(%s) missing ONLINE_JUDGE JVM property in %v", tc.lang, args)
			}
			if tc.lang == "kotlin-jvm" && (!containsArg(args, "-XX:MaxDirectMemorySize=16m") || !containsArg(args, "-XX:MaxMetaspaceSize=64m")) {
				t.Errorf("buildCommand(%s) missing Kotlin/JVM off-heap guards in %v", tc.lang, args)
			}
			if tc.lang == "kotlin-jvm" && !containsArg(args, "-DONLINE_JUDGE=1") {
				t.Errorf("buildCommand(%s) missing ONLINE_JUDGE JVM property in %v", tc.lang, args)
			}
			if tc.lang == "elixir" && !containsArg(args, "ERL_AFLAGS=+MIscs 128 +S 1:1 +A 1 +MMscs 0") {
				t.Errorf("buildCommand(%s) missing ERL_AFLAGS in %v", tc.lang, args)
			}
			if tc.lang == "r" && !containsArg(args, "--slave") {
				t.Errorf("buildCommand(%s) should bypass the forking Rscript wrapper in %v", tc.lang, args)
			}
			if tc.lang == "julia" && !containsArg(args, "--compiled-modules=no") {
				t.Errorf("buildCommand(%s) should disable runtime precompile spawning in %v", tc.lang, args)
			}
			if tc.lang == "lisp" && !containsArg(args, "--dynamic-space-size") {
				t.Errorf("buildCommand(%s) should bound SBCL dynamic space in %v", tc.lang, args)
			}
			if tc.lang == "uhmlang" {
				if !containsArg(args, "GOMEMLIMIT=32MiB") || !containsArg(args, "GOGC=50") || !containsArg(args, "/usr/bin/umjunsik-lang-go") {
					t.Errorf("buildCommand(%s) missing Go runtime memory env in %v", tc.lang, args)
				}
			}
			if tc.lang == "aheui" && (len(args) < 3 || !strings.Contains(args[2], "from aheui.aheui import entry_point")) {
				t.Errorf("buildCommand(%s) missing aheui python wrapper body in %v", tc.lang, args)
			}
			if tc.lang == "malbolge" && !containsArg(args, "/usr/local/lib/aonohako/malbolge.py") {
				t.Errorf("buildCommand(%s) missing bundled interpreter path in %v", tc.lang, args)
			}
		})
	}
}

func TestBuildCommandBoundsChapelLocalesAndThreads(t *testing.T) {
	got := buildCommand("/tmp/Main", "chapel-binary", &model.RunRequest{})
	want := []string{"env", "CHPL_RT_NUM_THREADS_PER_LOCALE=1", "/tmp/Main", "-nl", "1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chapel command = %v, want %v", got, want)
	}
}

func TestBuildCommandBoundsPonySchedulers(t *testing.T) {
	got := buildCommand("/tmp/Main", "pony-binary", &model.RunRequest{})
	want := []string{"/tmp/Main", "--ponymaxthreads=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pony command = %v, want %v", got, want)
	}
}

func TestBuildCommandDisablesShellStartupFiles(t *testing.T) {
	tests := map[string][]string{
		"bash":     {"/bin/bash", "--noprofile", "--norc", "/tmp/Main.sh"},
		"posix-sh": {"/bin/dash", "/tmp/Main.sh"},
	}
	for runLang, want := range tests {
		if got := buildCommand("/tmp/Main.sh", runLang, &model.RunRequest{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s command = %v, want %v", runLang, got, want)
		}
	}
}

func TestBuildCommandUsesNonInteractivePowerShell(t *testing.T) {
	got := buildCommand("/tmp/Main.ps1", "powershell", &model.RunRequest{})
	want := []string{"pwsh", "-NoLogo", "-NoProfile", "-NonInteractive", "-File", "/tmp/Main.ps1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PowerShell command = %v, want %v", got, want)
	}
}

func TestBuildCommandHardensAlgol68Options(t *testing.T) {
	got := buildCommand("/tmp/Main.a68", "algol68", &model.RunRequest{})
	want := []string{"a68g", "--quiet", "--no-compile", "-O0", "--run", "--file", "/tmp/Main.a68", "--no-pragmats"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("algol68 command = %v, want %v", got, want)
	}
}

func TestBuildCommandErlangEntryPointVariants(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}

	defaultArgs := buildCommand("/tmp/beam", "erlang", req)
	expectedTail := []string{"+S", "1:1", "+A", "1", "-noshell", "-pa", "/tmp/beam", "-s", "main", "main", "-s", "init", "stop"}
	if defaultArgs[0] == "env" {
		if !containsArg(defaultArgs, "EMU=beam") || !containsArg(defaultArgs, "PROGNAME=erl") || !containsArg(defaultArgs, "ERL_AFLAGS=+MIscs 128 +S 1:1 +A 1 +MMscs 0") {
			t.Fatalf("default erlang env wrapper missing beam vars: %v", defaultArgs)
		}
		if !reflect.DeepEqual(defaultArgs[len(defaultArgs)-len(expectedTail):], expectedTail) {
			t.Fatalf("default erlang command tail = %v", defaultArgs)
		}
	} else if filepath.Base(defaultArgs[0]) != "erl" || !reflect.DeepEqual(defaultArgs[1:], expectedTail) {
		t.Fatalf("default erlang command = %v", defaultArgs)
	}

	req.EntryPoint = "judge/main:solve"
	entryArgs := buildCommand("/tmp/beam", "erlang", req)
	expectedEntryTail := []string{"+S", "1:1", "+A", "1", "-noshell", "-pa", "/tmp/beam", "-s", "main", "solve", "-s", "init", "stop"}
	if entryArgs[0] == "env" {
		if !containsArg(entryArgs, "EMU=beam") || !containsArg(entryArgs, "PROGNAME=erl") || !containsArg(entryArgs, "ERL_AFLAGS=+MIscs 128 +S 1:1 +A 1 +MMscs 0") {
			t.Fatalf("entrypoint erlang env wrapper missing beam vars: %v", entryArgs)
		}
		if !reflect.DeepEqual(entryArgs[len(entryArgs)-len(expectedEntryTail):], expectedEntryTail) {
			t.Fatalf("entrypoint erlang command tail = %v", entryArgs)
		}
	} else if filepath.Base(entryArgs[0]) != "erl" || !reflect.DeepEqual(entryArgs[1:], expectedEntryTail) {
		t.Fatalf("entrypoint erlang command = %v", entryArgs)
	}
}

func TestBuildCommandGDLProcedureEntryPoint(t *testing.T) {
	req := &model.RunRequest{EntryPoint: "solve_2$"}
	args := buildCommand("/tmp/Main.pro", "gdl", req)
	want := []string{"aonohako-gdl-run", "/tmp/Main.pro", "solve_2$"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("GDL command = %v, want %v", args, want)
	}
}

func TestBuildCommandNormalizesManagedRuntimeEntryPoints(t *testing.T) {
	req := &model.RunRequest{EntryPoint: "pkg/Main", Limits: model.Limits{MemoryMB: 64}}

	groovyArgs := buildCommand("/tmp/classes", "groovy", req)
	if groovyArgs[0] != "java" {
		t.Fatalf("groovy command should bypass the forking groovy shell launcher, got %v", groovyArgs)
	}
	if !containsArg(groovyArgs, "-cp") || !containsArg(groovyArgs, "pkg.Main") {
		t.Fatalf("groovy command = %v", groovyArgs)
	}
	classpath := ""
	for i, arg := range groovyArgs {
		if arg == "-cp" && i+1 < len(groovyArgs) {
			classpath = groovyArgs[i+1]
			break
		}
	}
	if !strings.Contains(classpath, "/tmp/classes") || !strings.Contains(classpath, "groovy") {
		t.Fatalf("groovy classpath should include compiled classes and groovy runtime jars, got %q", classpath)
	}

	scalaArgs := buildCommand("/tmp/classes", "scala", req)
	if scalaArgs[0] != "java" {
		t.Fatalf("scala command should bypass the forking scala shell launcher, got %v", scalaArgs)
	}
	if !containsArg(scalaArgs, "-cp") || !containsArg(scalaArgs, "pkg.Main") {
		t.Fatalf("scala command = %v", scalaArgs)
	}
	scalaClasspath := ""
	for i, arg := range scalaArgs {
		if arg == "-cp" && i+1 < len(scalaArgs) {
			scalaClasspath = scalaArgs[i+1]
			break
		}
	}
	if !strings.Contains(scalaClasspath, "/tmp/classes") || !strings.Contains(scalaClasspath, "scala") {
		t.Fatalf("scala classpath should include compiled classes and scala runtime jars, got %q", scalaClasspath)
	}
}

func TestBuildCommandPinsLanguageSpecificFlags(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 96}}

	coqArgs := buildCommand("/tmp/Main.v", "rocq", req)
	if !reflect.DeepEqual(coqArgs, []string{"true"}) {
		t.Fatalf("coq command = %v", coqArgs)
	}

	prologArgs := buildCommand("/tmp/Main.pl", "prolog", req)
	if !reflect.DeepEqual(prologArgs, []string{"swipl", "-q", "-f", "/tmp/Main.pl", "-g", "main", "-t", "halt"}) {
		t.Fatalf("prolog command = %v", prologArgs)
	}

	lispArgs := buildCommand("/tmp/Main.lisp", "lisp", req)
	if !reflect.DeepEqual(lispArgs, []string{"sbcl", "--noinform", "--dynamic-space-size", "64", "--script", "/tmp/Main.lisp"}) {
		t.Fatalf("lisp command = %v", lispArgs)
	}

	picoLispArgs := buildCommand("/tmp/Main.l", "picolisp", req)
	if !reflect.DeepEqual(picoLispArgs, []string{"pil", "/tmp/Main.l", "-bye"}) {
		t.Fatalf("picolisp command = %v", picoLispArgs)
	}

	jsArgs := buildCommand("/tmp/Main.js", "javascript", req)
	if !reflect.DeepEqual(jsArgs, []string{
		"node",
		"--disable-wasm-trap-handler",
		"--max-old-space-size=57",
		"--max-semi-space-size=1",
		"--stack-size=2048",
		"/tmp/Main.js",
	}) {
		t.Fatalf("javascript command = %v", jsArgs)
	}

	aplArgs := buildCommand("/tmp/Main.apl", "apl", req)
	if !reflect.DeepEqual(aplArgs, []string{
		"node",
		"--disable-wasm-trap-handler",
		"--max-old-space-size=57",
		"--max-semi-space-size=1",
		"--stack-size=2048",
		"/usr/local/bin/apl",
		"--script",
		"-f",
		"/tmp/Main.apl",
	}) {
		t.Fatalf("apl command = %v", aplArgs)
	}

	wasmArgs := buildCommand("/tmp/Main.wasm", "wasm", req)
	if !reflect.DeepEqual(wasmArgs, []string{
		"wasmtime",
		"run",
		"--dir=.",
		"-O", "memory-reservation=50331648",
		"-O", "memory-reservation-for-growth=0",
		"-O", "memory-guard-size=65536",
		"-W", "max-memory-size=50331648",
		"-W", "max-memories=1",
		"-W", "max-instances=1",
		"-W", "max-tables=1",
		"-W", "max-table-elements=65536",
		"-W", "max-wasm-stack=1048576",
		"-W", "trap-on-grow-failure=y",
		"/tmp/Main.wasm",
	}) {
		t.Fatalf("wasm command = %v", wasmArgs)
	}
}

func TestBuildCommandRunsAssemblyScriptWithoutFilesystemPreopens(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 128}}
	args := buildCommand("/tmp/Main.wasm", "assemblyscript", req)
	want := []string{
		"wasmtime",
		"run",
		"-O", "memory-reservation=67108864",
		"-O", "memory-reservation-for-growth=0",
		"-O", "memory-guard-size=65536",
		"-W", "max-memory-size=67108864",
		"-W", "max-memories=1",
		"-W", "max-instances=1",
		"-W", "max-tables=1",
		"-W", "max-table-elements=65536",
		"-W", "max-wasm-stack=1048576",
		"-W", "trap-on-grow-failure=y",
		"/tmp/Main.wasm",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("assemblyscript command = %v, want %v", args, want)
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--dir=") {
			t.Fatalf("assemblyscript command unexpectedly preopens a directory: %v", args)
		}
	}
}

func TestBuildCommandRunsFactorWithBoundedStacksAndNoUserInit(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 512}}
	args := buildCommand("/tmp/Main.factor", "factor", req)
	want := []string{
		"/opt/factor/factor",
		"-no-user-init",
		"-no-signals",
		"-q",
		"-datastack=256",
		"-retainstack=256",
		"-callstack=1024",
		"-callbacks=256",
		"-young=2",
		"-aging=4",
		"-tenured=192",
		"-codeheap=96",
		"/tmp/Main.factor",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("factor command = %v, want %v", args, want)
	}
}

func TestBuildCommandUsesRuntimeTuningConfig(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 256}}
	tuning := config.RuntimeTuningConfig{
		JVMHeapPercent:            40,
		GoMemoryReserveMB:         64,
		GoGOGC:                    80,
		ErlangSchedulers:          2,
		ErlangAsyncThreads:        3,
		DotnetGCHeapPercent:       50,
		DenoOldSpacePercent:       50,
		NodeOldSpacePercent:       50,
		NodeMaxSemiSpaceMB:        2,
		NodeStackSizeKB:           1024,
		WasmtimeMemoryGuardBytes:  128 << 10,
		WasmtimeMaxWasmStackBytes: 512 << 10,
	}

	jsArgs := buildCommandWithRuntimeTuning("/tmp/Main.js", "javascript", req, tuning)
	if !reflect.DeepEqual(jsArgs, []string{
		"node",
		"--disable-wasm-trap-handler",
		"--max-old-space-size=128",
		"--max-semi-space-size=2",
		"--stack-size=1024",
		"/tmp/Main.js",
	}) {
		t.Fatalf("javascript command with tuning = %v", jsArgs)
	}

	javaArgs := buildCommandWithRuntimeTuning("/tmp/Main.jar", "java", req, tuning)
	if !containsArg(javaArgs, "-Xmx102m") {
		t.Fatalf("java command with tuning = %v", javaArgs)
	}
	if !containsArg(javaArgs, "-XX:MaxDirectMemorySize=32m") || !containsArg(javaArgs, "-XX:MaxMetaspaceSize=64m") {
		t.Fatalf("java command with tuning missing off-heap guards = %v", javaArgs)
	}

	erlangArgs := buildCommandWithRuntimeTuning("/tmp/beam", "erlang", req, tuning)
	if !containsArg(erlangArgs, "2:2") || !containsArg(erlangArgs, "3") {
		t.Fatalf("erlang command with tuning = %v", erlangArgs)
	}

	if got := dotnetGCHeapHardLimitHex(256, tuning); got != "8000000" {
		t.Fatalf("dotnet GC heap hard limit = %s, want 8000000", got)
	}

	denoArgs := buildCommandWithRuntimeTuning("/tmp/Main.ts", "deno", req, tuning)
	if !reflect.DeepEqual(denoArgs, []string{
		"deno",
		"run",
		"--no-prompt",
		"--v8-flags=--max-old-space-size=128",
		"/tmp/Main.ts",
	}) {
		t.Fatalf("deno command with tuning = %v", denoArgs)
	}

	uhmArgs := buildCommandWithRuntimeTuning("/tmp/Main.umm", "uhmlang", req, tuning)
	if !reflect.DeepEqual(uhmArgs, []string{
		"env",
		"GOMEMLIMIT=192MiB",
		"GOGC=80",
		"/usr/bin/umjunsik-lang-go",
		"/tmp/Main.umm",
	}) {
		t.Fatalf("uhmlang command with tuning = %v", uhmArgs)
	}

	wasmArgs := buildCommandWithRuntimeTuning("/tmp/Main.wasm", "wasm", req, tuning)
	if !reflect.DeepEqual(wasmArgs, []string{
		"wasmtime",
		"run",
		"--dir=.",
		"-O", "memory-reservation=201326592",
		"-O", "memory-reservation-for-growth=0",
		"-O", "memory-guard-size=131072",
		"-W", "max-memory-size=201326592",
		"-W", "max-memories=1",
		"-W", "max-instances=1",
		"-W", "max-tables=1",
		"-W", "max-table-elements=65536",
		"-W", "max-wasm-stack=524288",
		"-W", "trap-on-grow-failure=y",
		"/tmp/Main.wasm",
	}) {
		t.Fatalf("wasm command with tuning = %v", wasmArgs)
	}
}

func TestBuildCommandTreatsAheuiWarningExitAsSuccess(t *testing.T) {
	workDir := t.TempDir()
	pkgDir := filepath.Join(workDir, "pkg", "aheui")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", pkgDir, err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "__init__.py"), []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile(__init__.py): %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "aheui.py"), []byte("import sys\n\ndef entry_point(argv):\n    sys.stdout.write('Hello, World!\\n')\n    sys.stderr.write('[Warning:VirtualMachine] Running without rlib/jit.\\n\\n')\n    return 9\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(aheui.py): %v", err)
	}
	program := filepath.Join(workDir, "Main.aheui")
	if err := os.WriteFile(program, []byte("fake"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", program, err)
	}
	args := buildCommand(program, "aheui", &model.RunRequest{Limits: model.Limits{MemoryMB: 64}})
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(workDir, "pkg"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("aheui wrapper should accept warning-only exit, err=%v output=%q", err, string(output))
	}
	if !strings.Contains(string(output), "Hello, World!\n") {
		t.Fatalf("aheui wrapper output = %q, want Hello, World! line", string(output))
	}
}

func TestBuildCommandUsesRuntimeScopedSQLiteDBAndJavaHeap(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 96}}

	sqliteArgs := buildCommand("/tmp/work/Main.sql", "sqlite", req)
	if !reflect.DeepEqual(sqliteArgs, []string{"sh", "-c", "exec sqlite3 \"$0\" \".read /dev/stdin\" \".read $1\"", "/tmp/work/.aonohako.sqlite3", "/tmp/work/Main.sql"}) {
		t.Fatalf("sqlite command = %v", sqliteArgs)
	}

	javaArgs := buildCommand("/tmp/submission.jar", "java", req)
	if !containsArg(javaArgs, "-Xmx48m") {
		t.Fatalf("java command missing -Xmx48m: %v", javaArgs)
	}
	if !containsArg(javaArgs, "-jar") || javaArgs[len(javaArgs)-1] != "/tmp/submission.jar" {
		t.Fatalf("java command should end with jar path: %v", javaArgs)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestBuildCommandCSharpDLL(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}
	args := buildCommand("/tmp/Program.dll", "csharp", req)
	if args[0] != "dotnet" {
		t.Errorf("csharp .dll should use dotnet, got %v", args)
	}
}

func TestBuildCommandCSharpExe(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}
	args := buildCommand("/tmp/Program", "csharp", req)
	if args[0] != "/tmp/Program" {
		t.Errorf("csharp non-.dll should run directly, got %v", args)
	}
}

func TestBuildCommandEmpty(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}
	// default case
	args := buildCommand("/tmp/a.out", "somethingelse", req)
	if len(args) != 1 || args[0] != "/tmp/a.out" {
		t.Errorf("unknown lang should return [path], got %v", args)
	}
}

func TestIOCopyPropagatesUnexpectedErrors(t *testing.T) {
	readErr := errors.New("read failed")
	if _, err := ioCopy(&cappedBuffer{limit: 16}, failingReader{err: readErr}); !errors.Is(err, readErr) {
		t.Fatalf("ioCopy read error = %v, want %v", err, readErr)
	}

	writeErr := errors.New("write failed")
	if _, err := ioCopy(failingWriter{err: writeErr}, strings.NewReader("payload")); !errors.Is(err, writeErr) {
		t.Fatalf("ioCopy write error = %v, want %v", err, writeErr)
	}
}

func TestFirstImagePathRequiresImageSidecarPath(t *testing.T) {
	paths := []model.OutputFile{
		{Path: "my_images_data.csv"},
		{Path: "__img__/metrics.jsonl"},
		{Path: "__img__/images.jsonl"},
	}
	if got := firstImagePath(paths); got != "__img__/images.jsonl" {
		t.Fatalf("firstImagePath = %q, want __img__/images.jsonl", got)
	}
}

// --------------- clipUTF8 ---------------

func TestClipUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		n    int
		want string
	}{
		{"short_ascii", []byte("hello"), 10, "hello"},
		{"exact_ascii", []byte("hello"), 5, "hello"},
		{"clip_ascii", []byte("hello world"), 5, "hello"},
		{"empty", []byte{}, 10, ""},
		{"clip_at_zero", []byte("hello"), 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clipUTF8(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("clipUTF8(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestClipUTF8MultibyteNoCut(t *testing.T) {
	// 한글 3 bytes per char: "가나" = 6 bytes
	input := []byte("가나")
	got := clipUTF8(input, 6)
	if got != "가나" {
		t.Errorf("clipUTF8(가나, 6) = %q, want 가나", got)
	}
}

func TestClipUTF8MultibyteCutsCleanly(t *testing.T) {
	// "가나" = 6 bytes; clipping at 4 should give "가" (3 bytes)
	input := []byte("가나")
	got := clipUTF8(input, 4)
	if got != "가" {
		t.Errorf("clipUTF8(가나, 4) = %q, want 가", got)
	}
}

func TestClipUTF8InvalidBytes(t *testing.T) {
	// Invalid UTF-8 that's shorter than n: should trim invalid tail
	input := []byte{0xff, 0xfe}
	got := clipUTF8(input, 10)
	if got != "" {
		t.Errorf("clipUTF8(invalid, 10) = %q, want empty", got)
	}
}

func TestClipUTF8MixedValid(t *testing.T) {
	// "a가" = 1 + 3 = 4 bytes; clipping at 3 should give "a"
	input := []byte("a가")
	got := clipUTF8(input, 3)
	if got != "a" {
		t.Errorf("clipUTF8(a가, 3) = %q, want a", got)
	}
}

// --------------- addressSpaceLimitBytes ---------------

func TestAddressSpaceLimitBytes(t *testing.T) {
	tests := []struct {
		name        string
		commandBase string
		memMB       int
		want        uint64
	}{
		{"native_small", "runner", 16, 512 * 1024 * 1024},
		{"native_floor", "runner", 256, 512 * 1024 * 1024},
		{"native_large", "runner", 512, 576 * 1024 * 1024},
		{"native_zero", "runner", 0, 512 * 1024 * 1024},
		{"python_interpreter_virtual_cap", "python3", 256, 1280 * 1024 * 1024},
		{"pypy_interpreter_virtual_cap", "pypy3", 128, 1024 * 1024 * 1024},
		{"sbcl_interpreter_virtual_cap", "sbcl", 256, 8192 * 1024 * 1024},
		{"node_high_virtual_cap", "node", 128, 1024 * 1024 * 1024},
		{"node_scaled_virtual_cap", "node", 512, 2560 * 1024 * 1024},
		{"deno_virtual_cap", "deno", 256, 65536 * 1024 * 1024},
		{"wasmtime_virtual_cap", "wasmtime", 256, 2048 * 1024 * 1024},
		{"go_interpreter_virtual_cap", "umjunsik-lang-go", 128, 1024 * 1024 * 1024},
		{"elixir_virtual_cap", "elixir", 256, 8192 * 1024 * 1024},
		{"dotnet_virtual_cap", "dotnet", 256, 3584 * 1024 * 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := addressSpaceLimitBytes(tc.commandBase, tc.memMB)
			if got != tc.want {
				t.Errorf("addressSpaceLimitBytes(%q, %d) = %d, want %d", tc.commandBase, tc.memMB, got, tc.want)
			}
		})
	}
}

func TestAddressSpaceLimitBytesAlwaysAtLeast512MB(t *testing.T) {
	minBytes := uint64(512 * 1024 * 1024)
	for memMB := 0; memMB <= 200; memMB++ {
		got := addressSpaceLimitBytes("runner", memMB)
		if got < minBytes {
			t.Errorf("addressSpaceLimitBytes(%d) = %d, below minimum %d", memMB, got, minBytes)
		}
	}
}

func TestAddressSpaceProximityClassificationOnlyForNativeCommands(t *testing.T) {
	for _, commandBase := range []string{"deno", "dotnet", "elixir", "java", "node", "pypy3", "python3", "sbcl", "umjunsik-lang-go", "wasmtime"} {
		if addressSpaceProximityCanClassifyMLE(commandBase, "") {
			t.Fatalf("%s should not use address-space proximity for MLE classification", commandBase)
		}
	}
	if !addressSpaceProximityCanClassifyMLE("runner", "binary") {
		t.Fatalf("native runner should use address-space proximity for MLE classification")
	}
}

func TestEvaluateRunStatusClassifiesFinalCPUOverrun(t *testing.T) {
	req := &model.RunRequest{ExpectedStdout: "ok\n", Limits: model.Limits{TimeMs: 100, MemoryMB: 64}}
	res := execResult{Status: "OK", CPUTimeMs: 101}
	status, _, reason, source := evaluateRunStatus(context.Background(), Workspace{}, req, res, []byte("ok\n"), "stdout", "", nil, config.DefaultRuntimeTuningConfig(), "")
	if status != model.RunStatusTLE {
		t.Fatalf("status = %q, want TLE", status)
	}
	if reason != "cpu time limit exceeded" {
		t.Fatalf("reason = %q, want cpu limit reason", reason)
	}
	if source != "cpu_time_final" {
		t.Fatalf("source = %q, want cpu_time_final", source)
	}
}

func TestSandboxCommandBaseSkipsEnvAssignments(t *testing.T) {
	trustedRoot := filepath.Join(t.TempDir(), "usr", "local", "bin")
	runtimePath := filepath.Join(trustedRoot, "umjunsik-lang-go")
	if err := os.MkdirAll(trustedRoot, 0o755); err != nil {
		t.Fatalf("create trusted runtime directory: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("create trusted runtime: %v", err)
	}
	got := sandboxCommandBaseWithTrustedRoots([]string{"/usr/bin/env", "GOMEMLIMIT=64MiB", runtimePath, "/tmp/Main.umm"}, []string{trustedRoot})
	if got != "umjunsik-lang-go" {
		t.Fatalf("sandboxCommandBase returned %q", got)
	}
}

// --------------- Run edge cases ---------------

func TestRunNilRequest(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), nil, Hooks{})
	if resp.Status != model.RunStatusInitFail {
		t.Errorf("expected InitFail for nil request, got %s", resp.Status)
	}
}

func TestRunNoBinaries(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{}, Hooks{})
	if resp.Status != model.RunStatusInitFail {
		t.Errorf("expected InitFail for empty binaries, got %s", resp.Status)
	}
}

func TestRunRejectsUnknownRuntimeProfile(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{RuntimeProfile: "missing"}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "unknown runtime_profile") {
		t.Fatalf("expected unknown runtime profile init fail, got %+v", resp)
	}
}

func TestRunEmptyCommand(t *testing.T) {
	svc := New()
	req := &model.RunRequest{
		Lang: "",
		Binaries: []model.Binary{{
			Name:    "empty",
			DataB64: b64(""),
			Mode:    "exec",
		}},
		Limits: model.Limits{TimeMs: 100, MemoryMB: 16},
	}
	// buildCommand with empty file just returns the path;
	// the command may fail but shouldn't panic
	resp := svc.Run(context.Background(), req, Hooks{})
	_ = resp // just ensure no panic
}

// --------------- prepareWorkspaceDirs ---------------

func TestPrepareWorkspaceDirs(t *testing.T) {
	dir := t.TempDir()
	if _, err := prepareWorkspaceDirs(dir); err != nil {
		t.Fatalf("prepareWorkspaceDirs failed: %v", err)
	}
	expected := []string{".home", ".tmp", ".cache", ".mpl", ".pip-cache", "__img__", "box"}
	for _, name := range expected {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected dir %s to exist: %v", name, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", name)
		}
	}
}

func TestPrepareWorkspaceDirsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := prepareWorkspaceDirs(dir); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := prepareWorkspaceDirs(dir); err != nil {
		t.Fatalf("second (idempotent) call failed: %v", err)
	}
}
