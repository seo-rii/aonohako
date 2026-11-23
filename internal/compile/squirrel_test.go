package compile

import (
	"aonohako/internal/model"
	"aonohako/internal/util"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSquirrelCompilation(t *testing.T) {
	work := t.TempDir()
	body := []byte("print(42);\n")
	if err := os.WriteFile(filepath.Join(work, "Main.nut"), body, 0444); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}
	runner.hook = func(workDir, _ string, _, _ []string) {
		if err := os.WriteFile(filepath.Join(workDir, "Main.cnut"), []byte("compiled bytecode"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	c, ok := lookupCompiler("squirrel")
	if !ok {
		t.Fatal("compiler missing")
	}
	resp := c.Compile(context.Background(), CompileJob{WorkDir: work, Target: "Main.cnut", Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.nut"}}}, Runner: runner})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("response=%+v", resp)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands=%+v", runner.commands)
	}
	want := []string{"-c", "-o", filepath.Join(work, "Main.cnut"), filepath.Join(work, "Main.nut")}
	if got := runner.commands[0]; got.bin != "aonohako-squirrel" || !reflect.DeepEqual(got.args, want) {
		t.Fatalf("command=%+v want=%v", got, want)
	}
	if len(resp.Artifacts) != 2 || resp.Artifacts[0].Name != "Main.cnut" || resp.Artifacts[0].DataB64 != util.EncodeB64([]byte("compiled bytecode")) || resp.Artifacts[1].Name != "Main.nut" {
		t.Fatalf("artifacts=%+v", resp.Artifacts)
	}
	data, err := os.ReadFile(filepath.Join(work, "Main.nut"))
	if err != nil || string(data) != string(body) {
		t.Fatalf("source mutated: %q %v", data, err)
	}
}
func TestSquirrelRejectsMissingSourceAndPropagatesCompileFailure(t *testing.T) {
	c, ok := lookupCompiler("squirrel")
	if !ok {
		t.Fatal("compiler missing")
	}
	for _, tc := range []struct {
		name, source, want string
		result             CommandResult
	}{
		{name: "missing source", source: "notes.txt", want: model.CompileStatusInvalid},
		{name: "invalid syntax", source: "Main.nut", want: model.CompileStatusCompileError, result: CommandResult{Status: model.CompileStatusCompileError, Reason: "syntax failed", Stderr: "bad source"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingCommandRunner{result: tc.result}
			resp := c.Compile(context.Background(), CompileJob{WorkDir: t.TempDir(), Target: "Main.cnut", Request: &model.CompileRequest{Sources: []model.Source{{Name: tc.source}}}, Runner: r})
			if resp.Status != tc.want || len(resp.Artifacts) != 0 || resp.Stderr != tc.result.Stderr {
				t.Fatalf("response=%+v", resp)
			}
			if tc.want == model.CompileStatusInvalid && len(r.commands) != 0 {
				t.Fatal("ran without source")
			}
		})
	}
}
