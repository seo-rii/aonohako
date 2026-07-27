package runvalidation

import (
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
)

func TestValidatePythonLibraryMode(t *testing.T) {
	valid := &model.RunRequest{
		Lang:              "python",
		PythonLibraryMode: pythonpolicy.LibraryModeStdlib,
		Binaries:          []model.Binary{{Name: "Main.py", DataB64: "cHJpbnQoMSk="}},
		Limits:            model.Limits{TimeMs: 1000, MemoryMB: 64},
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("stdlib Python request should validate: %v", err)
	}

	invalid := *valid
	invalid.PythonLibraryMode = "all"
	if err := Validate(&invalid); err == nil || !strings.Contains(err.Error(), "python_library_mode") {
		t.Fatalf("invalid mode error = %v", err)
	}

	nonPython := *valid
	nonPython.Lang = "binary"
	nonPython.Binaries = []model.Binary{{Name: "Main", DataB64: "eA==", Mode: "exec"}}
	if err := Validate(&nonPython); err == nil || !strings.Contains(err.Error(), "requires a Python") {
		t.Fatalf("non-Python mode error = %v", err)
	}
}

func TestUsesPythonFindsEveryExecutionShape(t *testing.T) {
	tests := []struct {
		name string
		req  *model.RunRequest
	}{
		{name: "legacy", req: &model.RunRequest{Lang: "PYTHON3"}},
		{name: "step", req: &model.RunRequest{Programs: []model.RunProgram{{Lang: "python"}}}},
		{name: "interactor", req: &model.RunRequest{Interactor: &model.InteractorSpec{Lang: "python"}}},
		{name: "spj", req: &model.RunRequest{SPJ: &model.SPJSpec{Lang: "python"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !UsesPython(tc.req) {
				t.Fatalf("UsesPython() = false")
			}
		})
	}
	if UsesPython(&model.RunRequest{Lang: "binary"}) {
		t.Fatalf("binary request unexpectedly uses Python")
	}
}
