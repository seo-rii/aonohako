package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDotnetRuntimeStateIsScopedToWorkspaceAndSealedAfterRelease(t *testing.T) {
	base := t.TempDir()
	workDir := filepath.Join(base, "work")
	globalPath := filepath.Join(base, ".dotnet")
	konanPath := filepath.Join(base, ".konan-lock")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("Mkdir(work): %v", err)
	}
	if err := os.Mkdir(globalPath, 0o700); err != nil {
		t.Fatalf("Mkdir(global): %v", err)
	}

	lease, err := acquireRuntimeStateAt(workDir, "dotnet", os.Geteuid(), os.Getegid(), true, globalPath, konanPath)
	if err != nil {
		t.Fatalf("acquireRuntimeStateAt(dotnet): %v", err)
	}
	target, err := os.Readlink(globalPath)
	if err != nil {
		t.Fatalf("Readlink(global): %v", err)
	}
	wantTarget := filepath.Join(workDir, ".dotnet-shared")
	if target != wantTarget {
		t.Fatalf("global target = %q, want %q", target, wantTarget)
	}
	probe := filepath.Join(globalPath, "lockfiles", "global", "probe")
	if err := os.WriteFile(probe, []byte("state"), 0o600); err != nil {
		t.Fatalf("WriteFile(probe): %v", err)
	}
	if _, err := os.Stat(filepath.Join(wantTarget, "lockfiles", "global", "probe")); err != nil {
		t.Fatalf("workspace state was not materialized: %v", err)
	}
	if _, err := acquireRuntimeStateAt(workDir, "python3", os.Geteuid(), os.Getegid(), false, globalPath, konanPath); err == nil {
		t.Fatal("ordinary sandbox must fail closed while fixed runtime state is exposed")
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release(): %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second Release(): %v", err)
	}
	info, err := os.Lstat(globalPath)
	if err != nil {
		t.Fatalf("Lstat(global): %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("sealed global mode = %v, want directory 0700", info.Mode())
	}
}

func TestKonanCacheLockIsScopedToWorkspaceAndSealedAfterRelease(t *testing.T) {
	base := t.TempDir()
	workDir := filepath.Join(base, "work")
	dotnetPath := filepath.Join(base, ".dotnet")
	globalPath := filepath.Join(base, ".lock")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("Mkdir(work): %v", err)
	}
	if err := os.WriteFile(globalPath, nil, 0o400); err != nil {
		t.Fatalf("WriteFile(global): %v", err)
	}

	lease, err := acquireRuntimeStateAt(workDir, "kotlinc-native", os.Geteuid(), os.Getegid(), true, dotnetPath, globalPath)
	if err != nil {
		t.Fatalf("acquireRuntimeStateAt(kotlinc-native): %v", err)
	}
	target, err := os.Readlink(globalPath)
	if err != nil {
		t.Fatalf("Readlink(global): %v", err)
	}
	if target != filepath.Join(workDir, ".konan-cache-lock") {
		t.Fatalf("global target = %q", target)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release(): %v", err)
	}
	info, err := os.Lstat(globalPath)
	if err != nil {
		t.Fatalf("Lstat(global): %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
		t.Fatalf("sealed lock mode = %v, want regular 0400", info.Mode())
	}
}

func TestOrdinaryRuntimeStateLeasesCanOverlap(t *testing.T) {
	first, err := acquireRuntimeStateAt(t.TempDir(), "python3", os.Geteuid(), os.Getegid(), false, "", "")
	if err != nil {
		t.Fatalf("first ordinary lease: %v", err)
	}
	defer first.Release()
	second, err := acquireRuntimeStateAt(t.TempDir(), "node", os.Geteuid(), os.Getegid(), false, "", "")
	if err != nil {
		t.Fatalf("second ordinary lease: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release(): %v", err)
	}
}

func TestPowerShellDoesNotAcquireDotnetSharedRuntimeState(t *testing.T) {
	dotnetPath := filepath.Join(t.TempDir(), ".dotnet")
	first, err := acquireRuntimeStateAt(t.TempDir(), "python3", os.Geteuid(), os.Getegid(), true, dotnetPath, "")
	if err != nil {
		t.Fatalf("ordinary lease: %v", err)
	}
	defer first.Release()
	powershell, err := acquireRuntimeStateAt(t.TempDir(), "pwsh", os.Geteuid(), os.Getegid(), true, dotnetPath, "")
	if err != nil {
		t.Fatalf("PowerShell ordinary lease: %v", err)
	}
	if err := powershell.Release(); err != nil {
		t.Fatalf("PowerShell Release(): %v", err)
	}
	if _, err := os.Lstat(dotnetPath); !os.IsNotExist(err) {
		t.Fatalf("PowerShell touched dotnet shared state: %v", err)
	}
}
