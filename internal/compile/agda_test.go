package compile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestAgdaCompilerBoundsLibraryDiscoveryAndPublishesOnlySources(t *testing.T) {
	workDir := t.TempDir()
	sourcePath := filepath.Join(workDir, "Main.agda")
	if err := os.WriteFile(sourcePath, []byte("module Main where\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(dir, bin string, args, _ []string) {
			if bin != "agda" || dir != workDir || len(args) != 1 || args[0] != sourcePath {
				t.Fatalf("unexpected checker command: %s %v in %s", bin, args, dir)
			}
			libraries, err := filepath.Glob(filepath.Join(dir, "*.agda-lib"))
			if err != nil || len(libraries) != 1 {
				t.Fatalf("library roots = %v, error = %v", libraries, err)
			}
			body, err := os.ReadFile(libraries[0])
			if err != nil || string(body) != "include: .\n" {
				t.Fatalf("library definition = %q, error = %v", body, err)
			}
			info, err := os.Stat(libraries[0])
			if err != nil || info.Mode().Perm() != 0o444 {
				t.Fatalf("library root must be readable and immutable to the sandbox: info=%v err=%v", info, err)
			}
		},
	}
	compiler, _ := lookupCompiler("agda")
	resp := compiler.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.agda"}}},
		Runner:  runner,
	})
	if resp.Status != model.CompileStatusOK || len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.agda" {
		t.Fatalf("unexpected compile response: %+v", resp)
	}
	libraries, err := filepath.Glob(filepath.Join(workDir, "*.agda-lib"))
	if err != nil || len(libraries) != 0 {
		t.Fatalf("generated library roots remain after compile: %v, error = %v", libraries, err)
	}
}

func TestAgdaCompilerPreservesSubmittedLibraryAndCheckerFailure(t *testing.T) {
	workDir := t.TempDir()
	libraryPath := filepath.Join(workDir, "project.agda-lib")
	libraryBody := "name: project\ninclude: .\nflags: --safe\n"
	if err := os.WriteFile(libraryPath, []byte(libraryBody), 0o444); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusCompileError, Stderr: "false != true", Reason: "checker failed"},
		hook: func(dir, _ string, _, _ []string) {
			libraries, err := filepath.Glob(filepath.Join(dir, "*.agda-lib"))
			if err != nil || len(libraries) != 1 || libraries[0] != libraryPath {
				t.Fatalf("submitted library root was replaced: %v, error = %v", libraries, err)
			}
			body, err := os.ReadFile(libraryPath)
			if err != nil || string(body) != libraryBody {
				t.Fatalf("submitted library changed: %q, error = %v", body, err)
			}
		},
	}
	resp := (agdaCompiler{}).Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.agda"}, {Name: "project.agda-lib"}}},
		Runner:  runner,
	})
	if resp.Status != model.CompileStatusCompileError || resp.Stderr != "false != true" || len(resp.Artifacts) != 0 {
		t.Fatalf("checker failure was not preserved: %+v", resp)
	}
	if _, err := os.Stat(libraryPath); err != nil {
		t.Fatalf("submitted library was removed: %v", err)
	}
}

func TestAgdaCompilerStopsBeforeCheckerWhenLibraryRootCannotBePrepared(t *testing.T) {
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}
	resp := (agdaCompiler{}).Compile(context.Background(), CompileJob{
		WorkDir: filepath.Join(t.TempDir(), "missing"),
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.agda"}}},
		Runner:  runner,
	})
	if resp.Status != model.CompileStatusInternal || !strings.Contains(resp.Reason, "agda library root preparation failed") || len(runner.commands) != 0 {
		t.Fatalf("unprepared library root reached the checker: response=%+v commands=%v", resp, runner.commands)
	}
}
