package compile

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"aonohako/internal/model"
)

func b64String(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func b64Bytes(v []byte) string {
	return base64.StdEncoding.EncodeToString(v)
}

func TestRunRejectsInvalidTargetPath(t *testing.T) {
	svc := New()
	tests := []string{"../escape", "nested/Main", "/tmp/Main"}
	for _, target := range tests {
		resp := svc.Run(context.Background(), &model.CompileRequest{
			Lang:   "UHMLANG",
			Target: target,
			Sources: []model.Source{{
				Name:    "Main.uhm",
				DataB64: b64String("text"),
			}},
		})
		if resp.Status != model.CompileStatusInvalid {
			t.Fatalf("target=%q status=%q want=%q", target, resp.Status, model.CompileStatusInvalid)
		}
	}
}

func TestRunRejectsOversizedSource(t *testing.T) {
	svc := New()
	large := bytes.Repeat([]byte("a"), 17<<20)
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "UHMLANG",
		Sources: []model.Source{{
			Name:    "Main.uhm",
			DataB64: b64Bytes(large),
		}},
	})
	if resp.Status != model.CompileStatusInvalid {
		t.Fatalf("status=%q want=%q", resp.Status, model.CompileStatusInvalid)
	}
}

func TestCompileCSharpMaterializesProjectSources(t *testing.T) {
	workDir := t.TempDir()
	_ = compileCSharp(context.Background(), workDir, []model.Source{
		{
			Name:    "src/App/App.csproj",
			DataB64: b64String("<Project Sdk=\"Microsoft.NET.Sdk\"></Project>"),
		},
		{
			Name:    "src/App/Program.cs",
			DataB64: b64String("class Program { static void Main() {} }"),
		},
	})

	if _, err := os.Stat(filepath.Join(workDir, "csproj", "src", "App", "App.csproj")); err != nil {
		t.Fatalf("expected App.csproj to be materialized, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "csproj", "src", "App", "Program.cs")); err != nil {
		t.Fatalf("expected Program.cs to preserve directory structure, err=%v", err)
	}
}

func TestCollectArtifactsRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.bin"), bytes.Repeat([]byte("x"), 20<<20), 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	if _, err := collectArtifacts(root, func(string) bool { return true }, ""); err == nil {
		t.Fatalf("expected oversized artifact error")
	}
}
