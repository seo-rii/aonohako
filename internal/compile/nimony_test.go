package compile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/util"
)

func TestNimonyCompilerSelectsRootAndPublishesOnlyExecutable(t *testing.T) {
	compiler, ok := lookupCompiler("nimony")
	if !ok {
		t.Fatal("Nimony compiler missing")
	}
	for _, root := range []string{"Main.nim", "main.nim", "nested/program.nim"} {
		t.Run(root, func(t *testing.T) {
			workDir := t.TempDir()
			const target = "Answer"
			sources := []model.Source{{Name: root}}
			if root == "Main.nim" || root == "main.nim" {
				sources = append(sources, model.Source{Name: "A.nim"})
			}
			runner := &recordingCommandRunner{
				result: CommandResult{Status: model.CompileStatusOK, Stdout: "compiler output", Stderr: "compiler diagnostic"},
				hook: func(workDir, _ string, _, _ []string) {
					if err := os.WriteFile(filepath.Join(workDir, target), []byte("native executable"), 0o755); err != nil {
						t.Fatal(err)
					}
					cache := filepath.Join(workDir, ".cache", "nimony")
					if err := os.MkdirAll(cache, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(cache, "generated.c"), []byte("cached code"), 0o600); err != nil {
						t.Fatal(err)
					}
				},
			}
			resp := compiler.Compile(context.Background(), CompileJob{
				WorkDir: workDir, Target: target,
				Request: &model.CompileRequest{Sources: sources}, Runner: runner,
			})
			if resp.Status != model.CompileStatusOK || resp.Stdout != "compiler output" || resp.Stderr != "compiler diagnostic" {
				t.Fatalf("response = %+v", resp)
			}
			if len(runner.commands) != 1 {
				t.Fatalf("commands = %+v", runner.commands)
			}
			command := runner.commands[0]
			wantArgs := []string{"c", "-d:release", "-d:ONLINE_JUDGE", "--opt:speed", "--parallelBuild:1", "--nimcache:" + filepath.Join(workDir, ".cache", "nimony"), "--out:" + filepath.Join(workDir, target), filepath.Join(workDir, root)}
			if command.bin != "nimony" || command.workDir != workDir || !reflect.DeepEqual(command.args, wantArgs) {
				t.Fatalf("command = %+v, want nimony %v", command, wantArgs)
			}
			if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != target || resp.Artifacts[0].Mode != "exec" || resp.Artifacts[0].DataB64 != util.EncodeB64([]byte("native executable")) {
				t.Fatalf("artifacts = %+v", resp.Artifacts)
			}
		})
	}
}

func TestNimonyCompilerRejectsMissingSourcesAndPreservesFailure(t *testing.T) {
	compiler, ok := lookupCompiler("nimony")
	if !ok {
		t.Fatal("Nimony compiler missing")
	}
	for _, tc := range []struct {
		name, source, status string
		result               CommandResult
	}{
		{name: "missing source", source: "README.txt", status: model.CompileStatusInvalid},
		{name: "syntax failure", source: "Main.nim", status: model.CompileStatusCompileError, result: CommandResult{Status: model.CompileStatusCompileError, Reason: "compile failed", Stderr: "syntax error"}},
		{name: "missing artifact", source: "Main.nim", status: model.CompileStatusInternal, result: CommandResult{Status: model.CompileStatusOK}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingCommandRunner{result: tc.result}
			resp := compiler.Compile(context.Background(), CompileJob{
				WorkDir: t.TempDir(), Target: "Main", Request: &model.CompileRequest{Sources: []model.Source{{Name: tc.source}}}, Runner: runner,
			})
			if resp.Status != tc.status || len(resp.Artifacts) != 0 || resp.Stderr != tc.result.Stderr {
				t.Fatalf("response = %+v", resp)
			}
			if tc.status == model.CompileStatusInvalid && (len(runner.commands) != 0 || resp.Reason != "no nimony sources") {
				t.Fatalf("missing source response = %+v, commands = %+v", resp, runner.commands)
			}
			if tc.result.Reason != "" && resp.Reason != tc.result.Reason {
				t.Fatalf("reason = %q, want %q", resp.Reason, tc.result.Reason)
			}
		})
	}
}

func TestNimonyProfileUsesNativeExecution(t *testing.T) {
	for _, lang := range []string{"NIMONY", "nimony", " Nimony "} {
		profile, ok := profiles.Resolve(lang)
		if !ok || profile.SourceLang != "NIMONY" || profile.Extension != "nim" || profile.CompileKind != "nimony" || profile.DefaultTarget != "Main" || profile.RunLang != "binary" {
			t.Fatalf("Resolve(%q) = %+v, %v", lang, profile, ok)
		}
		if got := profiles.NormalizeRunLang(lang); got != "binary" {
			t.Fatalf("NormalizeRunLang(%q) = %q", lang, got)
		}
	}
}
