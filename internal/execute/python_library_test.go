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
		lang string
		mode pythonpolicy.LibraryMode
		want []string
	}{
		{
			name: "CPython omitted defaults to stdlib",
			lang: "python",
			want: []string{"python3", "-I", "-S", "-c", pythonStdlibRunner, path},
		},
		{
			name: "CPython stdlib",
			lang: "python",
			mode: pythonpolicy.LibraryModeStdlib,
			want: []string{"python3", "-I", "-S", "-c", pythonStdlibRunner, path},
		},
		{
			name: "CPython installed",
			lang: "python",
			mode: pythonpolicy.LibraryModeInstalled,
			want: []string{"python3", "-I", "-S", "-c", pythonInstalledRunner, path, pythonTrustedSitecustomizePath},
		},
		{
			name: "PyPy omitted defaults to stdlib",
			lang: "pypy",
			want: []string{"pypy3", "-I", "-S", "-c", pythonStdlibRunner, path},
		},
		{
			name: "PyPy stdlib",
			lang: "pypy",
			mode: pythonpolicy.LibraryModeStdlib,
			want: []string{"pypy3", "-I", "-S", "-c", pythonStdlibRunner, path},
		},
		{
			name: "PyPy installed",
			lang: "pypy",
			mode: pythonpolicy.LibraryModeInstalled,
			want: []string{"pypy3", "-I", "-S", "-c", pythonInstalledRunner, path, pythonTrustedSitecustomizePath},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &model.RunRequest{PythonLibraryMode: tc.mode}
			if got := buildCommand(path, tc.lang, req); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("buildCommand() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSandboxSupplementaryGroupsGrantInstalledPythonRuntimesOnly(t *testing.T) {
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
		{name: "PyPy stdlib", lang: "pypy", mode: pythonpolicy.LibraryModeStdlib, want: []uint32{65532}},
		{name: "PyPy omitted", lang: "pypy", want: []uint32{65532}},
		{name: "PyPy installed", lang: "pypy", mode: pythonpolicy.LibraryModeInstalled, want: []uint32{65532, pythonpolicy.ExternalLibraryGID}},
		{name: "PyPy alias", lang: "PYPY3", mode: pythonpolicy.LibraryModeInstalled, want: []uint32{65532, pythonpolicy.ExternalLibraryGID}},
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
			"import sys\n"+
			"import helper\n"+
			"assert sys.argv == [__file__]\n"+
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

func TestStdlibPythonCommandProvidesExitAndQuit(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	for _, name := range []string{"exit", "quit"} {
		t.Run(name, func(t *testing.T) {
			workDir := t.TempDir()
			mainPath := filepath.Join(workDir, "Main.py")
			body := "print('before')\n" + name + "(0)\nraise AssertionError('unreachable')\n"
			if err := os.WriteFile(mainPath, []byte(body), 0o644); err != nil {
				t.Fatalf("write Main.py: %v", err)
			}

			args := buildCommand(mainPath, "python", &model.RunRequest{PythonLibraryMode: pythonpolicy.LibraryModeStdlib})
			cmd := exec.Command(python, args[1:]...)
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s(0) failed: %v\n%s", name, err, out)
			}
			if string(out) != "before\n" {
				t.Fatalf("stdout = %q, want before", out)
			}
		})
	}
}

func TestStdlibPythonCommandProvidesOtherSiteBuiltins(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	workDir := t.TempDir()
	mainPath := filepath.Join(workDir, "Main.py")
	body := "import builtins\n" +
		"names = ('help', 'copyright', 'credits', 'license')\n" +
		"assert all(hasattr(builtins, name) for name in names)\n" +
		"assert callable(help)\n" +
		"print('ok')\n"
	if err := os.WriteFile(mainPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write Main.py: %v", err)
	}

	args := buildCommand(mainPath, "python", &model.RunRequest{PythonLibraryMode: pythonpolicy.LibraryModeStdlib})
	cmd := exec.Command(python, args[1:]...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("site builtins failed: %v\n%s", err, out)
	}
	if string(out) != "ok\n" {
		t.Fatalf("stdout = %q, want ok", out)
	}
}

func TestInstalledPythonRunnerLoadsOnlyTrustedSitecustomize(t *testing.T) {
	interpreters := []struct {
		name string
		path string
	}{
		{name: "CPython", path: "python3"},
		{name: "PyPy", path: "pypy3"},
	}
	for _, interpreter := range interpreters {
		t.Run(interpreter.name, func(t *testing.T) {
			executable, err := exec.LookPath(interpreter.path)
			if err != nil {
				t.Skipf("%s is unavailable", interpreter.path)
			}

			workDir := t.TempDir()
			externalDir := t.TempDir()
			userBase := t.TempDir()
			trustedDir := t.TempDir()
			trustedSitecustomizePath := filepath.Join(trustedDir, "sitecustomize.py")
			if err := os.WriteFile(trustedSitecustomizePath, []byte(
				"import builtins\n"+
					"SOURCE = 'trusted'\n"+
					"builtins.AONOHAKO_SITECUSTOMIZE = SOURCE\n",
			), 0o644); err != nil {
				t.Fatalf("write trusted sitecustomize: %v", err)
			}
			if err := os.WriteFile(filepath.Join(trustedDir, "trusted_probe.py"), []byte("VALUE = 'trusted-package-ok'\n"), 0o644); err != nil {
				t.Fatalf("write trusted package probe: %v", err)
			}

			maliciousSitecustomize := []byte(
				"import builtins\n" +
					"SOURCE = 'untrusted'\n" +
					"builtins.AONOHAKO_SITECUSTOMIZE = SOURCE\n" +
					"raise RuntimeError('untrusted sitecustomize executed')\n",
			)
			for _, dir := range []string{workDir, externalDir} {
				if err := os.WriteFile(filepath.Join(dir, "sitecustomize.py"), maliciousSitecustomize, 0o644); err != nil {
					t.Fatalf("write untrusted sitecustomize: %v", err)
				}
			}
			if err := os.WriteFile(filepath.Join(externalDir, "external_probe.py"), []byte("VALUE = 'leaked'\n"), 0o644); err != nil {
				t.Fatalf("write external probe: %v", err)
			}

			userSiteCommand := exec.Command(executable, "-S", "-c", "import site; print(site.getusersitepackages())")
			userSiteCommand.Env = append(os.Environ(), "PYTHONUSERBASE="+userBase)
			userSiteOutput, err := userSiteCommand.CombinedOutput()
			if err != nil {
				t.Fatalf("resolve user site: %v\n%s", err, userSiteOutput)
			}
			userSite := strings.TrimSpace(string(userSiteOutput))
			if err := os.MkdirAll(userSite, 0o755); err != nil {
				t.Fatalf("create user site: %v", err)
			}
			if err := os.WriteFile(filepath.Join(userSite, "sitecustomize.py"), maliciousSitecustomize, 0o644); err != nil {
				t.Fatalf("write user sitecustomize: %v", err)
			}
			if err := os.WriteFile(filepath.Join(userSite, "user_probe.py"), []byte("VALUE = 'leaked'\n"), 0o644); err != nil {
				t.Fatalf("write user probe: %v", err)
			}

			if err := os.WriteFile(filepath.Join(workDir, "helper.py"), []byte("VALUE = 'local-ok'\n"), 0o644); err != nil {
				t.Fatalf("write helper: %v", err)
			}
			mainPath := filepath.Join(workDir, "Main.py")
			if err := os.WriteFile(mainPath, []byte(
				"import builtins\n"+
					"import importlib.util\n"+
					"import sys\n"+
					"import helper\n"+
					"import sitecustomize\n"+
					"import trusted_probe\n"+
					"assert sys.argv == [__file__]\n"+
					"assert helper.VALUE == 'local-ok'\n"+
					"assert builtins.AONOHAKO_SITECUSTOMIZE == 'trusted'\n"+
					"assert sitecustomize.SOURCE == 'trusted'\n"+
					"assert trusted_probe.VALUE == 'trusted-package-ok'\n"+
					"assert importlib.util.find_spec('external_probe') is None\n"+
					"assert importlib.util.find_spec('user_probe') is None\n"+
					"print('ok')\n",
			), 0o644); err != nil {
				t.Fatalf("write Main.py: %v", err)
			}

			cmd := exec.Command(executable, "-I", "-S", "-c", pythonInstalledRunner, mainPath, trustedSitecustomizePath)
			cmd.Dir = workDir
			cmd.Env = append(os.Environ(), "PYTHONPATH="+externalDir, "PYTHONUSERBASE="+userBase)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("installed command failed: %v\n%s", err, out)
			}
			if string(out) != "ok\n" {
				t.Fatalf("stdout = %q, want ok", out)
			}
		})
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
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "Python or PyPy execution target") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
