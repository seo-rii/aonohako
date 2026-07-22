package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePythonPackagesContextUsesProvidedDirectory(t *testing.T) {
	dir := t.TempDir()

	got, cleanup, err := resolvePythonPackagesContext(dir)
	if err != nil {
		t.Fatalf("resolvePythonPackagesContext(%q): %v", dir, err)
	}
	defer cleanup()

	if got != dir {
		t.Fatalf("context path = %q, want %q", got, dir)
	}
}

func TestResolvePythonPackagesContextCreatesEmptyContext(t *testing.T) {
	got, cleanup, err := resolvePythonPackagesContext("")
	if err != nil {
		t.Fatalf("resolvePythonPackagesContext(empty): %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(got, ".empty")); err != nil {
		t.Fatalf("empty context marker missing: %v", err)
	}
}

func TestResolvePythonPackagesContextRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packages.py")
	if err := os.WriteFile(path, []byte("pass\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, _, err := resolvePythonPackagesContext(path); err == nil {
		t.Fatalf("expected file path to be rejected as python packages context")
	}
}

func TestDefaultPythonPackagesContextUsesEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AONOHAKO_PYTHON_PACKAGES_CONTEXT", dir)

	if got := defaultPythonPackagesContext(); got != dir {
		t.Fatalf("default context = %q, want env directory %q", got, dir)
	}
}

func TestDefaultPythonPackagesContextUsesRepoPythonDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "python"), 0o755); err != nil {
		t.Fatalf("mkdir python context: %v", err)
	}
	t.Setenv("AONOHAKO_PYTHON_PACKAGES_CONTEXT", "")
	t.Chdir(dir)

	if got := defaultPythonPackagesContext(); got != "python" {
		t.Fatalf("default context = %q, want repository python directory", got)
	}
}

func TestDefaultPythonPackagesContextCanStayEmpty(t *testing.T) {
	t.Setenv("AONOHAKO_PYTHON_PACKAGES_CONTEXT", "")
	t.Chdir(t.TempDir())

	if got := defaultPythonPackagesContext(); got != "" {
		t.Fatalf("default context = %q, want empty", got)
	}
}

func TestRuntimeBuilderRejectsUnknownOnlyFilter(t *testing.T) {
	catalogPath := filepath.Join("..", "..", "runtime-images.yml")
	cmd := exec.Command("go", "run", ".", "-catalog", catalogPath, "-mode", "production", "-dry-run", "-only", "definitely-not-an-image")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("runtime-builder succeeded with an unmatched -only filter: %s", out)
	}
	if !strings.Contains(string(out), `no runtime image matches -only "definitely-not-an-image"`) {
		t.Fatalf("runtime-builder error did not explain the unmatched filter: %s", out)
	}
}

func TestRuntimeBuilderRejectsUnpinnedCatalogBaseImage(t *testing.T) {
	catalogPath := filepath.Join(t.TempDir(), "runtime-images.yml")
	body := `
languages:
  plain:
    smoke:
      command: ["plain", "--version"]
profiles:
  type-a:
    base_image: ubuntu:latest
    languages: [plain]
`
	if err := os.WriteFile(catalogPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile catalog: %v", err)
	}
	cmd := exec.Command("go", "run", ".", "-catalog", catalogPath, "-mode", "production", "-dry-run")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("runtime-builder accepted unpinned base image: %s", out)
	}
	if !strings.Contains(string(out), `base_image must be digest-pinned, got "ubuntu:latest"`) {
		t.Fatalf("runtime-builder error did not explain unpinned base image: %s", out)
	}
}

func TestRuntimeBuilderAddsPrebuiltRuntimeBinariesContext(t *testing.T) {
	contextDir := t.TempDir()
	for _, name := range []string{"aonohako", "aonohako-selftest"} {
		if err := os.WriteFile(filepath.Join(contextDir, name), nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	catalogPath := filepath.Join("..", "..", "runtime-images.yml")
	cmd := exec.Command("go", "run", ".", "-catalog", catalogPath, "-mode", "production", "-dry-run", "-only", "type-a")
	cmd.Env = append(os.Environ(), "AONOHAKO_RUNTIME_BINARIES_CONTEXT="+contextDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime-builder failed: %v\n%s", err, out)
	}
	marker := "--build-context aonohako-runtime-binaries=" + contextDir
	if !strings.Contains(string(out), marker) {
		t.Fatalf("runtime-builder output is missing %q:\n%s", marker, out)
	}
}

func TestRuntimeBuilderKeepsDockerfileBinaryFallbackByDefault(t *testing.T) {
	catalogPath := filepath.Join("..", "..", "runtime-images.yml")
	cmd := exec.Command("go", "run", ".", "-catalog", catalogPath, "-mode", "production", "-dry-run", "-only", "type-a")
	cmd.Env = append(os.Environ(), "AONOHAKO_RUNTIME_BINARIES_CONTEXT=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime-builder failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--build-context aonohako-runtime-binaries=") {
		t.Fatalf("runtime-builder must leave the Dockerfile binary stage as the default fallback:\n%s", out)
	}
}

func TestRuntimeBuilderRejectsIncompleteRuntimeBinariesContext(t *testing.T) {
	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "aonohako"), nil, 0o644); err != nil {
		t.Fatalf("write aonohako: %v", err)
	}

	catalogPath := filepath.Join("..", "..", "runtime-images.yml")
	cmd := exec.Command("go", "run", ".", "-catalog", catalogPath, "-mode", "production", "-dry-run", "-only", "type-a")
	cmd.Env = append(os.Environ(), "AONOHAKO_RUNTIME_BINARIES_CONTEXT="+contextDir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("runtime-builder accepted an incomplete binary context: %s", out)
	}
	if !strings.Contains(string(out), "runtime binaries context is missing aonohako-selftest") {
		t.Fatalf("runtime-builder error did not identify the missing binary: %s", out)
	}
}

func TestRuntimeBuilderExportsOnlyDedicatedCacheTarget(t *testing.T) {
	catalogPath := filepath.Join("..", "..", "runtime-images.yml")
	cmd := exec.Command(
		"go", "run", ".",
		"-catalog", catalogPath,
		"-mode", "production",
		"-dry-run",
		"-only", "type-a",
		"-cache-from", "type=registry,ref=example.invalid/cache:type-a",
		"-cache-to", "type=registry,ref=example.invalid/cache:type-a,mode=max",
		"-cache-target", "runtime-toolchain",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime-builder failed: %v\n%s", err, out)
	}
	body := string(out)
	if count := strings.Count(body, "docker buildx build"); count != 2 {
		t.Fatalf("runtime-builder emitted %d commands, want cache target and final image:\n%s", count, out)
	}
	finalIndex := strings.LastIndex(body, "docker buildx build")
	cacheCommand := body[:finalIndex]
	finalCommand := body[finalIndex:]
	if !strings.Contains(cacheCommand, "--target runtime-toolchain") || !strings.Contains(cacheCommand, "--cache-to type=registry,ref=example.invalid/cache:type-a,mode=max") {
		t.Fatalf("cache target command is incomplete: %s", cacheCommand)
	}
	if !strings.Contains(finalCommand, "--cache-from type=registry,ref=example.invalid/cache:type-a") {
		t.Fatalf("final image command must import the persistent cache: %s", finalCommand)
	}
	if strings.Contains(finalCommand, "--cache-to") {
		t.Fatalf("final image command must not export app-specific layers: %s", finalCommand)
	}
}
