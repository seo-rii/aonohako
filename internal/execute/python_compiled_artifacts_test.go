package execute

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/compile"
	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/util"
)

func TestPythonBytecodeBundleCompilesAndRunsWithoutSources(t *testing.T) {
	withPythonBytecodeRunners(t, func(t *testing.T, executable string, run func(string, ...string) ([]byte, error)) {
		var baseline []model.Artifact
		for i, comment := range []string{"# first", "# a different comment"} {
			sources := map[string]string{
				"Main.py": "import sys, inspect, helper, pkg\n" +
					"from pathlib import Path\n" +
					"assert __file__.endswith('/Main.pyc')\n" +
					"assert sys.argv == [__file__]\n" +
					"assert sys.modules['__main__'].__file__ == __file__\n" +
					"assert not list(Path(__file__).parent.rglob('*.py'))\n" +
					"assert helper.value.__code__.co_firstlineno == 1\n" +
					"assert helper.value.__doc__ == 'kept docstring'\n" +
					"try:\n    inspect.getsource(helper.value)\n" +
					"except (OSError, TypeError):\n    pass\n" +
					"else:\n    raise AssertionError('source unexpectedly available')\n" +
					"print(helper.value() + pkg.VALUE)\n" +
					"print(Path(__file__).read_bytes()[4:16].hex())\n" + comment + "\n",
				"helper.py":       "def value():\n    'kept docstring'\n    return 40 " + comment + "\n",
				"pkg/__init__.py": "from .value import VALUE " + comment + "\n",
				"pkg/value.py":    "VALUE = 2 " + comment + "\n",
			}
			path, artifacts := prepareCompiledPythonBytecode(t, executable, sources)
			if i == 0 {
				baseline = artifacts
			} else if !reflect.DeepEqual(baseline, artifacts) {
				t.Fatal("comment variants produced different execution bundles")
			}
			output, err := run(path)
			if err != nil || string(output) != "42\n000000000000000000000000\n" {
				t.Fatalf("source-free execution: err=%v output=%q", err, output)
			}
		}
	})
}

func TestPythonBytecodeBundlePreservesTracebackLines(t *testing.T) {
	withPythonBytecodeRunners(t, func(t *testing.T, executable string, run func(string, ...string) ([]byte, error)) {
		path, _ := prepareCompiledPythonBytecode(t, executable, map[string]string{
			"Main.py": "def fail():\n    raise ValueError('line-preserved')\nfail()\n",
		})
		output, err := run(path)
		text := string(output)
		if err == nil || !strings.Contains(text, "line 2, in fail") || !strings.Contains(text, "ValueError: line-preserved") {
			t.Fatalf("unexpected traceback: err=%v output=%q", err, output)
		}
	})
}

func TestPythonBytecodeBundleDoesNotFallBackForInvalidBytecode(t *testing.T) {
	withPythonBytecodeRunners(t, func(t *testing.T, executable string, run func(string, ...string) ([]byte, error)) {
		for _, damage := range []string{"magic", "header", "body"} {
			t.Run(damage, func(t *testing.T) {
				path, _ := prepareCompiledPythonBytecode(t, executable, map[string]string{"Main.py": "print('must-not-run')\n"})
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				switch damage {
				case "magic":
					data[0] ^= 0xff
				case "header":
					data = data[:3]
				case "body":
					data = data[:16]
				}
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o444); err != nil {
					t.Fatal(err)
				}
				output, err := run(path)
				if err == nil || strings.Contains(string(output), "must-not-run") {
					t.Fatalf("invalid bytecode executed: err=%v output=%q", err, output)
				}
			})
		}
	})
}

func prepareCompiledPythonBytecode(t *testing.T, executable string, texts map[string]string) (string, []model.Artifact) {
	t.Helper()
	// Compile service binaries are resolved through the production allowlisted
	// PATH. Private toolchains remain covered by the compiler and launcher tests.
	compilerPath, err := util.ResolveCommandPath(filepath.Base(executable), util.BaseEnv())
	if err != nil {
		t.Skipf("compile-service integration requires a system interpreter: %v", err)
	}
	compilerInfo, err := os.Stat(compilerPath)
	if err != nil {
		t.Fatal(err)
	}
	runnerInfo, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(compilerInfo, runnerInfo) {
		t.Skip("compile-service and launcher interpreters differ")
	}
	lang := "PYTHON3"
	if strings.Contains(filepath.Base(executable), "pypy") {
		lang = "PYPY3"
	}
	sources := make([]model.Source, 0, len(texts))
	for name, text := range texts {
		sources = append(sources, model.Source{Name: name, DataB64: util.EncodeB64([]byte(text))})
	}
	response := compile.New().Run(context.Background(), &model.CompileRequest{Lang: lang, EntryPoint: "Main.py", Sources: sources, PythonArtifactMode: pythonpolicy.ArtifactModeBytecodeV1})
	if response.Status != model.CompileStatusOK {
		t.Fatalf("compile bytecode bundle: %+v", response)
	}
	workspace, err := prepareWorkspaceDirs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binaries := make([]model.Binary, 0, len(response.Artifacts))
	for _, artifact := range response.Artifacts {
		binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
	}
	path, _, err := materializeFiles(workspace, &model.RunRequest{Lang: lang, EntryPoint: "Main.pyc", Binaries: binaries})
	if err != nil {
		t.Fatal(err)
	}
	return path, response.Artifacts
}
