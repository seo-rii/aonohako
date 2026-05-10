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
}

func (r *recordingCommandRunner) Run(_ context.Context, workDir, bin string, args, env []string) CommandResult {
	r.commands = append(r.commands, recordedCommand{
		workDir: workDir,
		bin:     bin,
		args:    append([]string{}, args...),
		env:     append([]string{}, env...),
	})
	return r.result
}

func TestCompileRegistryIncludesSimpleCompilers(t *testing.T) {
	for _, kind := range []string{
		"scheme", "awk", "tcl", "gdl", "octave", "carbon", "graphql", "lean4", "agda", "dafny", "tla", "why3",
		"raku", "vb6", "smalltalk", "golfscript", "duckdb", "bqn", "apl", "uiua", "janet", "sed", "bc", "forth",
	} {
		if _, ok := lookupCompiler(kind); !ok {
			t.Fatalf("missing compiler registry entry for %s", kind)
		}
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
