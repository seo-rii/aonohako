package compile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/util"
)

type recordedCommand struct {
	workDir string
	bin     string
	args    []string
	env     []string
}

type recordingCommandRunner struct {
	commands []recordedCommand
	result   CommandResult
	hook     func(workDir, bin string, args, env []string)
}

func (r *recordingCommandRunner) Run(_ context.Context, workDir, bin string, args, env []string) CommandResult {
	r.commands = append(r.commands, recordedCommand{
		workDir: workDir,
		bin:     bin,
		args:    append([]string{}, args...),
		env:     append([]string{}, env...),
	})
	if r.hook != nil {
		r.hook(workDir, bin, args, env)
	}
	return r.result
}

func TestCompileRegistryIncludesSimpleCompilers(t *testing.T) {
	for _, kind := range []string{
		"c", "cpp", "asm", "fortran", "objective-c", "objective-cpp",
		"pascal", "delphi", "objectpascal", "nim", "zig", "sml", "idris2", "ada", "d",
		"rust", "go", "java", "groovy", "clojure",
		"scheme", "awk", "tcl", "gdl", "octave", "carbon", "graphql", "lean4", "agda", "dafny", "tla", "why3", "fstar", "alloy", "acl2", "kframework",
		"vhdl", "verilog", "crystal", "vala", "vlang", "odin", "c3", "hare", "vbnet", "gleam", "cuda-ocelot", "rocq", "isabelle",
		"python", "pypy",
		"racket", "javascript", "ruby", "php", "lua", "perl",
		"raku", "r", "mercury", "prolog", "lisp", "picolisp", "nasm", "erlang", "vb6", "smalltalk", "golfscript", "duckdb", "bqn", "apl", "j", "uiua", "janet", "sed", "bc", "forth",
		"typescript", "kotlin", "cobol", "cython", "haskell", "elm", "haxe", "swift", "sqlite", "julia", "scala", "fsharp",
		"freebasic", "classic-basic", "mojo", "zerolang", "deno", "kotlin-jvm", "coffeescript", "rescript", "purescript", "whitespace", "befunge", "brainfuck", "malbolge", "lolcode", "apecode", "wasm",
		"ocaml", "elixir", "csharp", "dart", "none",
	} {
		if _, ok := lookupCompiler(kind); !ok {
			t.Fatalf("missing compiler registry entry for %s", kind)
		}
	}
}

func TestZigCompilerTargetsPortableBaselineCPU(t *testing.T) {
	compiler, ok := compileRegistry["zig"].(singleSourceExecutableCompiler)
	if !ok {
		t.Fatalf("zig compiler = %T, want singleSourceExecutableCompiler", compileRegistry["zig"])
	}
	args := compiler.args(
		CompileJob{WorkDir: "/work", Target: "Main"},
		"/work/Main.zig",
	)
	if !slices.Contains(args, "-mcpu=baseline") {
		t.Fatalf("zig compiler args = %v, want portable CPU baseline", args)
	}
}

func TestAPECodeCompilerBuildsExecutable(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.ape"), []byte("state main { return true; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, bin string, args, env []string) {
			if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("native"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := apeCodeCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.ape"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	wantArgs := []string{"-o", filepath.Join(workDir, "Main"), filepath.Join(workDir, "Main.ape")}
	if !reflect.DeepEqual(runner.commands[0].args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", runner.commands[0].args, wantArgs)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
}

func TestCarbonCompilerCompilesAndLinksExecutableWithPrebuiltCoreObjects(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"Helper.carbon", "Main.carbon"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("fn Run() -> i32 { return 0; }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	coreDir := filepath.Join(t.TempDir(), "core")
	if err := os.MkdirAll(filepath.Join(coreDir, "prelude"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(coreDir, "io.o"), filepath.Join(coreDir, "prelude", "string.o")} {
		if err := os.WriteFile(path, []byte("core"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, args, _ []string) {
			switch args[0] {
			case "compile":
				if err := os.WriteFile(filepath.Join(workDir, "Main.o"), []byte("object"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "link":
				if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("executable"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		},
	}

	resp := carbonCompiler{coreObjectDir: coreDir}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Helper.carbon"}, {Name: "Main.carbon"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	wantCompile := []string{
		"compile",
		"--optimize=speed",
		"--no-debug-info",
		"--output-last-input-only",
		"--output=" + filepath.Join(workDir, "Main.o"),
		filepath.Join(workDir, "Main.carbon"),
	}
	if got := runner.commands[0]; got.bin != "carbon" || !reflect.DeepEqual(got.args, wantCompile) {
		t.Fatalf("compile command = %+v, want carbon %#v", got, wantCompile)
	}
	wantLink := []string{
		"link",
		"--output=" + filepath.Join(workDir, "Main"),
		filepath.Join(workDir, "Main.o"),
		filepath.Join(coreDir, "io.o"),
		filepath.Join(coreDir, "prelude", "string.o"),
	}
	if got := runner.commands[1]; got.bin != "carbon" || !reflect.DeepEqual(got.args, wantLink) {
		t.Fatalf("link command = %+v, want carbon %#v", got, wantLink)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
}

func TestCarbonCompilerRequiresPrebuiltCoreObjects(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.carbon"), []byte("fn Run() -> i32 { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}

	resp := carbonCompiler{coreObjectDir: t.TempDir()}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.carbon"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "Core objects") {
		t.Fatalf("response = %+v", resp)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("runner commands = %+v, want none", runner.commands)
	}
}

func TestZerolangCompilerImportsGraphThenBuildsExecutable(t *testing.T) {
	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, "Main.0")
	if err := os.WriteFile(sourcePath, []byte(`pub fn main(world: World) -> Void raises { check world.out.write("ok\n") }`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, args, _ []string) {
			if len(args) > 0 && args[0] == "build" {
				if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("native"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		},
	}

	resp := zerolangCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.0"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	graphPath := filepath.Join(workDir, ".cache", "zerolang-Main.graph")
	cacheEnv := []string{"ZERO_CACHE_DIR=" + filepath.Join(workDir, ".cache", "zerolang-native")}
	wantCommands := []recordedCommand{
		{workDir: workDir, bin: "zero", args: []string{"import", "--out", graphPath, sourcePath}, env: cacheEnv},
		{workDir: workDir, bin: "zero", args: []string{"build", "--release", "release-fast", "--out", filepath.Join(workDir, "Main"), graphPath}, env: cacheEnv},
	}
	if !reflect.DeepEqual(runner.commands, wantCommands) {
		t.Fatalf("runner commands = %#v, want %#v", runner.commands, wantCommands)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
}

func TestCompileRegistryCoversProfileCompileKinds(t *testing.T) {
	for language, profile := range profiles.All() {
		if profile.CompileKind == "" {
			t.Fatalf("profile %s has empty compile kind", language)
		}
		if _, ok := lookupCompiler(profile.CompileKind); !ok {
			t.Fatalf("profile %s references missing compiler kind %q", language, profile.CompileKind)
		}
	}
}

func TestGoCompilerBuildsSoleNestedModuleFromModuleRoot(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := goCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{
			{Name: "src/go.mod"},
			{Name: "src/main.go"},
		}},
		Runner: runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("response = %+v", resp)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %+v, want one", runner.commands)
	}
	wantArgs := []string{
		"-C", filepath.Join(workDir, "src"),
		"build", "-buildvcs=false", "-tags=online_judge,ONLINE_JUDGE",
		"-o", filepath.Join(workDir, "Main"), ".",
	}
	if !reflect.DeepEqual(runner.commands[0].args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.commands[0].args, wantArgs)
	}
	if runner.commands[0].workDir != workDir {
		t.Fatalf("sandbox workDir = %q, want workspace root %q", runner.commands[0].workDir, workDir)
	}
}

func TestGoCompilerRejectsMultipleModuleFiles(t *testing.T) {
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}
	resp := goCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: t.TempDir(),
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{
			{Name: "go.mod"},
			{Name: "main.go"},
			{Name: "nested/go.mod"},
		}},
		Runner: runner,
	})
	if resp.Status != model.CompileStatusInvalid || !strings.Contains(resp.Reason, "multiple go.mod") {
		t.Fatalf("response = %+v", resp)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("compiler ran for ambiguous modules: %+v", runner.commands)
	}
}

func TestGoCompilerKeepsRootAndNoModuleBuildModes(t *testing.T) {
	tests := []struct {
		name    string
		sources []model.Source
		wantEnd []string
	}{
		{
			name:    "root module",
			sources: []model.Source{{Name: "go.mod"}, {Name: "main.go"}},
			wantEnd: []string{"build", "-buildvcs=false", "-tags=online_judge,ONLINE_JUDGE", "-o", "Main", "."},
		},
		{
			name:    "no module",
			sources: []model.Source{{Name: "src/main.go"}},
			wantEnd: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			runner := &recordingCommandRunner{
				result: CommandResult{Status: model.CompileStatusOK},
				hook: func(workDir, _ string, _ []string, _ []string) {
					if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("binary"), 0o755); err != nil {
						t.Fatal(err)
					}
				},
			}
			resp := goCompiler{}.Compile(context.Background(), CompileJob{WorkDir: workDir, Target: "Main", Request: &model.CompileRequest{Sources: tc.sources}, Runner: runner})
			if resp.Status != model.CompileStatusOK || len(runner.commands) != 1 {
				t.Fatalf("response=%+v commands=%+v", resp, runner.commands)
			}
			want := tc.wantEnd
			if want == nil {
				want = []string{"build", "-buildvcs=false", "-tags=online_judge,ONLINE_JUDGE", "-o", "Main", filepath.Join(workDir, "src/main.go")}
			}
			if !reflect.DeepEqual(runner.commands[0].args, want) {
				t.Fatalf("args = %#v, want %#v", runner.commands[0].args, want)
			}
		})
	}
}

func TestKFrameworkCompilerUsesMainDefinition(t *testing.T) {
	workDir := t.TempDir()
	for name, content := range map[string]string{
		"Helper.k": "module HELPER\nendmodule\n",
		"Main.k":   "module MAIN\nendmodule\n",
	} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}

	resp := kFrameworkCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{
			{Name: "Helper.k"},
			{Name: "Main.k"},
		}},
		Runner: runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	wantArgs := []string{filepath.Join(workDir, "Main.k")}
	if !reflect.DeepEqual(runner.commands[0].args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", runner.commands[0].args, wantArgs)
	}
}

func TestSingleSourceExecutableCompilerUsesPreferredSource(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"Helper.nim", "Main.nim"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("echo 1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := singleSourceExecutableCompiler{
		exts:           []string{".nim"},
		preferredBases: []string{"Main.nim"},
		noSourceReason: "no nim sources",
		bin:            "nim",
		args: func(job CompileJob, sourcePath string) []string {
			return []string{"c", "--out:" + outputPath(job), sourcePath}
		},
	}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Helper.nim"}, {Name: "Main.nim"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	wantSource := filepath.Join(workDir, "Main.nim")
	if got := runner.commands[0].args[len(runner.commands[0].args)-1]; got != wantSource {
		t.Fatalf("selected source = %q, want %q", got, wantSource)
	}
}

func TestIdris2CompilerCollectsChezExecutableBundle(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.idr"), []byte("main : IO ()\nmain = putStrLn \"ok\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			execDir := filepath.Join(workDir, "build", "exec")
			if err := os.MkdirAll(filepath.Join(execDir, "Main_app"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(execDir, "Main"), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(execDir, "Main_app", "Main.so"), []byte("native"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(execDir, "Main_app", "Main.ss"), []byte("(display \"ok\")"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := idris2Compiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.idr"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	wantArgs := []string{"--cg", "chez", "-o", "Main", filepath.Join(workDir, "Main.idr")}
	if !reflect.DeepEqual(runner.commands[0].args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", runner.commands[0].args, wantArgs)
	}
	if len(resp.Artifacts) != 3 {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
	names := []string{resp.Artifacts[0].Name, resp.Artifacts[1].Name, resp.Artifacts[2].Name}
	if !reflect.DeepEqual(names, []string{"Main", "Main_app/Main.so", "Main_app/Main.ss"}) {
		t.Fatalf("artifact names = %#v", names)
	}
	modes := []string{resp.Artifacts[0].Mode, resp.Artifacts[1].Mode, resp.Artifacts[2].Mode}
	if !reflect.DeepEqual(modes, []string{"exec", "exec", ""}) {
		t.Fatalf("artifact modes = %#v", modes)
	}
	wrapper, err := util.DecodeB64(resp.Artifacts[0].DataB64)
	if err != nil {
		t.Fatalf("decode wrapper: %v", err)
	}
	if body := string(wrapper); !strings.Contains(body, `exec "./Main_app/Main.so" "$@"`) || strings.Contains(body, "readlink") {
		t.Fatalf("unexpected idris2 wrapper: %q", body)
	}
}

func TestIdris2CompilerAddsForkFreeWrapperWhenBackendSkipsIt(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.idr"), []byte("main : IO ()\nmain = putStrLn \"ok\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			execDir := filepath.Join(workDir, "build", "exec", "Main_app")
			if err := os.MkdirAll(execDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(execDir, "Main.so"), []byte("native"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(execDir, "Main.ss"), []byte("(display \"ok\")"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := idris2Compiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.idr"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(resp.Artifacts) != 3 {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
	if resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("wrapper artifact = %+v", resp.Artifacts[0])
	}
	if resp.Artifacts[1].Name != "Main_app/Main.so" || resp.Artifacts[1].Mode != "exec" {
		t.Fatalf("main shared object artifact = %+v", resp.Artifacts[1])
	}
}

func TestMercuryCompilerUsesModuleMakeTarget(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "main.m"), []byte(":- module main.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			if err := os.WriteFile(filepath.Join(workDir, "main"), []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}

	compiler, ok := lookupCompiler("mercury")
	if !ok {
		t.Fatal("missing mercury compiler")
	}
	resp := compiler.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "main.m"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	wantArgs := []string{"--make", "--grade", "hlc.gc", "main"}
	if !reflect.DeepEqual(runner.commands[0].args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", runner.commands[0].args, wantArgs)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
}

func TestValaCompilerDoesNotPassUnsupportedOptimizationFlag(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.vala"), []byte("int main() { return 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}

	compiler, ok := lookupCompiler("vala")
	if !ok {
		t.Fatal("missing vala compiler")
	}
	resp := compiler.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.vala"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	for _, arg := range runner.commands[0].args {
		if arg == "-O" {
			t.Fatalf("vala args must not include unsupported -O flag: %#v", runner.commands[0].args)
		}
	}
}

func TestPythonLikeCompilerCollectsPythonArtifacts(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.py"), []byte("print(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			if err := os.WriteFile(filepath.Join(workDir, "Main.pyc"), []byte("bytecode"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := pythonLikeCompiler{interpreter: "python3"}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.py"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	if want := []string{"-I", "-S", "-m", "compileall", "-q", "-b", "Main.py"}; !reflect.DeepEqual(runner.commands[0].args, want) {
		t.Fatalf("runner args = %#v, want %#v", runner.commands[0].args, want)
	}
	if len(resp.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
}

func TestElmCompilerWritesProjectAndNodeWrapper(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.elm"), []byte("port module Main exposing (main)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(_ string, _ string, args, _ []string) {
			if err := os.WriteFile(args[len(args)-1], []byte("var Elm = { Main: { init: function () { return { ports: {} }; } } };"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := elmCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.elm"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	wantArgs := []string{"make", filepath.Join(workDir, "Main.elm"), "--output", filepath.Join(workDir, "aonohako-elm-compiled.js")}
	if !reflect.DeepEqual(runner.commands[0].args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", runner.commands[0].args, wantArgs)
	}
	if !reflect.DeepEqual(runner.commands[0].env, []string{"HOME=" + filepath.Join(workDir, ".home"), "GHCRTS=-N1"}) {
		t.Fatalf("runner env = %#v", runner.commands[0].env)
	}
	if _, err := os.Stat(filepath.Join(workDir, "elm.json")); err != nil {
		t.Fatalf("elm.json was not written: %v", err)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.js" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
	body, err := util.DecodeB64(resp.Artifacts[0].DataB64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `fs.readFileSync(0, "utf8")`) || !strings.Contains(string(body), "__aonohakoElm.Main.init") || !strings.Contains(string(body), "__aonohakoPorts.stdin.send") {
		t.Fatalf("elm wrapper missing runtime bridge: %s", string(body))
	}
}

func TestReScriptCompilerCollectsMainAndHelperJS(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.res"), []byte("Console.log(\"ok\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			outDir := filepath.Join(workDir, "lib", "js")
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outDir, "Main.js"), []byte("require('./Helper.js');\nconsole.log('ok');\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outDir, "Helper.js"), []byte("exports.value = 'ok';\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := reScriptCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.res"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	if !reflect.DeepEqual(runner.commands[0].args, []string{"build"}) {
		t.Fatalf("runner args = %#v", runner.commands[0].args)
	}
	if _, err := os.Stat(filepath.Join(workDir, "rescript.json")); err != nil {
		t.Fatalf("rescript.json was not written: %v", err)
	}
	if len(resp.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
	if got := []string{resp.Artifacts[0].Name, resp.Artifacts[1].Name}; !reflect.DeepEqual(got, []string{"Main.js", "Helper.js"}) {
		t.Fatalf("artifact names = %#v", got)
	}
}

func TestPureScriptCompilerWritesSpagoProjectAndCollectsOutput(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.purs"), []byte("module Main where\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			if _, err := os.Stat(filepath.Join(workDir, "src", "Main.purs")); err != nil {
				t.Fatalf("Main.purs was not copied into src: %v", err)
			}
			outDir := filepath.Join(workDir, "output", "Main")
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outDir, "index.js"), []byte("exports.main = function () {};\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := pureScriptCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.purs"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	if !reflect.DeepEqual(runner.commands[0].args, []string{"build"}) {
		t.Fatalf("runner args = %#v", runner.commands[0].args)
	}
	pureScriptHome := filepath.Join(workDir, ".purescript-home")
	if !reflect.DeepEqual(runner.commands[0].env, []string{"HOME=" + pureScriptHome, "XDG_CACHE_HOME=" + filepath.Join(pureScriptHome, ".cache")}) {
		t.Fatalf("runner env = %#v", runner.commands[0].env)
	}
	if _, err := os.Stat(filepath.Join(workDir, "spago.yaml")); err != nil {
		t.Fatalf("spago.yaml was not written: %v", err)
	}
	if len(resp.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
	if got := []string{resp.Artifacts[0].Name, resp.Artifacts[1].Name}; !reflect.DeepEqual(got, []string{"Main.js", "output/Main/index.js"}) {
		t.Fatalf("artifact names = %#v", got)
	}
	body, err := util.DecodeB64(resp.Artifacts[0].DataB64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `require("./output/Main/index.js").main();`) {
		t.Fatalf("wrapper body = %s", string(body))
	}
}

func TestNativeCompilerUsesRunnerAndReadsExecutable(t *testing.T) {
	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, "Main.c")
	if err := os.WriteFile(sourcePath, []byte("int main(void){return 0;}"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(workDir, _ string, _ []string, _ []string) {
			if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}

	resp := nativeCompiler{
		exts: []string{".c"},
		bin:  "gcc",
		flags: func(CompileJob) []string {
			return []string{"-O2", "-DONLINE_JUDGE=1"}
		},
	}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.c"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	wantArgs := []string{sourcePath, "-O2", "-DONLINE_JUDGE=1", "-o", "Main"}
	if got := runner.commands[0].args; !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", got, wantArgs)
	}
}

func TestScriptCheckCompilerUsesRunnerForEverySource(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"A.rb", "B.rb", "data.txt"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("puts 1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}

	resp := scriptCheckCompiler{exts: []string{".rb"}, noSourceReason: "no ruby sources", bin: "ruby", prefix: []string{"-c"}}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "A.rb"}, {Name: "B.rb"}, {Name: "data.txt"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	if len(resp.Artifacts) != 3 {
		t.Fatalf("artifacts = %+v, want source and data artifacts", resp.Artifacts)
	}
	if want := []string{"-c", filepath.Join(workDir, "A.rb")}; !reflect.DeepEqual(runner.commands[0].args, want) {
		t.Fatalf("first runner args = %#v, want %#v", runner.commands[0].args, want)
	}
	if want := []string{"-c", filepath.Join(workDir, "B.rb")}; !reflect.DeepEqual(runner.commands[1].args, want) {
		t.Fatalf("second runner args = %#v, want %#v", runner.commands[1].args, want)
	}
}

func TestCheckedSourcesCompilerUsesRunnerAndCollectsArtifacts(t *testing.T) {
	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, "Main.foo")
	if err := os.WriteFile(sourcePath, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK, Stdout: "out", Stderr: "err"}}

	resp := checkedSourcesCompiler{
		exts:           []string{".foo"},
		noSourceReason: "no foo sources",
		bin:            "checker",
		prefix:         []string{"--lint"},
		env:            []string{"CHECKER_ENV=1"},
	}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.foo"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if resp.Stdout != "out" || resp.Stderr != "err" {
		t.Fatalf("captured output = (%q, %q)", resp.Stdout, resp.Stderr)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.foo" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	got := runner.commands[0]
	if got.workDir != workDir || got.bin != "checker" {
		t.Fatalf("runner command = %+v", got)
	}
	if want := []string{"--lint", sourcePath}; !reflect.DeepEqual(got.args, want) {
		t.Fatalf("runner args = %#v, want %#v", got.args, want)
	}
	if want := []string{"CHECKER_ENV=1"}; !reflect.DeepEqual(got.env, want) {
		t.Fatalf("runner env = %#v, want %#v", got.env, want)
	}
}

func TestPassThroughCompilerRequiresMatchingSource(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.scm"), []byte("(display 1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp := passThroughCompiler{exts: []string{".scm"}, noSourceReason: "no scheme sources"}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.scm"}}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.scm" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}

	resp = passThroughCompiler{exts: []string{".scm"}, noSourceReason: "no scheme sources"}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.txt"}}},
	})
	if resp.Status != model.CompileStatusInvalid || resp.Reason != "no scheme sources" {
		t.Fatalf("missing source response = %+v", resp)
	}
}

func TestPicoLispCompilerPassesThroughDotLSource(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.l"), []byte("(prinl \"ok\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compiler, ok := compileRegistry["picolisp"]
	if !ok {
		t.Fatal("picolisp compiler missing from registry")
	}

	resp := compiler.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.l"}}},
	})
	if resp.Status != model.CompileStatusOK || len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.l" {
		t.Fatalf("picolisp compile response = %+v, want preserved Main.l artifact", resp)
	}

	resp = compiler.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.lisp"}}},
	})
	if resp.Status != model.CompileStatusInvalid || resp.Reason != "no picolisp sources" {
		t.Fatalf("picolisp wrong-extension response = %+v", resp)
	}
}
