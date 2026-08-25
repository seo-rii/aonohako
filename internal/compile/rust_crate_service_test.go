package compile

import (
	"context"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/rustpolicy"
)

func TestCompileServiceRejectsInvalidRustCrateMode(t *testing.T) {
	resp := New().Run(context.Background(), &model.CompileRequest{
		Lang:          "RUST2021",
		RustCrateMode: rustpolicy.CrateMode("vendor"),
		Sources:       []model.Source{{Name: "main.rs", DataB64: b64String("fn main() {}\n")}},
	})
	if resp.Status != model.CompileStatusInvalid || !strings.Contains(resp.Reason, "invalid rust_crate_mode") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestCompileServiceRejectsRustCrateModeForOtherLanguages(t *testing.T) {
	resp := New().Run(context.Background(), &model.CompileRequest{
		Lang:          "C11",
		RustCrateMode: rustpolicy.CrateModeInstalled,
		Sources:       []model.Source{{Name: "main.c", DataB64: b64String("int main(void) { return 0; }\n")}},
	})
	if resp.Status != model.CompileStatusInvalid || !strings.Contains(resp.Reason, "requires a Rust compile language") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
