package execute

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
)

func TestBuildCommandSelectsPythonLibraryMode(t *testing.T) {
	path := "/work/box/Main.py"
	tests := []struct {
		name string
		mode pythonpolicy.LibraryMode
		want []string
	}{
		{
			name: "omitted defaults to stdlib",
			want: []string{"python3", "-E", "-s", "-S", path},
		},
		{
			name: "stdlib",
			mode: pythonpolicy.LibraryModeStdlib,
			want: []string{"python3", "-E", "-s", "-S", path},
		},
		{
			name: "installed",
			mode: pythonpolicy.LibraryModeInstalled,
			want: []string{"python3", path},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &model.RunRequest{PythonLibraryMode: tc.mode}
			if got := buildCommand(path, "python", req); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("buildCommand() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSandboxSupplementaryGroupsGrantInstalledPythonOnly(t *testing.T) {
	identity := sandboxIdentity{uid: 65532, gid: 65532}
	tests := []struct {
		name string
		lang string
		mode pythonpolicy.LibraryMode
		want []uint32
	}{
		{name: "stdlib", lang: "python", mode: pythonpolicy.LibraryModeStdlib, want: []uint32{65532}},
		{name: "omitted", lang: "python", want: []uint32{65532}},
		{name: "installed", lang: "python", mode: pythonpolicy.LibraryModeInstalled, want: []uint32{65532, pythonpolicy.ExternalLibraryGID}},
		{name: "python alias", lang: "PYTHON3", mode: pythonpolicy.LibraryModeInstalled, want: []uint32{65532, pythonpolicy.ExternalLibraryGID}},
		{name: "non python", lang: "binary", mode: pythonpolicy.LibraryModeInstalled, want: []uint32{65532}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sandboxSupplementaryGroups(identity, tc.lang, tc.mode); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("groups = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStdlibPythonCommandKeepsSubmissionImportsAndIgnoresPythonPath(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	workDir := t.TempDir()
	externalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "helper.py"), []byte("VALUE = 'local-ok'\n"), 0o644); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalDir, "external_probe.py"), []byte("VALUE = 'leaked'\n"), 0o644); err != nil {
		t.Fatalf("write external probe: %v", err)
	}
	mainPath := filepath.Join(workDir, "Main.py")
	if err := os.WriteFile(mainPath, []byte(
		"import importlib.util\n"+
			"import helper\n"+
			"assert helper.VALUE == 'local-ok'\n"+
			"assert importlib.util.find_spec('external_probe') is None\n"+
			"print('ok')\n",
	), 0o644); err != nil {
		t.Fatalf("write Main.py: %v", err)
	}

	args := buildCommand(mainPath, "python", &model.RunRequest{PythonLibraryMode: pythonpolicy.LibraryModeStdlib})
	cmd := exec.Command(python, args[1:]...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "PYTHONPATH="+externalDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stdlib command failed: %v\n%s", err, out)
	}
	if string(out) != "ok\n" {
		t.Fatalf("stdout = %q, want ok", out)
	}
}

func TestDerivedPythonRunsPreserveLibraryMode(t *testing.T) {
	req := &model.RunRequest{
		PythonLibraryMode: pythonpolicy.LibraryModeInstalled,
		Limits:            model.Limits{TimeMs: 1000, MemoryMB: 64},
		Interactor: &model.InteractorSpec{
			Lang:     "python",
			Binaries: []model.Binary{{Name: "judge.py", DataB64: "eA=="}},
		},
	}
	if got := interactorRunRequest(req).PythonLibraryMode; got != pythonpolicy.LibraryModeInstalled {
		t.Fatalf("interactor mode = %q, want installed", got)
	}
}

func TestServiceRejectsPythonModeWithoutPythonTarget(t *testing.T) {
	resp := New().Run(context.Background(), &model.RunRequest{
		Lang:              "binary",
		PythonLibraryMode: pythonpolicy.LibraryModeInstalled,
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "Python execution target") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
