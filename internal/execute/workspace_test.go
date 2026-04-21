package execute

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestMaterializeFilesKeepsNestedPathsReadableAndWritableToSandboxUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop to sandbox user")
	}

	workDir := t.TempDir()
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

	workDir := t.TempDir()
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

	cmd := exec.Command("sh", "-lc", "test -r \"$1\"", "sh", jarPath)
	cmd.Dir = ws.BoxDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox user should read generated jar: %v\n%s", err, string(output))
	}
}
