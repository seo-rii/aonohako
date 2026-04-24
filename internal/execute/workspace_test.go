package execute

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"aonohako/internal/model"
)

func TestPrepareWorkspaceDirsCreatesWritableBox(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	info, err := os.Stat(ws.BoxDir)
	if err != nil {
		t.Fatalf("stat box dir: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat sys payload has unexpected type %T", info.Sys())
	}
	if info.Mode().Perm() != 0o777 || stat.Mode&0o1000 == 0 {
		t.Fatalf("box dir mode = %v, want sticky writable directory", info.Mode())
	}
}

func TestPrepareWorkspaceDirsCreatesToolchainStateDirs(t *testing.T) {
	workDir := t.TempDir()
	_, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	for _, rel := range []string{
		".home",
		".tmp",
		".cache",
		".mpl",
		".pip-cache",
		".dotnet-home",
		".nuget",
		".konan-home",
		".konan",
		".mix",
		".hex",
		"__img__",
	} {
		info, statErr := os.Stat(filepath.Join(workDir, rel))
		if statErr != nil {
			t.Fatalf("expected %s to exist: %v", rel, statErr)
		}
		if !info.IsDir() {
			t.Fatalf("%s should be a directory", rel)
		}
	}
}

func TestMaterializeFilesStoresProgramsInsideBoxWithImmutableModes(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	req := &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{
			{Name: "Main", DataB64: b64("#!/bin/sh\necho ok\n"), Mode: "exec"},
			{Name: "input.txt", DataB64: b64("sample"), Mode: ""},
		},
	}

	primary, _, err := materializeFiles(ws, req)
	if err != nil {
		t.Fatalf("materializeFiles: %v", err)
	}

	if filepath.Dir(primary) != ws.BoxDir {
		t.Fatalf("primary path dir = %q, want %q", filepath.Dir(primary), ws.BoxDir)
	}

	mainInfo, err := os.Stat(filepath.Join(ws.BoxDir, "Main"))
	if err != nil {
		t.Fatalf("stat Main: %v", err)
	}
	if mainInfo.Mode().Perm() != 0o555 {
		t.Fatalf("Main mode = %v, want 0555", mainInfo.Mode())
	}

	dataInfo, err := os.Stat(filepath.Join(ws.BoxDir, "input.txt"))
	if err != nil {
		t.Fatalf("stat input.txt: %v", err)
	}
	if dataInfo.Mode().Perm() != 0o444 {
		t.Fatalf("input.txt mode = %v, want 0444", dataInfo.Mode())
	}
}

func TestMaterializeFilesUsesExplicitPythonEntrypoint(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	primary, lang, err := materializeFiles(ws, &model.RunRequest{
		Lang:       "python",
		EntryPoint: "src/main.py",
		Binaries: []model.Binary{
			{Name: "tools/ignored.py", DataB64: b64("print('wrong')\n")},
			{Name: "data/input.csv", DataB64: b64("2,3\n")},
			{Name: "src/main.py", DataB64: b64("print('right')\n")},
		},
	})
	if err != nil {
		t.Fatalf("materializeFiles: %v", err)
	}
	if lang != "python" {
		t.Fatalf("lang = %q, want python", lang)
	}
	if got := filepath.ToSlash(strings.TrimPrefix(primary, ws.BoxDir+string(os.PathSeparator))); got != "src/main.py" {
		t.Fatalf("primary = %q, want src/main.py", got)
	}
}

func TestMaterializeFilesRejectsMissingEntrypoint(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	_, _, err = materializeFiles(ws, &model.RunRequest{
		Lang:       "python",
		EntryPoint: "src/missing.py",
		Binaries: []model.Binary{
			{Name: "src/main.py", DataB64: b64("print('ok')\n")},
		},
	})
	if err == nil {
		t.Fatalf("expected missing entrypoint validation error")
	}
	if !strings.Contains(err.Error(), "entry_point") {
		t.Fatalf("expected entry_point validation error, got %v", err)
	}
}

func TestMaterializeFilesRejectsEntrypointPathEscape(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	_, _, err = materializeFiles(ws, &model.RunRequest{
		Lang:       "python",
		EntryPoint: "../main.py",
		Binaries: []model.Binary{
			{Name: "src/main.py", DataB64: b64("print('ok')\n")},
		},
	})
	if err == nil {
		t.Fatalf("expected entrypoint path escape validation error")
	}
}

func TestMaterializeFilesRejectsUnsafeJavaEntrypoint(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	_, _, err = materializeFiles(ws, &model.RunRequest{
		Lang:       "java",
		EntryPoint: "Main\r\nClass-Path: /tmp/evil",
		Binaries: []model.Binary{
			{Name: "Main.class", DataB64: b64("class-bytes")},
		},
	})
	if err == nil {
		t.Fatalf("expected unsafe java entrypoint validation error")
	}
	if !strings.Contains(err.Error(), "entry_point") {
		t.Fatalf("expected entry_point validation error, got %v", err)
	}
}

func TestMaterializeFilesPrefersCoqSourceAsPrimary(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	primary, lang, err := materializeFiles(ws, &model.RunRequest{
		Lang: "coq",
		Binaries: []model.Binary{
			{Name: "Main.glob", DataB64: b64("glob")},
			{Name: "Main.v", DataB64: b64("Theorem t : True.\nProof. exact I. Qed.\n")},
			{Name: "Main.vo", DataB64: b64("vo")},
		},
	})
	if err != nil {
		t.Fatalf("materializeFiles: %v", err)
	}
	if lang != "coq" {
		t.Fatalf("lang = %q, want coq", lang)
	}
	if got := filepath.Base(primary); got != "Main.v" {
		t.Fatalf("primary file = %q, want Main.v", got)
	}
}

func TestCaptureFileOutputRejectsSymlink(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	target := filepath.Join(workDir, "outside.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(ws.BoxDir, "result.txt")); err != nil {
		t.Fatalf("symlink result.txt: %v", err)
	}

	if _, err := captureFileOutput(ws, model.OutputFile{Path: "result.txt"}); err == nil {
		t.Fatalf("expected symlink output rejection")
	}
}

func TestCaptureFileOutputDoesNotFallbackFromBoxSymlinkToWorkspaceRoot(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.RootDir, "result.txt"), []byte("workspace-root"), 0o644); err != nil {
		t.Fatalf("write workspace root fallback: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws.BoxDir, "result.txt")); err != nil {
		t.Fatalf("symlink result.txt: %v", err)
	}

	if _, err := captureFileOutput(ws, model.OutputFile{Path: "result.txt"}); err == nil {
		t.Fatalf("expected symlink in box dir to block workspace-root fallback")
	}
}

func TestCaptureFileOutputPrefersBoxContentOverWorkspaceRoot(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	if err := os.WriteFile(filepath.Join(ws.RootDir, "result.txt"), []byte("workspace-root"), 0o644); err != nil {
		t.Fatalf("write workspace root result: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.BoxDir, "result.txt"), []byte("box"), 0o644); err != nil {
		t.Fatalf("write box result: %v", err)
	}

	got, err := captureFileOutput(ws, model.OutputFile{Path: "result.txt"})
	if err != nil {
		t.Fatalf("captureFileOutput: %v", err)
	}
	if string(got) != "box" {
		t.Fatalf("captureFileOutput read %q, want box content", string(got))
	}
}

func TestCaptureSidecarOutputsSkipsEscapingOrSymlinkedPaths(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.RootDir, "result.txt"), []byte("workspace-root"), 0o644); err != nil {
		t.Fatalf("write workspace root fallback: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(ws.BoxDir, "result.txt")); err != nil {
		t.Fatalf("symlink result.txt: %v", err)
	}

	outputs, errs := captureSidecarOutputs(ws, []model.OutputFile{
		{Path: "result.txt"},
		{Path: "../escape.txt"},
		{Path: "/tmp/escape.txt"},
	})
	if len(outputs) != 0 {
		t.Fatalf("expected suspicious sidecar outputs to be ignored, got %+v", outputs)
	}
	if len(errs) != 3 {
		t.Fatalf("expected diagnostics for rejected sidecars, got %+v", errs)
	}
}

func TestMaterializeFilesKeepsNestedPathsReadableAndWritableToSandboxUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop to sandbox user")
	}

	workDir, err := os.MkdirTemp("", "aonohako-workspace-test-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	defer os.RemoveAll(workDir)
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	_, _, err = materializeFiles(ws, &model.RunRequest{
		Lang: "java",
		Binaries: []model.Binary{
			{Name: "pkg/Main.class", DataB64: b64("class-bytes")},
		},
	})
	if err != nil {
		t.Fatalf("materializeFiles: %v", err)
	}
	if err := os.Chmod(ws.RootDir, 0o755); err != nil {
		t.Fatalf("chmod workspace root: %v", err)
	}

	cmd := exec.Command("sh", "-lc", "cat pkg/Main.class >/dev/null && touch pkg/generated.txt && test -f pkg/generated.txt")
	cmd.Dir = ws.BoxDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox user should traverse nested submission dirs and create peers: %v\n%s", err, string(output))
	}
}

func TestMaterializeFilesBuildsReadableSubmissionJarForSandboxUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop to sandbox user")
	}

	workDir, err := os.MkdirTemp("", "aonohako-workspace-test-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	defer os.RemoveAll(workDir)
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}

	jarPath, lang, err := materializeFiles(ws, &model.RunRequest{
		Lang:       "java",
		EntryPoint: "Main",
		Binaries: []model.Binary{
			{Name: "Main.class", DataB64: b64("class-bytes")},
		},
	})
	if err != nil {
		t.Fatalf("materializeFiles: %v", err)
	}
	if lang != "java" {
		t.Fatalf("lang = %q, want java", lang)
	}
	if err := os.Chmod(ws.RootDir, 0o755); err != nil {
		t.Fatalf("chmod workspace root: %v", err)
	}

	cmd := exec.Command("sh", "-lc", "test -r \"$1\"", "sh", jarPath)
	cmd.Dir = ws.BoxDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox user should read generated jar: %v\n%s", err, string(output))
	}
}
