package compile

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/model"
	"golang.org/x/sys/unix"
)

func b64String(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func b64Bytes(v []byte) string {
	return base64.StdEncoding.EncodeToString(v)
}

func sandboxWritableTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if os.Geteuid() != 0 {
		return dir
	}
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

func TestRunCommandRejectsSocketPairCreation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import errno, socket, sys\ntry:\n    socket.socketpair()\nexcept OSError as exc:\n    sys.exit(0 if exc.errno in (errno.EPERM, errno.EACCES) else 1)\nsys.exit(1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected socketpair denial probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
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
