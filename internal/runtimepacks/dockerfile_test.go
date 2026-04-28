package runtimepacks

import (
	"os"
	"os/exec"
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
	if !strings.Contains(body, "ARG RUNTIME_BASE=debian:trixie-slim@sha256:") {
		t.Fatalf("runtime.Dockerfile must default runtime images to digest-pinned debian:trixie-slim")
	}
}

func TestDockerfilesPinExternalBaseImagesByDigest(t *testing.T) {
	requiredArgs := map[string][]string{
		filepath.Join("..", "..", "Dockerfile"): {
			"GO_IMAGE",
			"RUNTIME_BASE",
			"DOTNET_SDK_IMAGE",
			"PYTHON_IMAGE",
		},
		filepath.Join("..", "..", "docker", "runtime.Dockerfile"): {
			"GO_IMAGE",
			"RUNTIME_BASE",
		},
	}
	pinnedArgPattern := regexp.MustCompile(`(?m)^ARG ([A-Z0-9_]+)=[^\s]+@sha256:[0-9a-f]{64}$`)
	unpinnedDirectFromPattern := regexp.MustCompile(`(?m)^FROM( --platform=\$BUILDPLATFORM)? [^{$\s][^\s@]*:[^\s@]*( AS|$)`)
	for path, args := range requiredArgs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		body := string(data)
		matches := pinnedArgPattern.FindAllStringSubmatch(body, -1)
		pinned := make(map[string]bool, len(matches))
		for _, match := range matches {
			pinned[match[1]] = true
		}
		for _, arg := range args {
			if !pinned[arg] {
				t.Fatalf("%s must define digest-pinned ARG %s", path, arg)
			}
		}
		if match := unpinnedDirectFromPattern.FindString(body); match != "" {
			t.Fatalf("%s contains unpinned direct external FROM %q", path, match)
		}
	}
}

func TestRepositoryPolicyScriptDoesNotRequireRipgrep(t *testing.T) {
	root := filepath.Join("..", "..")
	binDir := t.TempDir()
	for _, tool := range []string{"dirname", "grep"} {
		target := filepath.Join("/usr/bin", tool)
		if _, err := os.Stat(target); err != nil {
			t.Skipf("%s unavailable: %v", target, err)
		}
		if err := os.Symlink(target, filepath.Join(binDir, tool)); err != nil {
			t.Fatalf("symlink %s: %v", tool, err)
		}
	}
	cmd := exec.Command("/usr/bin/bash", filepath.Join("scripts", "check_repo_policy.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+binDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check_repo_policy.sh without rg: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "repository policy check passed") {
		t.Fatalf("policy output missing success line: %q", string(out))
	}
}

func TestRuntimeDockerfileUsesGo126BuilderImage(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	m := regexp.MustCompile(`ARG GO_IMAGE=golang:(\d+\.\d+)-bookworm@sha256:[a-f0-9]{64}`).FindStringSubmatch(string(data))
	if len(m) != 2 {
		t.Fatalf("runtime.Dockerfile is missing a parseable digest-pinned GO_IMAGE default")
	}
	if m[1] != "1.26" {
		t.Fatalf("GO_IMAGE default = %s, want 1.26 to satisfy go.mod and CI image builds", m[1])
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

func TestRuntimeDockerfileInstallsNpmPackagesAfterInstallScript(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	installRunIndex := strings.Index(body, "/bin/bash -euo pipefail -c \"${INSTALL_SCRIPT}\"")
	npmRunIndex := strings.Index(body, "env NPM_CONFIG_PREFIX=/usr/local npm install --global ${NPM_PACKAGES}")
	if installRunIndex == -1 || npmRunIndex == -1 {
		t.Fatalf("runtime.Dockerfile is missing INSTALL_SCRIPT or npm package installation")
	}
	if installRunIndex > npmRunIndex {
		t.Fatalf("runtime.Dockerfile must run INSTALL_SCRIPT before npm installs so custom node runtimes are available")
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
	if !strings.Contains(body, "install -d -m 0755 /usr/local/lib/aonohako") {
		t.Fatalf("runtime.Dockerfile must create a traversable /usr/local/lib/aonohako directory before copying helpers")
	}
	if !strings.Contains(body, "chmod 0755 /usr/local/lib/aonohako") {
		t.Fatalf("runtime.Dockerfile must keep /usr/local/lib/aonohako traversable for sandboxed helper interpreters")
	}
	if !strings.Contains(body, "chmod 0644 /usr/local/lib/aonohako/brainfuck.py /usr/local/lib/aonohako/whitespace.py") {
		t.Fatalf("runtime.Dockerfile must keep bundled helper scripts world-readable")
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
	for _, marker := range []string{
		"for tool in apt apt-get apt-cache apt-config dpkg dpkg-query dpkg-deb curl wget git pip pip3 npm npx yarn pnpm gem bundle bundler ssh scp sftp rsync nc netcat ncat socat telnet ftp lftp gdb gdbserver strace ltrace tcpdump tshark wireshark nmap dig nslookup host ip ss ifconfig route ping ping6 traceroute tracepath arp arping",
		"chmod 0750 \"$(command -v \"",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile must restrict nonessential runtime tool execution with %q", marker)
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
		"/tmp/.dotnet/shm/global",
		"/tmp/.dotnet/lockfiles/global",
		"/var/tmp",
		"/run/lock",
		"chmod 0755",
		"chown -R 65532:65532 /tmp/.dotnet",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile must statically harden %s to avoid runtime scratch mutation", marker)
		}
	}
}
