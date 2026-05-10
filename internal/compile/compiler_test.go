package compile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"aonohako/internal/model"
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
		"pascal", "nim", "zig", "ada", "d",
		"rust", "go", "java",
		"scheme", "awk", "tcl", "gdl", "octave", "carbon", "graphql", "lean4", "agda", "dafny", "tla", "why3",
		"vhdl", "verilog", "crystal", "vlang", "odin", "c3", "hare", "vbnet", "gleam", "cuda-ocelot", "rocq", "isabelle",
		"python", "pypy",
		"racket", "javascript", "ruby", "php", "lua", "perl",
		"raku", "vb6", "smalltalk", "golfscript", "duckdb", "bqn", "apl", "uiua", "janet", "sed", "bc", "forth",
	} {
		if _, ok := lookupCompiler(kind); !ok {
			t.Fatalf("missing compiler registry entry for %s", kind)
		}
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
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("runner commands = %+v", runner.commands)
	}
	if want := []string{"-I", "-S", "-m", "compileall", "-b", "."}; !reflect.DeepEqual(runner.commands[0].args, want) {
		t.Fatalf("runner args = %#v, want %#v", runner.commands[0].args, want)
	}
	if len(resp.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
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
	for _, name := range []string{"A.rb", "B.rb"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte("puts 1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}

	resp := scriptCheckCompiler{bin: "ruby", prefix: []string{"-c"}}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "A.rb"}, {Name: "B.rb"}}},
		Runner:  runner,
	})

	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status = %s, reason = %s", resp.Status, resp.Reason)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("runner commands = %+v", runner.commands)
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
