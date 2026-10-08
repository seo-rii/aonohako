package compile

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"aonohako/internal/model"
)

func TestGolfScriptCompilePassesThroughReportedPrograms(t *testing.T) {
	compiler, ok := lookupCompiler("golfscript")
	if !ok {
		t.Fatal("GolfScript compiler is missing")
	}
	for _, program := range []string{"~ +", "~+", "+"} {
		t.Run(program, func(t *testing.T) {
			workDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(workDir, "Main.gs"), []byte(program), 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingCommandRunner{}
			resp := compiler.Compile(context.Background(), CompileJob{
				WorkDir: workDir,
				Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.gs"}}},
				Runner:  runner,
			})
			if resp.Status != model.CompileStatusOK || len(resp.Artifacts) != 1 {
				t.Fatalf("compile response = %+v", resp)
			}
			if artifact := resp.Artifacts[0]; artifact.Name != "Main.gs" || artifact.DataB64 != base64.StdEncoding.EncodeToString([]byte(program)) {
				t.Fatalf("GolfScript source changed during compilation: %+v", artifact)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("GolfScript compilation executed a command: %+v", runner.commands)
			}
		})
	}
}
