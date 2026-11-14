package compile

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/util"
)

func TestPythonBytecodeModeReturnsCanonicalArtifacts(t *testing.T) {
	withPythonCompilers(t, func(t *testing.T, interpreter string) {
		mtime := time.Unix(1_700_000_000, 0)
		var baseline []model.Artifact
		for i, comment := range []string{"# first", "# different comment"} {
			sources := []model.Source{
				{Name: "Main.py", DataB64: util.EncodeB64([]byte("import helper, pkg " + comment + "\nprint(helper.VALUE + pkg.VALUE)\n" + comment + "\n"))},
				{Name: "helper.py", DataB64: util.EncodeB64([]byte("VALUE = 40 " + comment + "\n"))},
				{Name: "pkg/__init__.py", DataB64: util.EncodeB64([]byte("from .value import VALUE " + comment + "\n"))},
				{Name: "pkg/value.py", DataB64: util.EncodeB64([]byte("VALUE = 2 " + comment + "\n"))},
			}
			request := &model.CompileRequest{Sources: sources, PythonArtifactMode: pythonpolicy.ArtifactModeBytecodeV1}
			artifacts := compilePythonArtifactsForTest(t, interpreter, t.TempDir(), sources, mtime.Add(time.Duration(i)*time.Hour), request)
			if len(artifacts) != len(sources) {
				t.Fatalf("got %d artifacts for %d modules", len(artifacts), len(sources))
			}
			original := compilePythonArtifactsForTest(t, interpreter, t.TempDir(), sources, mtime, &model.CompileRequest{Sources: sources})
			for _, artifact := range artifacts {
				if !strings.HasSuffix(artifact.Name, ".pyc") {
					t.Fatalf("source included in bytecode bundle: %s", artifact.Name)
				}
				data := pythonArtifactBytes(t, artifacts, artifact.Name)
				checked := pythonArtifactBytes(t, original, artifact.Name)
				if len(data) <= 16 || !bytes.Equal(data[4:16], make([]byte, 12)) {
					t.Fatalf("noncanonical header for %s", artifact.Name)
				}
				if !bytes.Equal(data[:4], checked[:4]) || !bytes.Equal(data[16:], checked[16:]) {
					t.Fatalf("normalization changed interpreter magic or code body of %s", artifact.Name)
				}
			}
			if i == 0 {
				baseline = artifacts
			} else if !reflect.DeepEqual(baseline, artifacts) {
				t.Fatal("comment-only changes or different workspace/mtime changed canonical bytecode")
			}
			executionDir := t.TempDir()
			for _, artifact := range artifacts {
				path := filepath.Join(executionDir, artifact.Name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, pythonArtifactBytes(t, artifacts, artifact.Name), 0o444); err != nil {
					t.Fatal(err)
				}
			}
			script := "import runpy, sys\nsys.path.insert(0, '.')\nrunpy.run_path('Main.pyc', run_name='__main__')\n"
			result := (pythonExecCommandRunner{}).Run(context.Background(), executionDir, interpreter, []string{"-I", "-S", "-c", script}, nil)
			if result.Status != model.CompileStatusOK || result.Stdout != "42\n" {
				t.Fatalf("source-free module imports: %+v", result)
			}
		}
	})
}

func TestPythonBytecodeModePreservesProgramAndLineDifferences(t *testing.T) {
	withPythonCompilers(t, func(t *testing.T, interpreter string) {
		var baseline []byte
		for i, text := range []string{"print(1)\n", "print(2)\n", "\nprint(1)\n"} {
			sources := []model.Source{{Name: "Main.py", DataB64: util.EncodeB64([]byte(text))}}
			request := &model.CompileRequest{Sources: sources, PythonArtifactMode: pythonpolicy.ArtifactModeBytecodeV1}
			artifacts := compilePythonArtifactsForTest(t, interpreter, t.TempDir(), sources, time.Unix(1_700_000_000, 0), request)
			data := pythonArtifactBytes(t, artifacts, "Main.pyc")
			if i == 0 {
				baseline = data
			} else if bytes.Equal(baseline, data) {
				t.Fatalf("program or source-line change %d was lost", i)
			}
		}
	})
}

func TestPythonBytecodeModeRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request model.CompileRequest
	}{
		{name: "unknown-mode", request: model.CompileRequest{Lang: "PYTHON3", PythonArtifactMode: "bytecode-v2", Sources: []model.Source{{Name: "Main.py", DataB64: b64String("print(1)\n")}}}},
		{name: "other-language", request: model.CompileRequest{Lang: "C", PythonArtifactMode: pythonpolicy.ArtifactModeBytecodeV1, Sources: []model.Source{{Name: "Main.c", DataB64: b64String("int main() {}\n")}}}},
		{name: "uploaded-bytecode", request: model.CompileRequest{Lang: "PYTHON3", PythonArtifactMode: pythonpolicy.ArtifactModeBytecodeV1, Sources: []model.Source{{Name: "Main.py", DataB64: b64String("print(1)\n")}, {Name: "Main.pyc", DataB64: b64String("uploaded bytecode")}}}},
		{name: "no-python-source", request: model.CompileRequest{Lang: "PYTHON3", PythonArtifactMode: pythonpolicy.ArtifactModeBytecodeV1, Sources: []model.Source{{Name: "notes.txt", DataB64: b64String("text")}}}},
		{name: "uppercase-extension", request: model.CompileRequest{Lang: "PYTHON3", PythonArtifactMode: pythonpolicy.ArtifactModeBytecodeV1, Sources: []model.Source{{Name: "Main.PY", DataB64: b64String("print(1)\n")}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := New().Run(context.Background(), &tc.request)
			if response.Status != model.CompileStatusInvalid || !strings.Contains(response.Reason, "python_artifact_mode") {
				t.Fatalf("got %+v", response)
			}
		})
	}
}

func TestPythonBytecodeModeRejectsMissingOrUnexpectedCompilerOutputs(t *testing.T) {
	for _, kind := range []string{"missing", "unexpected", "invalid-header"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			runner := &recordingCommandRunner{result: CommandResult{Status: model.CompileStatusOK}, hook: func(dir, _ string, args, _ []string) {
				if !strings.Contains(strings.Join(args, " "), " -f ") {
					t.Fatal("bytecode mode must force compilation")
				}
				if kind == "missing" {
					return
				}
				name := "Main.pyc"
				if kind == "unexpected" {
					name = "injected.pyc"
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte("invalid"), 0o644); err != nil {
					t.Fatal(err)
				}
			}}
			response := (pythonLikeCompiler{interpreter: "python3"}).Compile(context.Background(), CompileJob{WorkDir: dir, Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.py"}}, PythonArtifactMode: pythonpolicy.ArtifactModeBytecodeV1}, Runner: runner})
			if response.Status != model.CompileStatusInternal || len(response.Artifacts) != 0 {
				t.Fatalf("unsafe compiler output accepted: %+v", response)
			}
		})
	}
}

func TestPythonLikeCompilerArtifactsAreReproducible(t *testing.T) {
	withPythonCompilers(t, func(t *testing.T, interpreter string) {
		sources := []model.Source{
			{Name: "Main.py", DataB64: util.EncodeB64([]byte("def values():\n    return {'alpha', 'beta', 'gamma'}, (1, 2, '한글')\nprint(sorted(values()[0]))\n"))},
			{Name: "pkg/helper.py", DataB64: util.EncodeB64([]byte("def outer():\n    def inner():\n        return ('nested', 42)\n    return inner\n"))},
		}
		request := &model.CompileRequest{Sources: sources}
		mtime := time.Unix(1_700_000_000, 0)
		first := compilePythonArtifactsForTest(t, interpreter, t.TempDir(), sources, mtime, request)
		second := compilePythonArtifactsForTest(t, interpreter, t.TempDir(), sources, mtime.Add(time.Hour), request)
		if len(first) != len(sources)*2 || len(second) != len(sources)*2 {
			t.Fatalf("artifact counts = %d, %d; want source and bytecode for each file", len(first), len(second))
		}
		if !reflect.DeepEqual(first, second) {
			t.Error("identical sources produced different artifacts in fresh workspaces with different mtimes")
		}
		for _, source := range sources {
			original, err := util.DecodeB64(source.DataB64)
			if err != nil {
				t.Fatal(err)
			}
			if got := pythonArtifactBytes(t, first, source.Name); !bytes.Equal(got, original) {
				t.Errorf("source artifact %s changed", source.Name)
			}
			for _, artifacts := range [][]model.Artifact{first, second} {
				pyc := pythonArtifactBytes(t, artifacts, source.Name+"c")
				if len(pyc) < 16 {
					t.Fatalf("%s bytecode is missing its PEP 552 header", source.Name)
				}
				if flags := binary.LittleEndian.Uint32(pyc[4:8]); flags != 3 {
					t.Errorf("%s bytecode flags = %d, want checked-hash flags 3", source.Name, flags)
				}
			}
		}
	})
}

func TestPythonLikeCompilerRebuildsChangedSourceAtSameMtime(t *testing.T) {
	withPythonCompilers(t, func(t *testing.T, interpreter string) {
		dir := t.TempDir()
		mtime := time.Unix(1_700_000_000, 0)
		var before, after []model.Artifact
		for i, source := range []string{"print('old')\n", "print('new')\n"} {
			sources := []model.Source{{Name: "Main.py", DataB64: util.EncodeB64([]byte(source))}}
			artifacts := compilePythonArtifactsForTest(t, interpreter, dir, sources, mtime, &model.CompileRequest{Sources: sources})
			if i == 0 {
				before = artifacts
			} else {
				after = artifacts
			}
		}
		beforePyc := pythonArtifactBytes(t, before, "Main.pyc")
		afterPyc := pythonArtifactBytes(t, after, "Main.pyc")
		if bytes.Equal(beforePyc, afterPyc) {
			t.Error("changed source with the same size and mtime reused stale bytecode")
		}
		if len(beforePyc) < 16 || len(afterPyc) < 16 {
			t.Fatal("bytecode is missing its PEP 552 header")
		}
		if bytes.Equal(beforePyc[8:16], afterPyc[8:16]) {
			t.Error("changed source did not change the bytecode header's source hash")
		}
		if err := os.Remove(filepath.Join(dir, "Main.py")); err != nil {
			t.Fatal(err)
		}
		// The execution service loads sibling bytecode with this loader. Keep
		// that path working even when the header switches to checked hashes.
		script := "import importlib.machinery\ncode = importlib.machinery.SourcelessFileLoader('__main__', 'Main.pyc').get_code('__main__')\nexec(code, {'__name__': '__main__'})\n"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result := (pythonExecCommandRunner{}).Run(ctx, dir, interpreter, []string{"-I", "-S", "-c", script}, nil)
		if result.Status != model.CompileStatusOK || result.Stdout != "new\n" {
			t.Fatalf("load changed bytecode: status=%s stdout=%q stderr=%q reason=%s", result.Status, result.Stdout, result.Stderr, result.Reason)
		}
	})
}

func TestPythonLikeCompilerFallbackCompilesWorkspace(t *testing.T) {
	withPythonCompilers(t, func(t *testing.T, interpreter string) {
		sources := []model.Source{
			{Name: "Main.py", DataB64: util.EncodeB64([]byte("print('main')\n"))},
			{Name: "pkg/helper.py", DataB64: util.EncodeB64([]byte("VALUE = 'nested'\n"))},
			{Name: "notes.txt", DataB64: util.EncodeB64([]byte("not a Python source\n"))},
		}
		for _, tc := range []struct {
			name    string
			request *model.CompileRequest
		}{
			{name: "nil-request"},
			{name: "no-python-source", request: &model.CompileRequest{Sources: sources[2:]}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				artifacts := compilePythonArtifactsForTest(t, interpreter, t.TempDir(), sources, time.Unix(1_700_000_000, 0), tc.request)
				if len(artifacts) != 4 {
					t.Fatalf("artifact count = %d, want two sources and two bytecode files", len(artifacts))
				}
				for _, name := range []string{"Main.pyc", "pkg/helper.pyc"} {
					pyc := pythonArtifactBytes(t, artifacts, name)
					if len(pyc) < 16 || binary.LittleEndian.Uint32(pyc[4:8]) != 3 {
						t.Errorf("%s does not have a checked-hash bytecode header", name)
					}
				}
			})
		}
	})
}

func withPythonCompilers(t *testing.T, test func(*testing.T, string)) {
	t.Helper()
	for _, interpreter := range []string{"python3", "pypy3"} {
		t.Run(interpreter, func(t *testing.T) {
			executable, err := exec.LookPath(interpreter)
			if err != nil {
				t.Skipf("%s is unavailable", interpreter)
			}
			test(t, executable)
		})
	}
}

func compilePythonArtifactsForTest(t *testing.T, interpreter, dir string, sources []model.Source, mtime time.Time, request *model.CompileRequest) []model.Artifact {
	t.Helper()
	if err := materializeSources(dir, sources); err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if err := os.Chtimes(filepath.Join(dir, source.Name), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response := (pythonLikeCompiler{interpreter: interpreter}).Compile(ctx, CompileJob{
		WorkDir: dir,
		Request: request,
		Runner:  pythonExecCommandRunner{},
	})
	if response.Status != model.CompileStatusOK {
		t.Fatalf("compile Python: status=%s stdout=%q stderr=%q reason=%s", response.Status, response.Stdout, response.Stderr, response.Reason)
	}
	return response.Artifacts
}

func pythonArtifactBytes(t *testing.T, artifacts []model.Artifact, name string) []byte {
	t.Helper()
	for _, artifact := range artifacts {
		if artifact.Name == name {
			data, err := util.DecodeB64(artifact.DataB64)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
	}
	t.Fatalf("missing artifact %s", name)
	return nil
}

type pythonExecCommandRunner struct{}

func (pythonExecCommandRunner) Run(ctx context.Context, workDir, bin string, args, env []string) CommandResult {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir
	// Match production's allowlisted environment, especially the absence of
	// SOURCE_DATE_EPOCH, so a developer's environment cannot mask the defect.
	cmd.Env = append(util.BaseEnv(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := CommandResult{Status: model.CompileStatusOK, Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		result.Status = model.CompileStatusCompileError
		result.Reason = err.Error()
	}
	return result
}
