package execute

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func forceDirectMode(t *testing.T) {
	t.Helper()
	requireSandboxSupport(t)
}

func requireSandboxSupport(t *testing.T) {
	t.Helper()

	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "probe.sh",
			DataB64: b64("#!/bin/sh\nexit 0\n"),
			Mode:    "exec",
		}},
		ExpectedStdout: "",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status == model.RunStatusAccepted {
		return
	}
	if strings.Contains(resp.Stderr, "sandbox-init:") || strings.Contains(resp.Reason, "sandbox-init:") || strings.Contains(resp.Reason, "sandbox requires root") {
		t.Skipf("sandbox isolation is unavailable on this runner: %+v", resp)
	}
}

func buildCTestBinary(t *testing.T, source string, args ...string) string {
	t.Helper()

	cc, err := exec.LookPath("cc")
	if err != nil {
		cc, err = exec.LookPath("gcc")
	}
	if err != nil {
		t.Skip("C compiler is unavailable on this runner")
	}

	workDir := t.TempDir()
	binPath := filepath.Join(workDir, "runner")
	cmdArgs := []string{"-O2", "-x", "c", "-", "-o", binPath}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command(cc, cmdArgs...)
	cmd.Stdin = strings.NewReader(source)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compile C helper: %v\n%s", err, string(output))
	}

	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read compiled helper: %v", err)
	}
	return base64.StdEncoding.EncodeToString(data)
}

// --------------- #8: buildCommand Java -Xmx dynamic ---------------

func TestBuildCommandJavaXmxDynamic(t *testing.T) {
	tests := []struct {
		name     string
		memoryMB int
		wantXmx  string
	}{
		{"below_32_floors_to_32", 16, "-Xmx32m"},
		{"exactly_32", 32, "-Xmx32m"},
		{"above_32", 128, "-Xmx128m"},
		{"large_memory", 512, "-Xmx512m"},
		{"zero_floors_to_32", 0, "-Xmx32m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &model.RunRequest{Limits: model.Limits{MemoryMB: tc.memoryMB}}
			args := buildCommand("/tmp/Main.jar", "java", req)
			found := false
			for _, a := range args {
				if a == tc.wantXmx {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected %s in args, got %v", tc.wantXmx, args)
			}
			// jar path should be the last arg
			if args[len(args)-1] != "/tmp/Main.jar" {
				t.Errorf("expected jar path as last arg, got %s", args[len(args)-1])
			}
		})
	}
}

func TestBuildCommandJavaAlwaysHasJarFlag(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 128}}
	args := buildCommand("/tmp/Main.jar", "java", req)
	hasJar := false
	for i, a := range args {
		if a == "-jar" && i+1 < len(args) && args[i+1] == "/tmp/Main.jar" {
			hasJar = true
			break
		}
	}
	if !hasJar {
		t.Errorf("expected '-jar /tmp/Main.jar' in args, got %v", args)
	}
}

// --------------- buildCommand all languages ---------------

func TestBuildCommandAllLanguages(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}
	tests := []struct {
		lang      string
		path      string
		wantFirst string
	}{
		{"binary", "/tmp/a.out", "/tmp/a.out"},
		{"ocaml", "/tmp/sol", "env"},
		{"elixir", "/tmp/sol.exs", "env"},
		{"python", "/tmp/sol.py", "python3"},
		{"pypy", "/tmp/sol.py", "pypy3"},
		{"javascript", "/tmp/sol.js", "node"},
		{"ruby", "/tmp/sol.rb", "ruby"},
		{"php", "/tmp/sol.php", "php"},
		{"lua", "/tmp/sol.lua", "lua5.4"},
		{"perl", "/tmp/sol.pl", "perl"},
		{"uhmlang", "/tmp/sol.umm", "/usr/bin/umjunsik-lang-go"},
		{"text", "/tmp/data.txt", "cat"},
		{"unknown_lang", "/tmp/a.out", "/tmp/a.out"},
	}
	for _, tc := range tests {
		t.Run(tc.lang, func(t *testing.T) {
			args := buildCommand(tc.path, tc.lang, req)
			if len(args) == 0 {
				t.Fatalf("buildCommand returned empty args for %s", tc.lang)
			}
			if args[0] != tc.wantFirst {
				t.Errorf("buildCommand(%s) first arg = %q, want %q", tc.lang, args[0], tc.wantFirst)
			}
			// path should be in args
			found := false
			for _, a := range args {
				if a == tc.path {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("buildCommand(%s) missing path %q in %v", tc.lang, tc.path, args)
			}
			if tc.lang == "ocaml" && !containsArg(args, "OCAMLRUNPARAM=s=32k") {
				t.Errorf("buildCommand(%s) missing OCAMLRUNPARAM in %v", tc.lang, args)
			}
			if tc.lang == "elixir" && !containsArg(args, "ERL_AFLAGS=+MIscs 128 +S 1:1 +A 1") {
				t.Errorf("buildCommand(%s) missing ERL_AFLAGS in %v", tc.lang, args)
			}
		})
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestBuildCommandCSharpDLL(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}
	args := buildCommand("/tmp/Program.dll", "csharp", req)
	if args[0] != "dotnet" {
		t.Errorf("csharp .dll should use dotnet, got %v", args)
	}
}

func TestBuildCommandCSharpExe(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}
	args := buildCommand("/tmp/Program", "csharp", req)
	if args[0] != "/tmp/Program" {
		t.Errorf("csharp non-.dll should run directly, got %v", args)
	}
}

func TestBuildCommandEmpty(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{MemoryMB: 64}}
	// default case
	args := buildCommand("/tmp/a.out", "somethingelse", req)
	if len(args) != 1 || args[0] != "/tmp/a.out" {
		t.Errorf("unknown lang should return [path], got %v", args)
	}
}

// --------------- clipUTF8 ---------------

func TestClipUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		n    int
		want string
	}{
		{"short_ascii", []byte("hello"), 10, "hello"},
		{"exact_ascii", []byte("hello"), 5, "hello"},
		{"clip_ascii", []byte("hello world"), 5, "hello"},
		{"empty", []byte{}, 10, ""},
		{"clip_at_zero", []byte("hello"), 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clipUTF8(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("clipUTF8(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestClipUTF8MultibyteNoCut(t *testing.T) {
	// 한글 3 bytes per char: "가나" = 6 bytes
	input := []byte("가나")
	got := clipUTF8(input, 6)
	if got != "가나" {
		t.Errorf("clipUTF8(가나, 6) = %q, want 가나", got)
	}
}

func TestClipUTF8MultibyteCutsCleanly(t *testing.T) {
	// "가나" = 6 bytes; clipping at 4 should give "가" (3 bytes)
	input := []byte("가나")
	got := clipUTF8(input, 4)
	if got != "가" {
		t.Errorf("clipUTF8(가나, 4) = %q, want 가", got)
	}
}

func TestClipUTF8InvalidBytes(t *testing.T) {
	// Invalid UTF-8 that's shorter than n: should trim invalid tail
	input := []byte{0xff, 0xfe}
	got := clipUTF8(input, 10)
	if got != "" {
		t.Errorf("clipUTF8(invalid, 10) = %q, want empty", got)
	}
}

func TestClipUTF8MixedValid(t *testing.T) {
	// "a가" = 1 + 3 = 4 bytes; clipping at 3 should give "a"
	input := []byte("a가")
	got := clipUTF8(input, 3)
	if got != "a" {
		t.Errorf("clipUTF8(a가, 3) = %q, want a", got)
	}
}

// --------------- addressSpaceLimitBytes ---------------

func TestAddressSpaceLimitBytes(t *testing.T) {
	tests := []struct {
		memMB int
		want  uint64
	}{
		{16, 512 * 1024 * 1024},  // 16+64=80 < 512, floors to 512
		{128, 512 * 1024 * 1024}, // 128+64=192 < 512, floors to 512
		{256, 512 * 1024 * 1024}, // 256+64=320 < 512, floors to 512
		{512, 576 * 1024 * 1024}, // 512+64=576
		{0, 512 * 1024 * 1024},   // 0+64=64 < 512, floors to 512
	}
	for _, tc := range tests {
		got := addressSpaceLimitBytes(tc.memMB)
		if got != tc.want {
			t.Errorf("addressSpaceLimitBytes(%d) = %d, want %d", tc.memMB, got, tc.want)
		}
	}
}

func TestAddressSpaceLimitBytesAlwaysAtLeast512MB(t *testing.T) {
	minBytes := uint64(512 * 1024 * 1024)
	for memMB := 0; memMB <= 200; memMB++ {
		got := addressSpaceLimitBytes(memMB)
		if got < minBytes {
			t.Errorf("addressSpaceLimitBytes(%d) = %d, below minimum %d", memMB, got, minBytes)
		}
	}
}

// --------------- max ---------------

func TestMaxHelper(t *testing.T) {
	if got := max(3, 5); got != 5 {
		t.Errorf("max(3,5) = %d", got)
	}
	if got := max(5, 3); got != 5 {
		t.Errorf("max(5,3) = %d", got)
	}
	if got := max(-1, 0); got != 0 {
		t.Errorf("max(-1,0) = %d", got)
	}
	if got := max(7, 7); got != 7 {
		t.Errorf("max(7,7) = %d", got)
	}
}

// --------------- Run edge cases ---------------

func TestRunNilRequest(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), nil, Hooks{})
	if resp.Status != model.RunStatusInitFail {
		t.Errorf("expected InitFail for nil request, got %s", resp.Status)
	}
}

func TestRunNoBinaries(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.RunRequest{}, Hooks{})
	if resp.Status != model.RunStatusInitFail {
		t.Errorf("expected InitFail for empty binaries, got %s", resp.Status)
	}
}

func TestRunEmptyCommand(t *testing.T) {
	svc := New()
	req := &model.RunRequest{
		Lang: "",
		Binaries: []model.Binary{{
			Name:    "empty",
			DataB64: b64(""),
			Mode:    "exec",
		}},
		Limits: model.Limits{TimeMs: 100, MemoryMB: 16},
	}
	// buildCommand with empty file just returns the path;
	// the command may fail but shouldn't panic
	resp := svc.Run(context.Background(), req, Hooks{})
	_ = resp // just ensure no panic
}

// --------------- prepareWorkspaceDirs ---------------

func TestPrepareWorkspaceDirs(t *testing.T) {
	dir := t.TempDir()
	if _, err := prepareWorkspaceDirs(dir); err != nil {
		t.Fatalf("prepareWorkspaceDirs failed: %v", err)
	}
	expected := []string{".home", ".tmp", ".cache", ".mpl", ".pip-cache", "__img__", "box"}
	for _, name := range expected {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected dir %s to exist: %v", name, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s should be a directory", name)
		}
	}
}

func TestPrepareWorkspaceDirsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if _, err := prepareWorkspaceDirs(dir); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := prepareWorkspaceDirs(dir); err != nil {
		t.Fatalf("second (idempotent) call failed: %v", err)
	}
}
