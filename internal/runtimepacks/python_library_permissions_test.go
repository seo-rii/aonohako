package runtimepacks

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aonohako/internal/pythonpolicy"
)

func TestDockerBuildEnablesPythonLibraryIsolationForMatchingRuntimes(t *testing.T) {
	pythonBuild := (ImageSpec{
		Name:      "python-runtime",
		BaseImage: "debian:trixie-slim",
		Languages: []string{"plain", "python"},
	}).DockerBuild(".", "aonohako")
	if got := pythonBuild.BuildArgs["PYTHON_LIBRARY_ISOLATION"]; got != "true" {
		t.Fatalf("Python isolation arg = %q, want true", got)
	}
	if got := pythonBuild.BuildArgs["PYTHON_EXTERNAL_LIBRARY_GID"]; got != strconv.FormatUint(uint64(pythonpolicy.ExternalLibraryGID), 10) {
		t.Fatalf("Python library GID arg = %q", got)
	}
	if got := pythonBuild.BuildArgs["PYPY_LIBRARY_ISOLATION"]; got != "false" {
		t.Fatalf("PyPy isolation arg in Python-only image = %q, want false", got)
	}

	pypyBuild := (ImageSpec{
		Name:      "pypy-runtime",
		BaseImage: "debian:trixie-slim",
		Languages: []string{"plain", "pypy"},
	}).DockerBuild(".", "aonohako")
	if got := pypyBuild.BuildArgs["PYTHON_LIBRARY_ISOLATION"]; got != "false" {
		t.Fatalf("CPython isolation arg in PyPy-only image = %q, want false", got)
	}
	if got := pypyBuild.BuildArgs["PYPY_LIBRARY_ISOLATION"]; got != "true" {
		t.Fatalf("PyPy isolation arg = %q, want true", got)
	}
	if got := pypyBuild.BuildArgs["PYTHON_EXTERNAL_LIBRARY_GID"]; got != strconv.FormatUint(uint64(pythonpolicy.ExternalLibraryGID), 10) {
		t.Fatalf("PyPy library GID arg = %q", got)
	}

	nonPythonBuild := (ImageSpec{
		Name:      "plain-runtime",
		BaseImage: "debian:trixie-slim",
		Languages: []string{"plain"},
	}).DockerBuild(".", "aonohako")
	if got := nonPythonBuild.BuildArgs["PYTHON_LIBRARY_ISOLATION"]; got != "false" {
		t.Fatalf("non-Python isolation arg = %q, want false", got)
	}
	if got := nonPythonBuild.BuildArgs["PYPY_LIBRARY_ISOLATION"]; got != "false" {
		t.Fatalf("non-PyPy isolation arg = %q, want false", got)
	}
}

func TestRuntimeDockerfilePermissionIsolatesPythonLibraries(t *testing.T) {
	root := filepath.Join("..", "..")
	data, err := os.ReadFile(filepath.Join(root, "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)

	beforeRuntime, runtime, found := strings.Cut(body, "\nFROM runtime-toolchain AS runtime\n")
	if !found {
		t.Fatal("runtime.Dockerfile missing final runtime stage")
	}
	_, toolchain, found := strings.Cut(beforeRuntime, "\nFROM runtime-foundation AS runtime-toolchain\n")
	if !found {
		t.Fatal("runtime.Dockerfile missing toolchain stage")
	}

	mount := "source=scripts/isolate_python_libraries.sh,target=/tmp/aonohako-isolate-python-libraries.sh,ro"
	helper := "bash /tmp/aonohako-isolate-python-libraries.sh "
	isolationArgs := ` "${PYTHON_EXTERNAL_LIBRARY_GID}" "${PYTHON_LIBRARY_ISOLATION}" "${PYPY_LIBRARY_ISOLATION}"`
	assertOrderedDockerRunMarkers(t, toolchain, helper+"initial"+isolationArgs,
		mount,
		"python3 -m pip install",
		"/usr/local/go/bin/go mod download",
		"env -u INSTALL_SCRIPT",
		"/usr/local/cargo/bin/cargo vendor",
		"npm install --global",
		helper+"initial"+isolationArgs,
	)

	packageContextCopy := strings.Index(runtime, "COPY --from=aonohako-python-packages / /usr/local/lib/aonohako/python/")
	trustedHookCopy := strings.Index(runtime, "COPY --chmod=0644 python/sitecustomize.py /usr/local/lib/aonohako/python/sitecustomize.py")
	reconcile := helper + "reconcile" + isolationArgs
	if packageContextCopy == -1 || trustedHookCopy <= packageContextCopy || strings.Index(runtime, reconcile) <= trustedHookCopy {
		t.Fatal("runtime.Dockerfile must copy custom Python packages, overlay the trusted hook, then harden permissions")
	}
	assertOrderedDockerRunMarkers(t, runtime, reconcile,
		mount,
		`for tool in ${SANDBOX_TOOLS}`,
		reconcile,
		`if [[ "${PYTHON_LIBRARY_ISOLATION}" == "true" || "${PYPY_LIBRARY_ISOLATION}" == "true" ]]`,
		"path=/usr/local/lib/aonohako/python",
		`chown -R "0:${PYTHON_EXTERNAL_LIBRARY_GID}"`,
		`find "${path}" -type d -exec chmod 0750`,
		`find "${path}" -type f -perm /0111 -exec chmod 0750`,
		`find "${path}" -type f ! -perm /0111 -exec chmod 0640`,
		"for path in /usr/lib/python*/dist-packages/pip",
		`chmod -R go-rwx "${path}"`,
	)

	for _, marker := range []string{
		"ARG PYTHON_LIBRARY_ISOLATION=false",
		"ARG PYPY_LIBRARY_ISOLATION=false",
		"ARG PYTHON_EXTERNAL_LIBRARY_GID=65530",
	} {
		if !strings.Contains(toolchain, marker) {
			t.Errorf("runtime toolchain is missing Python permission arg %q", marker)
		}
	}
	for _, marker := range []string{
		"AONOHAKO_PYTHON_LIBRARY_ISOLATION=${PYTHON_LIBRARY_ISOLATION}",
		"AONOHAKO_PYPY_LIBRARY_ISOLATION=${PYPY_LIBRARY_ISOLATION}",
		"AONOHAKO_PYTHON_EXTERNAL_LIBRARY_GID=${PYTHON_EXTERNAL_LIBRARY_GID}",
	} {
		if !strings.Contains(runtime, marker) {
			t.Errorf("runtime.Dockerfile missing Python permission marker %q", marker)
		}
	}

	data, err = os.ReadFile(filepath.Join(root, "scripts", "isolate_python_libraries.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`if [[ "${python_isolation}" == "true" ]]`,
		`if [[ "${pypy_isolation}" == "true" ]]`,
		"/usr/lib/python*/dist-packages",
		"/usr/local/lib/python*/dist-packages",
		"/usr/lib/python*/site-packages",
		"/usr/local/lib/python*/site-packages",
		"/usr/lib/pypy*/dist-packages",
		"/usr/local/lib/pypy*/dist-packages",
		"/usr/lib/pypy*/site-packages",
		"/usr/local/lib/pypy*/site-packages",
		"/usr/share/python-wheels",
		`chown -R "0:${gid}"`,
		`! -uid 0 -o ! -gid "${gid}" \) -exec chown -h "0:${gid}"`,
		"-type d ! -perm 0750 -exec chmod 0750",
		"-type f -perm /0111 ! -perm 0750 -exec chmod 0750",
		"-type f ! -perm /0111 ! -perm 0640 -exec chmod 0640",
	} {
		if !strings.Contains(string(data), marker) {
			t.Errorf("Python isolation helper missing %q", marker)
		}
	}
}

func assertOrderedDockerRunMarkers(t *testing.T, stage, command string, markers ...string) {
	t.Helper()

	for _, instruction := range strings.Split(strings.ReplaceAll(stage, "\\\n", ""), "\n") {
		if !strings.HasPrefix(instruction, "RUN ") || !strings.Contains(instruction, command) {
			continue
		}
		for _, marker := range markers {
			_, remaining, found := strings.Cut(instruction, marker)
			if !found {
				t.Fatalf("Dockerfile RUN for %q is missing %q in the expected order", command, marker)
			}
			instruction = remaining
		}
		return
	}
	t.Fatalf("Dockerfile is missing a RUN instruction for %q", command)
}
