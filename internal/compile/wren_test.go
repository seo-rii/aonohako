package compile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

func TestWrenCompilePreservesSourcesWithoutEvaluatingThem(t *testing.T) {
	compiler, ok := lookupCompiler("wren")
	if !ok {
		t.Fatal("Wren compiler missing")
	}
	workDir := t.TempDir()
	files := map[string]string{
		"Main.wren":    "import \"./helper\" for Helper\nSystem.print(Helper.answer)\n",
		"helper.wren":  "class Helper { static answer { 42 } }\n",
		"invalid.wren": "var broken =\n",
	}
	sources := make([]model.Source, 0, len(files))
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(body), 0o444); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, model.Source{Name: name})
	}
	runner := &recordingCommandRunner{}
	resp := compiler.Compile(context.Background(), CompileJob{WorkDir: workDir, Request: &model.CompileRequest{Sources: sources}, Runner: runner})
	if resp.Status != model.CompileStatusOK || len(resp.Artifacts) != len(files) {
		t.Fatalf("response = %+v", resp)
	}
	for _, artifact := range resp.Artifacts {
		if body, ok := files[artifact.Name]; !ok || artifact.DataB64 != util.EncodeB64([]byte(body)) {
			t.Fatalf("source artifact changed: %+v", artifact)
		}
	}
	if len(runner.commands) != 0 {
		t.Fatalf("compile executed submitted Wren: %+v", runner.commands)
	}
	resp = compiler.Compile(context.Background(), CompileJob{WorkDir: workDir, Request: &model.CompileRequest{Sources: []model.Source{{Name: "data.txt"}}}, Runner: runner})
	if resp.Status != model.CompileStatusInvalid || resp.Reason != "no Wren sources" {
		t.Fatalf("missing Wren source response = %+v", resp)
	}
}
