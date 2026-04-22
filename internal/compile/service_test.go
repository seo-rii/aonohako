package compile

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/model"
)

func b64String(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func b64Bytes(v []byte) string {
	return base64.StdEncoding.EncodeToString(v)
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
	workDir := t.TempDir()
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
