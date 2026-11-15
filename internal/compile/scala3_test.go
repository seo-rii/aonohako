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

func TestScala3CompilerUsesTrustedOfflineClasspathAndPublishesClasses(t *testing.T) {
	root := t.TempDir()
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}, hook: func(workDir, _ string, _, _ []string) {
		for name, body := range map[string]string{"Main.class": "class", "Main.tasty": "typed IR", "shadow.jar": "submitted jar", "notes.txt": "notes"} {
			if err := os.WriteFile(filepath.Join(workDir, name), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}}
	compiler, ok := lookupCompiler("scala3")
	if !ok {
		t.Fatal("Scala 3 compiler absent")
	}
	got := compiler.Compile(context.Background(), CompileJob{WorkDir: root, Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.scala"}, {Name: "Helper.scala"}, {Name: "shadow.jar"}}}, Runner: runner})
	if got.Status != model.CompileStatusOK || len(got.Artifacts) != 1 || got.Artifacts[0].Name != "Main.class" || got.Artifacts[0].DataB64 != util.EncodeB64([]byte("class")) {
		t.Fatalf("response = %+v", got)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %+v", runner.commands)
	}
	cmd := runner.commands[0]
	want := []string{"-cp", filepath.Join(profiles.Scala3Home, "lib", "*"), "dotty.tools.dotc.Main", "-classpath", profiles.Scala3RuntimeClasspath, "-release", "17", "-encoding", "UTF-8", "-color:never", "-d", root, filepath.Join(root, "Helper.scala"), filepath.Join(root, "Main.scala")}
	if cmd.bin != "java" || !reflect.DeepEqual(cmd.args, want) {
		t.Fatalf("command = %+v, want java %v", cmd, want)
	}
	if !reflect.DeepEqual(cmd.env, javaCompileEnv(root, 768)) {
		t.Fatalf("JVM environment = %v", cmd.env)
	}
}

func TestScala3CompilerPreservesFailuresAndRejectsMissingClasses(t *testing.T) {
	for _, tc := range []struct {
		name, source, status, reason string
		result                       CommandResult
	}{
		{name: "no sources", source: "Readme.txt", status: model.CompileStatusInvalid, reason: "no scala3 sources"},
		{name: "type failure", source: "Main.scala", status: model.CompileStatusCompileError, reason: "compile failed", result: CommandResult{Status: model.CompileStatusCompileError, Stderr: "type mismatch", Reason: "compile failed"}},
		{name: "missing classes", source: "Main.scala", status: model.CompileStatusInternal, reason: "scala3 compiler produced no artifacts", result: CommandResult{Status: model.CompileStatusOK}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingCommandRunner{result: tc.result}
			got := (scala3Compiler{}).Compile(context.Background(), CompileJob{WorkDir: t.TempDir(), Request: &model.CompileRequest{Sources: []model.Source{{Name: tc.source}}}, Runner: runner})
			if got.Status != tc.status || !strings.Contains(got.Reason, tc.reason) || got.Stderr != tc.result.Stderr || len(got.Artifacts) != 0 {
				t.Fatalf("response = %+v", got)
			}
			if tc.status == model.CompileStatusInvalid && len(runner.commands) != 0 {
				t.Fatal("invoked JVM without Scala source")
			}
		})
	}
}
