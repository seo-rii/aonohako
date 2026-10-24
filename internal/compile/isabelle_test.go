package compile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"aonohako/internal/model"
)

func TestIsabelleCompilerBuildsDeclaredSessionAndPreservesProofFailure(t *testing.T) {
	workDir := t.TempDir()
	root := "session Submitted_Proof = HOL +\n  theories Impossible\n"
	for name, content := range map[string]string{
		"ROOT": root,
		"Impossible.thy": "theory Impossible imports Main begin\n" +
			"theorem impossible: False by simp\nend\n",
	} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingCommandRunner{result: CommandResult{
		Status: model.CompileStatusCompileError,
		Stdout: "Running Submitted_Proof ...\n",
		Stderr: "Failed to finish proof (line 2 of Impossible.thy)\n",
		Reason: "exit status 1",
	}}
	response := isabelleCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "ROOT"}, {Name: "Impossible.thy"}}},
		Runner:  runner,
	})
	// process_theories -D . creates an adhoc Draft session. Without explicit
	// theory arguments it succeeds without checking any of this ROOT's proofs.
	// build -D . must instead select the submitted ROOT's actual sessions.
	if len(runner.commands) != 1 {
		t.Fatalf("session checker calls = %d, want one; response = %+v", len(runner.commands), response)
	}
	command := runner.commands[0]
	if command.bin != "isabelle" || command.workDir != workDir || !reflect.DeepEqual(command.args, []string{"build", "-o", "naproche_server=false", "-D", "."}) {
		t.Fatalf("command does not build the submitted ROOT session: %+v", command)
	}
	if data, err := os.ReadFile(filepath.Join(workDir, "ROOT")); err != nil || string(data) != root {
		t.Fatalf("submitted ROOT changed: %q, %v", data, err)
	}
	if response.Status != runner.result.Status || response.Stdout != runner.result.Stdout || response.Stderr != runner.result.Stderr || response.Reason != runner.result.Reason {
		t.Fatalf("proof failure was not preserved: %+v", response)
	}
	if len(response.Artifacts) != 0 {
		t.Fatalf("failed proof returned artifacts: %+v", response.Artifacts)
	}
}

func TestIsabelleCompilerBuildsGeneratedSessionAndReturnsCheckedSources(t *testing.T) {
	workDir := t.TempDir()
	sources := []model.Source{{Name: "Second.thy"}, {Name: "First.thy"}}
	for _, source := range sources {
		if err := os.WriteFile(filepath.Join(workDir, source.Name), []byte("theory "+source.Name[:len(source.Name)-4]+" imports Main begin\nlemma valid: True by simp\nend\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK, Stdout: "Finished Aonohako\n"}}
	response := isabelleCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: sources},
		Runner:  runner,
	})
	if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0].args, []string{"build", "-o", "naproche_server=false", "-D", "."}) {
		t.Fatalf("generated session was not checked: %+v; response = %+v", runner.commands, response)
	}
	if data, err := os.ReadFile(filepath.Join(workDir, "ROOT")); err != nil || string(data) != "session Aonohako = HOL +\n  theories\n    First\n    Second\n" {
		t.Fatalf("generated ROOT does not include both submitted theories: %q, %v", data, err)
	}
	if response.Status != model.CompileStatusOK || response.Stdout != runner.result.Stdout {
		t.Fatalf("checked sources were not accepted: %+v", response)
	}
	var artifactNames []string
	for _, artifact := range response.Artifacts {
		artifactNames = append(artifactNames, artifact.Name)
	}
	if !reflect.DeepEqual(artifactNames, []string{"First.thy", "ROOT", "Second.thy"}) {
		t.Fatalf("checked artifacts = %v", artifactNames)
	}
}
