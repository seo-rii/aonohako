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

func TestPARIGPCompilation(t *testing.T) {
	work := t.TempDir()
	body := []byte("print(42);\n")
	if err := os.WriteFile(filepath.Join(work, "Main.gp"), body, 0444); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}}

	c, ok := lookupCompiler("pari-gp")
	if !ok {
		t.Fatal("compiler missing")
	}
	resp := c.Compile(context.Background(), CompileJob{WorkDir: work, Target: "Main.gp", Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.gp"}}}, Runner: runner})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("response=%+v", resp)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands=%+v", runner.commands)
	}
	want := []string{"--aonohako-check", "-f", "-q", filepath.Join(work, "Main.gp")}
	if got := runner.commands[0]; got.bin != "aonohako-pari-gp" || !reflect.DeepEqual(got.args, want) {
		t.Fatalf("command=%+v want=%v", got, want)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.gp" || resp.Artifacts[0].DataB64 != util.EncodeB64(body) {
		t.Fatalf("artifacts=%+v", resp.Artifacts)
	}
	data, err := os.ReadFile(filepath.Join(work, "Main.gp"))
	if err != nil || string(data) != string(body) {
		t.Fatalf("source mutated: %q %v", data, err)
	}
}
func TestPARIGPRejectsMissingSourceAndPropagatesCompileFailure(t *testing.T) {
	c, ok := lookupCompiler("pari-gp")
	if !ok {
		t.Fatal("compiler missing")
	}
	for _, tc := range []struct {
		name, source, want string
		result             CommandResult
	}{
		{name: "missing source", source: "notes.txt", want: model.CompileStatusInvalid},
		{name: "invalid syntax", source: "Main.gp", want: model.CompileStatusCompileError, result: CommandResult{Status: model.CompileStatusCompileError, Reason: "syntax failed", Stderr: "bad source"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingCommandRunner{result: tc.result}
			resp := c.Compile(context.Background(), CompileJob{WorkDir: t.TempDir(), Target: "Main.gp", Request: &model.CompileRequest{Sources: []model.Source{{Name: tc.source}}}, Runner: r})
			if resp.Status != tc.want || len(resp.Artifacts) != 0 || resp.Stderr != tc.result.Stderr {
				t.Fatalf("response=%+v", resp)
			}
			if tc.want == model.CompileStatusInvalid && len(r.commands) != 0 {
				t.Fatal("ran without source")
			}
		})
	}
}
