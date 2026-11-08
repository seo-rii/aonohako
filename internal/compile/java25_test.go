package compile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/util"
)

func TestJava25VersionSelectsMatchingCompilerAndRuntime(t *testing.T) {
	for _, tc := range []struct {
		lang, version, release, runLang string
	}{
		{lang: "JAVA25", release: "25", runLang: "java25"},
		{lang: "JAVA", version: "25", release: "25", runLang: "java25"},
		{lang: "java", version: "java25", release: "25", runLang: "java25"},
		{lang: "JAVA21", version: "--release=25", release: "25", runLang: "java25"},
		{lang: "JAVA25", version: "21", release: "21", runLang: "java"},
		{lang: "JAVA25", version: "8", release: "8", runLang: "java"},
	} {
		t.Run(tc.lang+"/"+tc.version, func(t *testing.T) {
			profile, ok := resolveProfile(tc.lang)
			if !ok {
				t.Fatalf("resolveProfile(%q) failed", tc.lang)
			}
			got, err := applyRequestedVersion(profile, tc.version)
			if err != nil {
				t.Fatal(err)
			}
			if got.JavaRelease != tc.release || got.RunLang != tc.runLang {
				t.Fatalf("versioned profile = %+v, want release %s with runtime %s", got, tc.release, tc.runLang)
			}
		})
	}
	for _, lang := range []string{"KOTLIN_JVM", "KOTLIN_JAVA21"} {
		profile, ok := resolveProfile(lang)
		if !ok {
			t.Fatalf("resolveProfile(%q) failed", lang)
		}
		if _, err := applyRequestedVersion(profile, "25"); err == nil {
			t.Errorf("%s unexpectedly accepts unsupported Kotlin JVM target 25", lang)
		}
	}
}

func TestJavaCompilerSelectsDedicated25ToolchainAndPublishesClasses(t *testing.T) {
	for _, release := range []string{"8", "11", "15", "17", "21", "25"} {
		t.Run(release, func(t *testing.T) {
			workDir := t.TempDir()
			runner := &recordingCommandRunner{
				result: CommandResult{Status: model.CompileStatusOK, Stdout: "compile output", Stderr: "compile diagnostic"},
				hook: func(workDir, _ string, _, _ []string) {
					if err := os.WriteFile(filepath.Join(workDir, "Main.class"), []byte("class bytes"), 0o644); err != nil {
						t.Fatal(err)
					}
				},
			}
			if err := os.WriteFile(filepath.Join(workDir, "Main.java"), []byte("class Main {}"), 0o644); err != nil {
				t.Fatal(err)
			}
			response := javaCompiler{}.Compile(context.Background(), CompileJob{
				WorkDir: workDir,
				Profile: profiles.Profile{JavaRelease: release},
				Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.java"}, {Name: "notes.txt"}}},
				Runner:  runner,
			})
			if response.Status != model.CompileStatusOK || response.Stdout != "compile output" || response.Stderr != "compile diagnostic" {
				t.Fatalf("response = %+v", response)
			}
			if len(runner.commands) != 1 {
				t.Fatalf("commands = %+v, want one javac invocation", runner.commands)
			}
			command := runner.commands[0]
			wantBin := "javac"
			if release == "25" {
				wantBin = filepath.Join(profiles.Java25Home, "bin", "javac")
			}
			wantArgs := []string{"--release", release, "-encoding", "UTF-8", filepath.Join(workDir, "Main.java")}
			if command.bin != wantBin || command.workDir != workDir || !reflect.DeepEqual(command.args, wantArgs) {
				t.Fatalf("command = %+v, want %s %v in %s", command, wantBin, wantArgs, workDir)
			}
			if len(response.Artifacts) != 1 || response.Artifacts[0].Name != "Main.class" || response.Artifacts[0].DataB64 != util.EncodeB64([]byte("class bytes")) {
				t.Fatalf("artifacts = %+v, want compiled class only", response.Artifacts)
			}
		})
	}
}

func TestJava25CompilerPreservesFailuresAndRequiresClasses(t *testing.T) {
	for _, tc := range []struct {
		name, source, status, reason string
		result                       CommandResult
	}{
		{name: "no Java sources", source: "README.txt", status: model.CompileStatusInvalid, reason: "no java sources"},
		{name: "compiler rejects source", source: "Main.java", status: model.CompileStatusCompileError, reason: "compile failed", result: CommandResult{Status: model.CompileStatusCompileError, Reason: "compile failed", Stderr: "syntax error"}},
		{name: "compiler produces no classes", source: "Main.java", status: model.CompileStatusInternal, reason: "javac produced no artifacts", result: CommandResult{Status: model.CompileStatusOK}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingCommandRunner{result: tc.result}
			response := javaCompiler{}.Compile(context.Background(), CompileJob{
				WorkDir: t.TempDir(),
				Profile: profiles.Profile{JavaRelease: "25"},
				Request: &model.CompileRequest{Sources: []model.Source{{Name: tc.source}}},
				Runner:  runner,
			})
			if response.Status != tc.status || !strings.Contains(response.Reason, tc.reason) || len(response.Artifacts) != 0 {
				t.Fatalf("response = %+v, want %s/%s without artifacts", response, tc.status, tc.reason)
			}
			if response.Stderr != tc.result.Stderr {
				t.Fatalf("compiler diagnostic = %q, want %q", response.Stderr, tc.result.Stderr)
			}
			if tc.source == "README.txt" && len(runner.commands) != 0 {
				t.Fatalf("compiler invoked without Java sources: %+v", runner.commands)
			}
		})
	}
}
