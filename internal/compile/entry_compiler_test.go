package compile

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"aonohako/internal/model"
)

func TestEntryCompilerUsesTrustedValidatorAndPreservesJSON(t *testing.T) {
	profile, ok := resolveProfile("ENTRY")
	if !ok || profile.RunLang != "entry" || profile.Extension != "entry.json" {
		t.Fatalf("profile=%+v", profile)
	}
	if _, err := applyRequestedVersion(profile, "entry-v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := applyRequestedVersion(profile, "entry-v2"); err == nil {
		t.Fatal("accepted unsupported grader")
	}
	dir := t.TempDir()
	source := []byte(`{"format":"entry-submission"}`)
	if err := os.WriteFile(filepath.Join(dir, "Main.entry.json"), source, 0600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}
	job := CompileJob{WorkDir: dir, Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.entry.json"}}}, Runner: runner}
	result := entryCompiler{}.Compile(context.Background(), job)
	if result.Status != model.CompileStatusOK || len(result.Artifacts) != 1 || result.Artifacts[0].Name != "Main.entry.json" || result.Artifacts[0].Mode != "" {
		t.Fatalf("result=%+v", result)
	}
	if len(runner.commands) != 1 || runner.commands[0].bin != "node" || !slices.Contains(runner.commands[0].args, "/usr/local/lib/aonohako/entry-runner.mjs") || !slices.Contains(runner.commands[0].args, "--check") {
		t.Fatalf("commands=%+v", runner.commands)
	}
	job.Request.Sources = append(job.Request.Sources, model.Source{Name: "payload.js"})
	if result := (entryCompiler{}).Compile(context.Background(), job); result.Status != model.CompileStatusInvalid {
		t.Fatal("accepted additional executable source")
	}
}
