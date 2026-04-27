package compile

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"golang.org/x/sys/unix"
)

func b64String(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func b64Bytes(v []byte) string {
	return base64.StdEncoding.EncodeToString(v)
}

func TestBuildEmbeddedRunnerPassesCgroupParent(t *testing.T) {
	runner, err := Build(config.Config{
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetSelfHosted,
				ExecutionTransport: platform.ExecutionTransportEmbedded,
				SandboxBackend:     platform.SandboxBackendHelper,
			},
			Cgroup: config.CgroupConfig{ParentDir: "/sys/fs/cgroup/aonohako"},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	service, ok := runner.(*Service)
	if !ok {
		t.Fatalf("Build() returned %T, want *Service", runner)
	}
	if service.cgroupParentDir != "/sys/fs/cgroup/aonohako" {
		t.Fatalf("cgroupParentDir = %q", service.cgroupParentDir)
	}
}

func sandboxWritableTempDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp(os.TempDir(), "aonohako-compile-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chown(dir, 65532, 65532); err != nil {
		t.Fatalf("Chown(%q): %v", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod(%q): %v", dir, err)
	}
	return dir
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

func TestRunRejectsMissingCompileEntrypoint(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang:       "C11",
		EntryPoint: "src/missing.c",
		Sources: []model.Source{{
			Name:    "src/main.c",
			DataB64: b64String("int main(void) { return 0; }\n"),
		}},
	})
	if resp.Status != model.CompileStatusInvalid {
		t.Fatalf("status=%q want=%q response=%+v", resp.Status, model.CompileStatusInvalid, resp)
	}
	if !strings.Contains(resp.Reason, "entry_point") {
		t.Fatalf("expected entry_point validation reason, got %+v", resp)
	}
}

func TestCompileNativeBuildsMultipleCFiles(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not available")
	}

	workDir := sandboxWritableTempDir(t)
	sources := []model.Source{
		{
			Name:    "src/main.c",
			DataB64: b64String("#include \"add.h\"\n#include <stdio.h>\nint main(void) { printf(\"%d\\n\", add(2, 3)); return 0; }\n"),
		},
		{
			Name:    "src/add.c",
			DataB64: b64String("#include \"add.h\"\nint add(int a, int b) { return a + b; }\n"),
		},
		{
			Name:    "src/add.h",
			DataB64: b64String("int add(int a, int b);\n"),
		},
	}
	if err := materializeSources(workDir, sources); err != nil {
		t.Fatalf("materializeSources: %v", err)
	}

	resp := compileNative(context.Background(), workDir, "Main", gatherByExt(sources, ".c", ".h"), "gcc", []string{"-O2"})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("expected multi-file C compile to succeed, got status=%q reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("unexpected artifacts: %+v", resp.Artifacts)
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

func TestRunRejectsTooManySources(t *testing.T) {
	svc := New()
	sources := make([]model.Source, 0, maxSourceFiles+1)
	for i := 0; i < maxSourceFiles+1; i++ {
		sources = append(sources, model.Source{
			Name:    "Main" + strconv.Itoa(i) + ".uhm",
			DataB64: b64String("text"),
		})
	}
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang:    "UHMLANG",
		Sources: sources,
	})
	if resp.Status != model.CompileStatusInvalid {
		t.Fatalf("status=%q want=%q", resp.Status, model.CompileStatusInvalid)
	}
	if !strings.Contains(resp.Reason, "too many sources") {
		t.Fatalf("expected too many sources reason, got %+v", resp)
	}
}

func TestResolveProfileSupportsNewLanguages(t *testing.T) {
	tests := map[string]struct {
		compileKind string
		runLang     string
	}{
		"asm":        {compileKind: "asm", runLang: "binary"},
		"aheui":      {compileKind: "none", runLang: "aheui"},
		"nasm":       {compileKind: "nasm", runLang: "binary"},
		"pascal":     {compileKind: "pascal", runLang: "binary"},
		"nim":        {compileKind: "nim", runLang: "binary"},
		"clojure":    {compileKind: "clojure", runLang: "clojure"},
		"racket":     {compileKind: "racket", runLang: "racket"},
		"ada":        {compileKind: "ada", runLang: "binary"},
		"dart":       {compileKind: "dart", runLang: "binary"},
		"haskell":    {compileKind: "haskell", runLang: "binary"},
		"swift":      {compileKind: "swift", runLang: "binary"},
		"sqlite":     {compileKind: "sqlite", runLang: "sqlite"},
		"julia":      {compileKind: "julia", runLang: "julia"},
		"erlang":     {compileKind: "erlang", runLang: "erlang"},
		"prolog":     {compileKind: "prolog", runLang: "prolog"},
		"r":          {compileKind: "r", runLang: "r"},
		"groovy":     {compileKind: "groovy", runLang: "groovy"},
		"fortan":     {compileKind: "fortran", runLang: "binary"},
		"d":          {compileKind: "d", runLang: "binary"},
		"coq":        {compileKind: "coq", runLang: "coq"},
		"zig":        {compileKind: "zig", runLang: "binary"},
		"lisp":       {compileKind: "lisp", runLang: "lisp"},
		"scala":      {compileKind: "scala", runLang: "scala"},
		"fsharp":     {compileKind: "fsharp", runLang: "fsharp"},
		"whitespace": {compileKind: "whitespace", runLang: "whitespace"},
		"bf":         {compileKind: "brainfuck", runLang: "brainfuck"},
		"wasm":       {compileKind: "wasm", runLang: "wasm"},
	}

	for input, want := range tests {
		profile, ok := resolveProfile(input)
		if !ok {
			t.Fatalf("resolveProfile(%q) reported unsupported language", input)
		}
		if profile.CompileKind != want.compileKind {
			t.Fatalf("resolveProfile(%q) compile kind = %q, want %q", input, profile.CompileKind, want.compileKind)
		}
		if profile.RunLang != want.runLang {
			t.Fatalf("resolveProfile(%q) run lang = %q, want %q", input, profile.RunLang, want.runLang)
		}
	}
}

func TestResolveProfileAcceptsLanguageAliases(t *testing.T) {
	tests := map[string]string{
		"assembly":   "asm",
		"gas":        "asm",
		"freepascal": "pascal",
		"fpc":        "pascal",
		"nasm64":     "nasm",
		"scheme":     "racket",
	}

	for input, wantCompileKind := range tests {
		profile, ok := resolveProfile(input)
		if !ok {
			t.Fatalf("resolveProfile(%q) reported unsupported language", input)
		}
		if profile.CompileKind != wantCompileKind {
			t.Fatalf("resolveProfile(%q) compile kind = %q, want %q", input, profile.CompileKind, wantCompileKind)
		}
	}
}

func TestRunRejectsInvalidWhitespaceProgram(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "WHITESPACE",
		Sources: []model.Source{{
			Name:    "Main.ws",
			DataB64: b64String("not whitespace"),
		}},
	})
	if resp.Status != model.CompileStatusCompileError {
		t.Fatalf("status=%q want=%q", resp.Status, model.CompileStatusCompileError)
	}
}

func TestRunRejectsInvalidBrainfuckProgram(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "BF",
		Sources: []model.Source{{
			Name:    "Main.bf",
			DataB64: b64String("++[>++<-"),
		}},
	})
	if resp.Status != model.CompileStatusCompileError {
		t.Fatalf("status=%q want=%q", resp.Status, model.CompileStatusCompileError)
	}
}

func TestRunPythonCompileSucceedsWithRootBackedSandboxWorkspace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop compile helper to sandbox user")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workRoot := t.TempDir()
	if err := os.Chmod(workRoot, 0o755); err != nil {
		t.Fatalf("Chmod(%q): %v", workRoot, err)
	}
	t.Setenv("AONOHAKO_EXECUTION_MODE", "local-root")
	t.Setenv("AONOHAKO_WORK_ROOT", workRoot)

	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: b64String("print('ok')\n"),
		}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("expected root-backed python compile to succeed, got status=%q reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
	}
	if len(resp.Artifacts) == 0 {
		t.Fatalf("expected compiled python artifacts")
	}
}

func TestRunPythonCompileDoesNotExecuteSitecustomize(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	originalMain := "print('ok')\n"
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{
			{
				Name:    "Main.py",
				DataB64: b64String(originalMain),
			},
			{
				Name:    "sitecustomize.py",
				DataB64: b64String("from pathlib import Path\nPath('Main.py').write_text(\"print(\\\"pwned\\\")\\n\")\n"),
			},
		},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
	}

	artifacts := map[string]string{}
	for _, artifact := range resp.Artifacts {
		raw, err := base64.StdEncoding.DecodeString(artifact.DataB64)
		if err != nil {
			t.Fatalf("decode artifact %q: %v", artifact.Name, err)
		}
		artifacts[artifact.Name] = string(raw)
	}
	if got := artifacts["Main.py"]; got != originalMain {
		t.Fatalf("expected Main.py artifact to stay unchanged, got %q", got)
	}
	if got := artifacts["sitecustomize.py"]; got == "" {
		t.Fatalf("expected sitecustomize.py artifact to be preserved")
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

func TestCollectArtifactsRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside.bin: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "artifact.bin")); err != nil {
		t.Fatalf("symlink artifact.bin: %v", err)
	}
	if _, err := collectArtifacts(root, func(string) bool { return true }, ""); err == nil {
		t.Fatalf("expected symlink artifact error")
	}
}

func TestCollectArtifactsRejectsHardlink(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.bin")
	if err := os.WriteFile(original, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write original.bin: %v", err)
	}
	if err := os.Link(original, filepath.Join(root, "artifact.bin")); err != nil {
		t.Fatalf("link artifact.bin: %v", err)
	}
	if _, err := collectArtifacts(root, func(string) bool { return true }, ""); err == nil {
		t.Fatalf("expected hardlink artifact error")
	}
}

func TestCollectArtifactsForCoqKeepsSourceArtifactForExecution(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"Main.v":    "Theorem same_folder_ok : 1 = 1.\nProof. reflexivity. Qed.\n",
		"Main.vo":   "vo",
		"Main.glob": "glob",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	artifacts, err := collectArtifacts(root, func(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".v") }, "")
	if err != nil {
		t.Fatalf("collectArtifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected only coq source artifact, got %+v", artifacts)
	}
	if artifacts[0].Name != "Main.v" {
		t.Fatalf("expected coq compile artifact Main.v, got %+v", artifacts)
	}
}

func TestRunLispCompileDoesNotReturnRuntimeCreatedFiles(t *testing.T) {
	if _, err := exec.LookPath("sbcl"); err != nil {
		t.Skip("sbcl not available")
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "LISP",
		Sources: []model.Source{{
			Name: "Main.lisp",
			DataB64: b64String(`(with-open-file (out "same-folder.txt"
                     :direction :output
                     :if-exists :supersede
                     :if-does-not-exist :create)
  (write-line "ok" out))
(format t "ok~%")
`),
		}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
	}
	foundSource := false
	for _, artifact := range resp.Artifacts {
		if artifact.Name == "same-folder.txt" {
			t.Fatalf("lisp compile must not execute top-level writes or return runtime-created files: %+v", resp.Artifacts)
		}
		if artifact.Name == "Main.lisp" {
			foundSource = true
		}
	}
	if !foundSource {
		t.Fatalf("lisp compile should return source artifact, got %+v", resp.Artifacts)
	}
}

func TestReadSingleArtifactRejectsSymlinkParents(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "payload.bin"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write payload.bin: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "subdir")); err != nil {
		t.Fatalf("symlink subdir: %v", err)
	}
	if _, err := readSingleArtifact(root, filepath.Join("subdir", "payload.bin"), "payload.bin", ""); err == nil {
		t.Fatalf("expected symlink parent rejection")
	}
}

func TestRunCommandKillsBackgroundChildren(t *testing.T) {
	workDir := sandboxWritableTempDir(t)
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		workDir,
		"/bin/sh",
		[]string{"-c", "sleep 30 & echo $! > bg.pid"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("runCommand status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
	rawPID, err := os.ReadFile(filepath.Join(workDir, "bg.pid"))
	if err != nil {
		t.Fatalf("read bg.pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatalf("parse bg.pid: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		if err != nil {
			t.Fatalf("kill(%d, 0): %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("background child %d is still alive", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunSandboxedCommandCapsCapturedOutput(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workDir := sandboxWritableTempDir(t)
	stdout, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"python3",
		[]string{
			"-c",
			fmt.Sprintf("import sys; sys.stdout.write('x' * %d); sys.stderr.write('y' * %d)", compileOutputCaptureBytes+1024, compileOutputCaptureBytes+2048),
		},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stderr=%q", status, reason, stderr)
	}
	if len(stdout) != compileOutputCaptureBytes {
		t.Fatalf("stdout length=%d, want cap %d", len(stdout), compileOutputCaptureBytes)
	}
	if len(stderr) != compileOutputCaptureBytes {
		t.Fatalf("stderr length=%d, want cap %d", len(stderr), compileOutputCaptureBytes)
	}
}

func TestCapCompileResponseOutputSetsTruncationFlags(t *testing.T) {
	resp := capCompileResponseOutput(model.CompileResponse{
		Status: model.CompileStatusCompileError,
		Stdout: strings.Repeat("x", compileOutputCaptureBytes+1),
		Stderr: strings.Repeat("y", compileOutputCaptureBytes+2),
	})
	if len(resp.Stdout) != compileOutputCaptureBytes {
		t.Fatalf("stdout length=%d, want cap %d", len(resp.Stdout), compileOutputCaptureBytes)
	}
	if len(resp.Stderr) != compileOutputCaptureBytes {
		t.Fatalf("stderr length=%d, want cap %d", len(resp.Stderr), compileOutputCaptureBytes)
	}
	if !resp.StdoutTruncated {
		t.Fatal("StdoutTruncated=false, want true")
	}
	if !resp.StderrTruncated {
		t.Fatal("StderrTruncated=false, want true")
	}
}

func TestRunSandboxedCommandAllowsWritesBesideNestedCompileSources(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop compile helper to sandbox user")
	}

	workDir := sandboxWritableTempDir(t)
	sourceDir := filepath.Join(workDir, "src", "App")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Program.cs"), []byte("class Program {}"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	stdout, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"/bin/sh",
		[]string{"-c", "mkdir -p src/App/obj && touch src/App/obj/generated.txt"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "obj", "generated.txt")); err != nil {
		t.Fatalf("expected nested generated file: %v", err)
	}
}

func TestRunSandboxedCommandPreventsRemovingOrReplacingSubmittedCompileSources(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop compile helper to sandbox user")
	}

	workDir := sandboxWritableTempDir(t)
	if err := materializeSources(workDir, []model.Source{
		{
			Name:    "pkg/Main.py",
			DataB64: b64String("print('safe')\n"),
		},
	}); err != nil {
		t.Fatalf("materializeSources: %v", err)
	}
	if err := hardenCompileWorkspace(workDir); err != nil {
		t.Fatalf("hardenCompileWorkspace: %v", err)
	}

	stdout, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"/bin/sh",
		[]string{"-c", "rm -f pkg/Main.py || true; printf 'print(\"pwned\")\\n' > pkg/Main.py 2>/dev/null || true; printf 'ok\\n' > pkg/generated.txt"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}

	raw, err := os.ReadFile(filepath.Join(workDir, "pkg", "Main.py"))
	if err != nil {
		t.Fatalf("read Main.py: %v", err)
	}
	if string(raw) != "print('safe')\n" {
		t.Fatalf("submitted source changed: %q", string(raw))
	}
	if _, err := os.Stat(filepath.Join(workDir, "pkg", "generated.txt")); err != nil {
		t.Fatalf("expected generated sibling file: %v", err)
	}
}

func TestRunCommandRejectsNetworkSockets(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import errno, socket, sys\ntry:\n    socket.socket()\nexcept OSError as exc:\n    sys.exit(0 if exc.errno in (errno.EPERM, errno.EACCES) else 1)\nsys.exit(1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected socket denial probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsUnixSocketConnectToHost(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	socketDir := t.TempDir()
	if err := os.Chmod(socketDir, 0o777); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	socketPath := filepath.Join(socketDir, "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0o777); err != nil {
		t.Fatalf("chmod unix socket: %v", err)
	}

	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\n    s.settimeout(0.5)\n    s.connect(%q)\n    print('connected')\nexcept OSError:\n    print('blocked')\n",
		socketPath,
	)
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", script},
		nil,
	)
	if status != model.CompileStatusOK || stdout != "blocked\n" {
		t.Fatalf("expected unix socket connect denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandAllowsLocalUnixSocketPairs(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import socket, sys\na, b = socket.socketpair()\na.sendall(b'ok')\nsys.exit(0 if b.recv(2) == b'ok' else 1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected local unix socketpair probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsUnixSocketSendmsgToHost(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-compile-dgram-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(socketPath)
	addr := &net.UnixAddr{Name: socketPath, Net: "unixgram"}
	listener, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatalf("listen unixgram socket: %v", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o777); err != nil {
		t.Fatalf("chmod unixgram socket: %v", err)
	}

	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)\n    s.sendmsg([b'escape'], [], 0, %q)\n    print('sent')\nexcept OSError:\n    print('blocked')\n",
		socketPath,
	)
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", script},
		nil,
	)
	if status != model.CompileStatusOK || stdout != "blocked\n" {
		t.Fatalf("expected unix sendmsg denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
	_ = listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 64)
	if n, _, err := listener.ReadFromUnix(buf); err == nil {
		t.Fatalf("expected no datagram delivery, got %q", string(buf[:n]))
	}
}

func TestRunCommandRejectsNamespaceEscape(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import ctypes, errno, sys\nlibc = ctypes.CDLL(None, use_errno=True)\nif libc.unshare(0x20000) == 0:\n    sys.exit(1)\nsys.exit(0 if ctypes.get_errno() in (errno.EPERM, errno.ENOSYS) else 1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected unshare denial probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsProcessGroupEscape(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import errno, os, sys\ntry:\n    os.setpgid(0, 0)\nexcept OSError as exc:\n    sys.exit(0 if exc.errno in (errno.EPERM, errno.EACCES) else 1)\nsys.exit(1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected process-group denial probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsFilesystemPrivilegeSyscalls(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import errno, os, sys\nopen('owned.txt', 'w').close()\nchecks = [\n    ('chmod', lambda: os.chmod('owned.txt', 0o777)),\n    ('chown', lambda: os.chown('owned.txt', os.getuid(), os.getgid())),\n    ('mknod', lambda: os.mknod('node')),\n]\nfor name, action in checks:\n    try:\n        action()\n        print(name + ':escaped')\n        sys.exit(1)\n    except OSError as exc:\n        if exc.errno not in (errno.EPERM, errno.EACCES, errno.ENOSYS):\n            print(name + ':error:' + str(exc.errno))\n            sys.exit(1)\nprint('blocked')\n"},
		nil,
	)
	if status != model.CompileStatusOK || stdout != "blocked\n" {
		t.Fatalf("expected filesystem privilege syscall denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsKernelAttackSurfaceSyscalls(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		cc, err = exec.LookPath("gcc")
	}
	if err != nil {
		t.Skip("C compiler is unavailable on this runner")
	}

	code := `
#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

static int check(const char *name, long nr) {
	errno = 0;
	long rc = syscall(nr, 0, 0, 0, 0, 0, 0);
	if (rc == -1 && (errno == EPERM || errno == EACCES || errno == ENOSYS)) {
		return 0;
	}
	printf("%s:%ld:%s\n", name, rc, strerror(errno));
	return 1;
}

int main(void) {
	int failed = 0;
#ifdef SYS_bpf
	failed |= check("bpf", SYS_bpf);
#endif
#ifdef SYS_userfaultfd
	failed |= check("userfaultfd", SYS_userfaultfd);
#endif
#ifdef SYS_io_uring_setup
	failed |= check("io_uring_setup", SYS_io_uring_setup);
#endif
#ifdef SYS_perf_event_open
	failed |= check("perf_event_open", SYS_perf_event_open);
#endif
#ifdef SYS_memfd_create
	failed |= check("memfd_create", SYS_memfd_create);
#endif
	if (failed != 0) {
		return 1;
	}
	puts("blocked");
	return 0;
}
`
	workDir := sandboxWritableTempDir(t)
	binPath := filepath.Join(workDir, "kernel-syscall-probe")
	compileCmd := exec.Command(cc, "-O2", "-x", "c", "-", "-o", binPath)
	compileCmd.Stdin = strings.NewReader(code)
	output, err := compileCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compile syscall probe: %v\n%s", err, string(output))
	}

	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		binPath,
		nil,
		nil,
	)
	if status != model.CompileStatusOK || stdout != "blocked\n" {
		t.Fatalf("expected kernel syscall denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandCannotReadOrWriteRootOwnedHostPaths(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop compile helper to sandbox user")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	secretDir := t.TempDir()
	if err := os.Chmod(secretDir, 0o700); err != nil {
		t.Fatalf("chmod secret dir: %v", err)
	}
	secretPath := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top-secret"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	script := fmt.Sprintf(
		"from pathlib import Path\nfor label, action in [('read', lambda: Path(%q).read_text()), ('write', lambda: Path(%q).write_text('escape'))]:\n    try:\n        action()\n        print(label + ':escaped')\n    except Exception:\n        print(label + ':blocked')\n",
		secretPath,
		filepath.Join(secretDir, "created.txt"),
	)
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", script},
		nil,
	)
	if status != model.CompileStatusOK || stdout != "read:blocked\nwrite:blocked\n" {
		t.Fatalf("expected host path read/write denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandDoesNotLeakInheritedFileDescriptors(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	workDir := sandboxWritableTempDir(t)
	fdFile, err := os.CreateTemp(workDir, "inherited-fd-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer fdFile.Close()

	if _, err := fdFile.WriteString("secret"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if _, err := fdFile.Seek(0, 0); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	fd := int(fdFile.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatalf("F_GETFD: %v", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
		t.Fatalf("F_SETFD: %v", err)
	}

	stdout, stderr, status, reason := runCommand(
		context.Background(),
		workDir,
		python,
		[]string{"-c", "import errno, os, sys\nfd = int(sys.argv[1])\ntry:\n    os.read(fd, 1)\nexcept OSError as exc:\n    sys.exit(0 if exc.errno == errno.EBADF else 1)\nsys.exit(1)\n", strconv.Itoa(fd)},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected inherited fd probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}
