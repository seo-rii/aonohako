package compile

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

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
