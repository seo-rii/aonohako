package runtimepacks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRuntimeDockerfileDeclaresRuntimeBaseBeforeFirstFrom(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	argIndex := strings.Index(body, "ARG RUNTIME_BASE=")
	goFromIndex := strings.Index(body, "FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS builder")
	runtimeFromIndex := strings.Index(body, "FROM ${RUNTIME_BASE} AS runtime")
	if argIndex == -1 || goFromIndex == -1 || runtimeFromIndex == -1 {
		t.Fatalf("runtime.Dockerfile is missing expected markers")
	}
	if !(argIndex < goFromIndex && goFromIndex < runtimeFromIndex) {
		t.Fatalf("ARG RUNTIME_BASE must be declared before the first FROM to be usable in a later FROM")
	}
	if !strings.Contains(body, "ARG RUNTIME_BASE=debian:trixie-slim") {
		t.Fatalf("runtime.Dockerfile must default runtime images to debian:trixie-slim")
	}
}

func TestRuntimeDockerfileUsesGo123BuilderImage(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	m := regexp.MustCompile(`ARG GO_IMAGE=golang:(\d+\.\d+)-bookworm`).FindStringSubmatch(string(data))
	if len(m) != 2 {
		t.Fatalf("runtime.Dockerfile is missing a parseable GO_IMAGE default")
	}
	if m[1] != "1.23" {
		t.Fatalf("GO_IMAGE default = %s, want 1.23 to satisfy go.mod and CI image builds", m[1])
	}
}

func TestRuntimeDockerfilePATHIncludesSbin(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "PATH=/usr/local/go/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin") {
		t.Fatalf("runtime.Dockerfile PATH must include go/cargo bins and /usr/sbin:/sbin for sandbox tools")
	}
	if !strings.Contains(body, "PYTHONPATH=/usr/local/lib/aonohako/python") {
		t.Fatalf("runtime.Dockerfile must export PYTHONPATH for vendored python judge helpers")
	}
}

func TestRuntimeDockerfileExportsRustToolchainEnv(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	for _, marker := range []string{
		"RUSTUP_HOME=/usr/local/rustup",
		"CARGO_HOME=/usr/local/cargo",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile must export %s for rust toolchain shims", marker)
		}
	}
}

func TestRuntimeDockerfileSupportsInstallScriptBuildArg(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "ARG INSTALL_SCRIPT=") {
		t.Fatalf("runtime.Dockerfile must declare INSTALL_SCRIPT build arg")
	}
	if !strings.Contains(body, "if [[ -n \"${INSTALL_SCRIPT}\" ]]") {
		t.Fatalf("runtime.Dockerfile must execute INSTALL_SCRIPT when provided")
	}
}

func TestRuntimeDockerfileAllowsSystemPipPackagesForPythonRuntime(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "python3 -m pip install --break-system-packages --no-cache-dir ${PIP_PACKAGES}") {
		t.Fatalf("runtime.Dockerfile must allow system-wide pip installs for bundled judge libraries")
	}
}

func TestRuntimeDockerfileCopiesGoBeforeStrictInstallScript(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	goCopyIndex := strings.Index(body, "COPY --from=builder /usr/local/go /usr/local/go")
	installRunIndex := strings.Index(body, "/bin/bash -euo pipefail -c \"${INSTALL_SCRIPT}\"")
	if goCopyIndex == -1 || installRunIndex == -1 {
		t.Fatalf("runtime.Dockerfile is missing go toolchain copy or strict install script execution")
	}
	if goCopyIndex > installRunIndex {
		t.Fatalf("runtime.Dockerfile must copy /usr/local/go before INSTALL_SCRIPT so go-based installers work")
	}
}

func TestRuntimeDockerfileCopiesSandboxSelftestBinary(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "go build -trimpath -ldflags='-s -w -buildid=' -o /out/aonohako-selftest ./cmd/selftest") {
		t.Fatalf("runtime.Dockerfile must build the sandbox selftest binary")
	}
	if !strings.Contains(body, "COPY --from=builder /out/aonohako-selftest /usr/local/bin/aonohako-selftest") {
		t.Fatalf("runtime.Dockerfile must copy the sandbox selftest binary into runtime images")
	}
	if !strings.Contains(body, "COPY python/ /usr/local/lib/aonohako/python/") {
		t.Fatalf("runtime.Dockerfile must copy vendored python judge helpers into runtime images")
	}
	if !strings.Contains(body, "find /usr/local/lib/aonohako/python -type d -exec chmod 0755 {} +") {
		t.Fatalf("runtime.Dockerfile must preserve traversable permissions on vendored python helper directories")
	}
}

func TestRuntimeDockerfileCreatesProtectedRootOwnedSandboxPath(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "/var/aonohako/protected") {
		t.Fatalf("runtime.Dockerfile must create a protected runtime-owned path for sandbox permission checks")
	}
	if !strings.Contains(body, "chmod 0700 /var/aonohako /var/aonohako/protected") {
		t.Fatalf("runtime.Dockerfile must restrict the protected runtime path to root")
	}
}

func TestRuntimeDockerfileHardensImageMetadataAndPackageManagerPaths(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	for _, marker := range []string{
		"/etc/debian_version",
		"/etc/os-release",
		"/etc/apt",
		"/usr/share/doc",
		"/usr/share/common-licenses",
		"/usr/share/bash-completion",
		"/usr/share/man",
		"/var/cache/debconf",
		"/var/lib/dpkg",
		"/var/lib/systemd",
		"/var/cache/apt",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile must harden %s to reduce image read surface", marker)
		}
	}
}

func TestRuntimeDockerfileHardensSharedScratchPathsAtBuildTime(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	for _, marker := range []string{
		"/tmp",
		"/var/tmp",
		"/run/lock",
		"chmod 0755",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile must statically harden %s to avoid runtime scratch mutation", marker)
		}
	}
}
