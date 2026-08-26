package compile

import (
	"context"
	"strings"
	"testing"

	"aonohako/internal/gomodulepolicy"
	"aonohako/internal/model"
)

func TestCompileServiceRejectsInvalidGoModuleMode(t *testing.T) {
	resp := New().Run(context.Background(), &model.CompileRequest{
		Lang:         "GO",
		GoModuleMode: gomodulepolicy.Mode("vendor"),
		Sources:      []model.Source{{Name: "main.go", DataB64: b64String("package main\nfunc main() {}\n")}},
	})
	if resp.Status != model.CompileStatusInvalid || !strings.Contains(resp.Reason, "invalid go_module_mode") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestCompileServiceRejectsGoModuleModeForOtherLanguages(t *testing.T) {
	resp := New().Run(context.Background(), &model.CompileRequest{
		Lang:         "C11",
		GoModuleMode: gomodulepolicy.ModeInstalled,
		Sources:      []model.Source{{Name: "main.c", DataB64: b64String("int main(void) { return 0; }\n")}},
	})
	if resp.Status != model.CompileStatusInvalid || !strings.Contains(resp.Reason, "requires a Go compile request") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
