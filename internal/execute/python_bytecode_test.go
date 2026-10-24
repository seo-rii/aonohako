package execute

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonRunnerUsesPrecompiledBytecodeWithSourceIdentity(t *testing.T) {
	withPythonBytecodeRunners(t, func(t *testing.T, executable string, run func(string, ...string) ([]byte, error)) {
		dir := t.TempDir()
		path := filepath.Join(dir, "Main.py")
		body := "import sys, __main__, helper\n" +
			"assert __name__ == '__main__'\n" +
			"assert __main__.__dict__ is globals()\n" +
			"assert __main__.__file__ == __file__\n" +
			"assert sys.argv == [__file__, 'argument']\n" +
			"assert __file__.endswith('/Main.py')\n" +
			"assert 'runpy' not in globals() and 'path' not in globals()\n" +
			"assert helper.VALUE == 'local-import'\n" +
			"print('compiled-marker')\n"
		writePythonBytecode(t, executable, path, body)
		if err := os.WriteFile(filepath.Join(dir, "helper.py"), []byte("VALUE = 'local-import'\n"), 0o644); err != nil {
			t.Fatalf("write sibling module: %v", err)
		}
		// A source-only runner would execute this replacement instead of the
		// artifact compiled above, so this proves the bytecode branch is used.
		replacement := []byte("raise RuntimeError('source-must-not-run')\n")
		if err := os.WriteFile(path, replacement, 0o444); err != nil {
			t.Fatalf("replace source: %v", err)
		}
		out, err := run(path, "argument")
		if err != nil || string(out) != "compiled-marker\n" {
			t.Fatalf("precompiled run: err=%v output=%q", err, out)
		}
		preserved, err := os.ReadFile(path)
		if err != nil || string(preserved) != string(replacement) {
			t.Fatalf("source was not preserved: err=%v contents=%q", err, preserved)
		}
	})
}

func TestPythonRunnerFallsBackForMissingOrInvalidBytecode(t *testing.T) {
	withPythonBytecodeRunners(t, func(t *testing.T, executable string, run func(string, ...string) ([]byte, error)) {
		for _, failure := range []string{"missing", "incompatible-magic", "truncated-header", "truncated-code"} {
			t.Run(failure, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "Main.py")
				writePythonBytecode(t, executable, path, "print('fallback-marker')\n")
				bytecodePath := path + "c"
				if failure == "missing" {
					if err := os.Remove(bytecodePath); err != nil {
						t.Fatalf("remove bytecode: %v", err)
					}
				} else {
					bytecode, err := os.ReadFile(bytecodePath)
					if err != nil {
						t.Fatalf("read bytecode: %v", err)
					}
					switch failure {
					case "incompatible-magic":
						bytecode[0] ^= 0xff
					case "truncated-header":
						bytecode = bytecode[:3]
					case "truncated-code":
						bytecode = bytecode[:len(bytecode)-1]
					}
					if err := os.WriteFile(bytecodePath, bytecode, 0o644); err != nil {
						t.Fatalf("damage bytecode: %v", err)
					}
				}
				out, err := run(path)
				if err != nil || string(out) != "fallback-marker\n" {
					t.Fatalf("source fallback: err=%v output=%q", err, out)
				}
			})
		}
	})
}

func TestPythonRunnerBytecodePreservesTracebackAndDoesNotRetrySource(t *testing.T) {
	withPythonBytecodeRunners(t, func(t *testing.T, executable string, run func(string, ...string) ([]byte, error)) {
		path := filepath.Join(t.TempDir(), "Main.py")
		body := "print('execution-started', flush=True)\n" +
			"def fail():\n" +
			"    raise ImportError('contestant-error')\n" +
			"import os, inspect\n" +
			fmt.Sprintf("os.chdir(%q)\n", t.TempDir()) +
			"assert fail.__code__.co_filename == __file__\n" +
			"assert inspect.getsource(fail).strip() == \"def fail():\\n    raise ImportError('contestant-error')\"\n" +
			"fail()\n"
		writePythonBytecode(t, executable, path, body)
		out, err := run(path)
		if err == nil {
			t.Fatalf("expected contestant failure, got output %q", out)
		}
		text := string(out)
		if strings.Count(text, "execution-started") != 1 {
			t.Fatalf("submission ran more than once: %s", text)
		}
		for _, expected := range []string{
			fmt.Sprintf("File %q, line 3, in fail", path),
			"raise ImportError('contestant-error')",
			"ImportError: contestant-error",
		} {
			if !strings.Contains(text, expected) {
				t.Fatalf("traceback missing %q: %s", expected, text)
			}
		}
	})
}

func TestPythonRunnerAcceptsExplicitBytecodeEntry(t *testing.T) {
	withPythonBytecodeRunners(t, func(t *testing.T, executable string, run func(string, ...string) ([]byte, error)) {
		path := filepath.Join(t.TempDir(), "Main.py")
		body := "import sys\n" +
			"assert __file__.endswith('/Main.pyc')\n" +
			"assert sys.argv == [__file__]\n" +
			"print('explicit-bytecode')\n"
		writePythonBytecode(t, executable, path, body)
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove source: %v", err)
		}
		out, err := run(path + "c")
		if err != nil || string(out) != "explicit-bytecode\n" {
			t.Fatalf("explicit bytecode entry: err=%v output=%q", err, out)
		}
	})
}

func writePythonBytecode(t *testing.T, executable, path, source string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	// Match the compile service: compileall records a relative co_filename,
	// whereas execution materializes these artifacts in a different directory.
	cmd := exec.Command(executable, "-I", "-S", "-m", "compileall", "-q", "-b", "--invalidation-mode", "checked-hash", filepath.Base(path))
	cmd.Dir = filepath.Dir(path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile bytecode: %v\n%s", err, out)
	}
}

func withPythonBytecodeRunners(t *testing.T, test func(*testing.T, string, func(string, ...string) ([]byte, error))) {
	t.Helper()
	for _, interpreter := range []string{"python3", "pypy3"} {
		t.Run(interpreter, func(t *testing.T) {
			executable, err := exec.LookPath(interpreter)
			if err != nil {
				t.Skipf("%s is unavailable", interpreter)
			}
			for _, mode := range []string{"stdlib", "installed"} {
				t.Run(mode, func(t *testing.T) {
					script := pythonStdlibRunner
					var trustedPath string
					if mode == "installed" {
						script = pythonInstalledRunner
						trustedPath = filepath.Join(t.TempDir(), "sitecustomize.py")
						if err := os.WriteFile(trustedPath, []byte("SOURCE = 'trusted'\n"), 0o644); err != nil {
							t.Fatalf("write trusted sitecustomize: %v", err)
						}
					}
					test(t, executable, func(path string, submissionArgs ...string) ([]byte, error) {
						args := []string{"-I", "-S", "-c", script, path}
						if trustedPath != "" {
							args = append(args, trustedPath)
						}
						args = append(args, submissionArgs...)
						cmd := exec.Command(executable, args...)
						cmd.Dir = filepath.Dir(path)
						return cmd.CombinedOutput()
					})
				})
			}
		})
	}
}
