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
	goFromIndex := strings.Index(body, "FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go-toolchain")
	builderFromIndex := strings.Index(body, "FROM go-toolchain AS builder")
	foundationFromIndex := strings.Index(body, "FROM ${RUNTIME_BASE} AS runtime-foundation")
	seedFromIndex := strings.Index(body, "FROM runtime-foundation AS runtime-seed")
	runtimeFromIndex := strings.Index(body, "FROM runtime-foundation AS runtime\n")
	if argIndex == -1 || goFromIndex == -1 || builderFromIndex == -1 || foundationFromIndex == -1 || seedFromIndex == -1 || runtimeFromIndex == -1 {
		t.Fatalf("runtime.Dockerfile is missing expected markers")
	}
	if !(argIndex < goFromIndex && goFromIndex < builderFromIndex && builderFromIndex < foundationFromIndex && foundationFromIndex < seedFromIndex && seedFromIndex < runtimeFromIndex) {
		t.Fatalf("ARG RUNTIME_BASE must be declared before the first FROM to be usable in a later FROM")
	}
	if !strings.Contains(body, "ARG RUNTIME_BASE=debian:trixie-slim@sha256:") {
		t.Fatalf("runtime.Dockerfile must default runtime images to digest-pinned debian:trixie-slim")
	}
}

func TestDockerfilesPinExternalBaseImagesByDigest(t *testing.T) {
	root := filepath.Join("..", "..")
	cmd := exec.Command("bash", filepath.Join("scripts", "check_dockerfile_bases.sh"), "--allow-context", "aonohako-python-packages", "Dockerfile", filepath.Join("docker", "runtime.Dockerfile"))
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("check_dockerfile_bases.sh: %v\n%s", err, out)
	}
}

func TestDockerfileBasePolicyRejectsValidSyntaxBypasses(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{name: "pinned arg", body: "ARG BASE=ubuntu:24.04@" + digest + "\nFROM --platform=$BUILDPLATFORM ${BASE} AS build\nFROM build AS final\n", ok: true},
		{name: "pinned direct", body: "from --platform=linux/amd64 ubuntu:24.04@" + digest + " AS final\n", ok: true},
		{name: "pinned continued from", body: "ARG BASE=ubuntu:24.04@" + digest + "\nFROM \\\n  ${BASE} AS final\n", ok: true},
		{name: "backtick escape", body: "# escape=`\nARG BASE=ubuntu:24.04@" + digest + "\nFROM `\n  ${BASE} AS final\n", ok: true},
		{name: "UTF-8 BOM pinned", body: "\ufeffFROM ubuntu:24.04@" + digest + " AS final\n", ok: true},
		{name: "scratch", body: "FROM scratch\n", ok: true},
		{name: "tagless", body: "FROM ubuntu\n"},
		{name: "lowercase tagged", body: "from ubuntu:latest\n"},
		{name: "explicit platform", body: "FROM --platform=linux/amd64 ubuntu:latest\n"},
		{name: "unchecked arg", body: "ARG OTHER=ubuntu:latest\nFROM ${OTHER}\n"},
		{name: "arg after from", body: "FROM scratch AS seed\nARG OTHER=ubuntu:24.04@" + digest + "\nFROM ${OTHER}\n"},
		{name: "mixed arg expression", body: "ARG REPO=ubuntu\nFROM ${REPO}:latest\n"},
		{name: "split from keyword", body: "FR\\\nOM ubuntu:latest\n"},
		{name: "split from with trailing space", body: "FROM scratch AS seed\nFR\\ \nOM ubuntu:latest\n"},
		{name: "split arg keyword", body: "AR\\\nG BASE=ubuntu:latest\nFROM ${BASE}\n"},
		{name: "backtick split from keyword", body: "# escape=`\nFR`\nOM ubuntu:latest\n"},
		{name: "backtick split from with trailing tab", body: "# escape=`\nFROM scratch AS seed\nFR`\t\nOM ubuntu:latest AS final\n"},
		{name: "spaced backtick split from keyword", body: "# escape = `\nFROM scratch AS seed\nFR`\nOM ubuntu:latest AS final\n"},
		{name: "UTF-8 BOM unpinned", body: "\ufeffFROM ubuntu:latest\nFROM scratch\n"},
		{name: "heredoc fake stage", body: "FROM scratch AS seed\nCOPY <<EOF /payload\nFROM scratch AS ubuntu\nEOF\nFROM ubuntu\n"},
		{name: "no from", body: "ARG BASE=ubuntu:24.04@" + digest + "\n"},
		{name: "unterminated continuation", body: "FROM \\"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Dockerfile")
			if err := os.WriteFile(path, []byte(tt.body), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "check_dockerfile_bases.sh"), path)
			out, err := cmd.CombinedOutput()
			if tt.ok && err != nil {
				t.Fatalf("policy rejected valid Dockerfile: %v\n%s", err, out)
			}
			if !tt.ok && err == nil {
				t.Fatalf("policy accepted unpinned Dockerfile:\n%s", tt.body)
			}
		})
	}
}

func TestDockerfilesUsePatchedRuntimeDependencies(t *testing.T) {
	const patchedTrixie = "debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2"

	runtimeData, err := os.ReadFile(filepath.Join("..", "..", "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatalf("read runtime Dockerfile: %v", err)
	}
	if !strings.Contains(string(runtimeData), "ARG RUNTIME_BASE="+patchedTrixie) {
		t.Fatalf("runtime Dockerfile must use patched trixie base %s", patchedTrixie)
	}

	serverData, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatalf("read server Dockerfile: %v", err)
	}
	serverBody := string(serverData)
	for _, marker := range []string{
		"ARG RUNTIME_BASE=" + patchedTrixie,
		"ARG PYTHON_IMAGE=python:3.13-slim-trixie@sha256:eb43ff125d8d58d7449dcba7d336c23bcac412f526d861db493b9994d8010280",
		"setuptools==80.10.2",
	} {
		if !strings.Contains(serverBody, marker) {
			t.Fatalf("server Dockerfile must contain patched runtime dependency marker %q", marker)
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

func TestRuntimeDockerfileUsesPatchedGoBuilderImage(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	m := regexp.MustCompile(`ARG GO_IMAGE=golang:(\d+\.\d+\.\d+)-bookworm@sha256:[a-f0-9]{64}`).FindStringSubmatch(string(data))
	if len(m) != 2 {
		t.Fatalf("runtime.Dockerfile is missing a parseable digest-pinned GO_IMAGE default")
	}
	if m[1] != "1.26.5" {
		t.Fatalf("GO_IMAGE default = %s, want 1.26.5 to satisfy go.mod and CI image builds", m[1])
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
		t.Fatalf("runtime.Dockerfile must export PYTHONPATH for custom python packages")
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
	if !strings.Contains(body, "env -u INSTALL_SCRIPT /bin/bash -euo pipefail -c \"${INSTALL_SCRIPT}\"") {
		t.Fatalf("runtime.Dockerfile must keep INSTALL_SCRIPT out of child build environments")
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

func TestRuntimeDockerfileBuildsReusableRuntimeSeed(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS go-toolchain") {
		t.Fatalf("runtime.Dockerfile must create a standalone go-toolchain stage")
	}
	if !strings.Contains(body, "FROM go-toolchain AS builder") {
		t.Fatalf("runtime.Dockerfile builder must derive from the shared go-toolchain stage")
	}
	seedMarker := "FROM runtime-foundation AS runtime-seed"
	runtimeMarker := "FROM runtime-foundation AS runtime\n"
	seedIndex := strings.Index(body, seedMarker)
	runtimeIndex := strings.Index(body, runtimeMarker)
	if seedIndex == -1 || runtimeIndex == -1 || seedIndex >= runtimeIndex {
		t.Fatalf("runtime.Dockerfile must declare the reusable runtime-seed before the independent runtime target")
	}

	seedBody := body[seedIndex:runtimeIndex]
	for _, marker := range []string{
		"COPY --from=go-toolchain /usr/local/go /usr/local/go",
		"COPY --from=builder /out/aonohako /usr/local/bin/aonohako",
		"COPY --from=builder /out/aonohako-selftest /usr/local/bin/aonohako-selftest",
	} {
		if !strings.Contains(seedBody, marker) {
			t.Fatalf("runtime-seed must contain reusable build output %q", marker)
		}
	}
	if strings.Contains(body, "FROM runtime-seed AS runtime") {
		t.Fatal("runtime must not derive from runtime-seed because language installs need to run in parallel with builder")
	}
	for _, marker := range []string{"APT_PACKAGES", "PIP_PACKAGES", "NPM_PACKAGES", "INSTALL_SCRIPT"} {
		if strings.Contains(seedBody, marker) {
			t.Fatalf("runtime-seed must not depend on language-specific build arg %q", marker)
		}
	}

	runtimeBody := body[runtimeIndex:]
	goCopyIndex := strings.Index(runtimeBody, "COPY --from=go-toolchain /usr/local/go /usr/local/go")
	installRunIndex := strings.Index(runtimeBody, "env -u INSTALL_SCRIPT /bin/bash -euo pipefail -c \"${INSTALL_SCRIPT}\"")
	if goCopyIndex == -1 || installRunIndex == -1 {
		t.Fatalf("runtime.Dockerfile is missing go toolchain copy or strict install script execution")
	}
	if goCopyIndex > installRunIndex {
		t.Fatalf("runtime.Dockerfile must seed /usr/local/go before INSTALL_SCRIPT so go-based installers work")
	}
	serverCopyIndex := strings.Index(runtimeBody, "COPY --from=builder /out/aonohako /usr/local/bin/aonohako")
	selftestCopyIndex := strings.Index(runtimeBody, "COPY --from=builder /out/aonohako-selftest /usr/local/bin/aonohako-selftest")
	npmRunIndex := strings.Index(runtimeBody, "env NPM_CONFIG_PREFIX=/usr/local npm install --global ${NPM_PACKAGES}")
	if serverCopyIndex == -1 || selftestCopyIndex == -1 || npmRunIndex == -1 {
		t.Fatal("runtime.Dockerfile is missing final binaries or npm package installation")
	}
	if serverCopyIndex < installRunIndex || serverCopyIndex < npmRunIndex || selftestCopyIndex < installRunIndex || selftestCopyIndex < npmRunIndex {
		t.Fatal("runtime must copy trusted binaries after root-level language installers finish")
	}
	if strings.Contains(body, "COPY --from=builder /usr/local/go /usr/local/go") {
		t.Fatalf("runtime.Dockerfile must source the reusable toolchain directly from go-toolchain")
	}
}

func TestRuntimeDockerfileSeparatesCommonAndRuntimeAptPackages(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	foundationMarker := "FROM ${RUNTIME_BASE} AS runtime-foundation"
	seedMarker := "FROM runtime-foundation AS runtime-seed"
	runtimeMarker := "FROM runtime-foundation AS runtime\n"
	foundationIndex := strings.Index(body, foundationMarker)
	seedIndex := strings.Index(body, seedMarker)
	runtimeIndex := strings.Index(body, runtimeMarker)
	if foundationIndex == -1 || seedIndex == -1 || runtimeIndex == -1 || foundationIndex >= seedIndex || seedIndex >= runtimeIndex {
		t.Fatalf("runtime.Dockerfile must derive runtime-seed and runtime independently from runtime-foundation")
	}

	foundationBody := body[foundationIndex:seedIndex]
	if !strings.Contains(foundationBody, "apt-get install -y --no-install-recommends ca-certificates coreutils tini util-linux") {
		t.Fatalf("runtime-foundation must install the common runtime packages")
	}
	if strings.Contains(foundationBody, "APT_PACKAGES") {
		t.Fatalf("runtime-foundation must not depend on language-specific APT_PACKAGES")
	}
	if strings.Count(foundationBody, "apt-get install") != 1 {
		t.Fatalf("runtime-foundation must contain only one common package installation")
	}

	runtimeBody := body[runtimeIndex:]
	for _, marker := range []string{
		"ARG APT_PACKAGES=",
		"if [[ -n \"${APT_PACKAGES}\" ]]",
		"apt-get install -y --no-install-recommends ${APT_PACKAGES}",
	} {
		if !strings.Contains(runtimeBody, marker) {
			t.Fatalf("runtime stage must install language-specific packages separately with %q", marker)
		}
	}
}

func TestRuntimeDockerfileInstallsNpmPackagesAfterInstallScript(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	installRunIndex := strings.Index(body, "env -u INSTALL_SCRIPT /bin/bash -euo pipefail -c \"${INSTALL_SCRIPT}\"")
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
	if !strings.Contains(body, "COPY --from=aonohako-python-packages / /usr/local/lib/aonohako/python/") {
		t.Fatalf("runtime.Dockerfile must copy custom python package context into runtime images")
	}
	if !strings.Contains(body, "install -d -m 0755 /usr/local/lib/aonohako") {
		t.Fatalf("runtime.Dockerfile must create a traversable /usr/local/lib/aonohako directory before copying helpers")
	}
	if !strings.Contains(body, "rm -f /usr/local/lib/aonohako/python/.empty") {
		t.Fatalf("runtime.Dockerfile must remove the empty custom python package marker")
	}
	if !strings.Contains(body, "chmod 0755 /usr/local/lib/aonohako") {
		t.Fatalf("runtime.Dockerfile must keep /usr/local/lib/aonohako traversable for sandboxed helper interpreters")
	}
	if !strings.Contains(body, "chmod 0644 /usr/local/lib/aonohako/brainfuck.py /usr/local/lib/aonohako/whitespace.py") {
		t.Fatalf("runtime.Dockerfile must keep bundled helper scripts world-readable")
	}
	if !strings.Contains(body, "find /usr/local/lib/aonohako/python -type d -exec chmod 0755 {} +") {
		t.Fatalf("runtime.Dockerfile must preserve traversable permissions on custom python package directories")
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
		"/etc/passwd",
		"/etc/group",
		"/etc/shells",
		"/etc/login.defs",
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
		"ARG SANDBOX_TOOLS=",
		"AONOHAKO_SANDBOX_TOOLS=${SANDBOX_TOOLS}",
		"for tool in apt apt-get apt-cache apt-config dpkg dpkg-query dpkg-deb curl wget git pip pip3 npm npx yarn pnpm gem bundle bundler ssh scp sftp rsync nc netcat ncat socat telnet ftp lftp gdb gdbserver strace ltrace tcpdump tshark wireshark nmap dig nslookup host ip ss ifconfig route ping ping6 traceroute tracepath arp arping",
		"chmod 0750 \"$(command -v \"",
		"for tool in ${SANDBOX_TOOLS}; do",
		"chmod 0755 \"$(command -v \"",
		"/usr/lib/python*/dist-packages/pip",
		"/usr/local/lib/node_modules/npm",
		"/opt/node-*/lib/node_modules/npm",
		"if [[ -e \"${path}\" ]]; then chmod -R go-rwx \"${path}\"; fi",
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
