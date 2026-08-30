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
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	packageContextCopy := strings.Index(body, "COPY --from=aonohako-python-packages / /usr/local/lib/aonohako/python/")
	trustedHookCopy := strings.Index(body, "COPY --chmod=0644 python/sitecustomize.py /usr/local/lib/aonohako/python/sitecustomize.py")
	if packageContextCopy == -1 || trustedHookCopy == -1 || trustedHookCopy < packageContextCopy {
		t.Fatalf("runtime.Dockerfile must copy the fixed trusted sitecustomize after the custom Python package context")
	}
	for _, marker := range []string{
		"ARG PYTHON_LIBRARY_ISOLATION=false",
		"ARG PYPY_LIBRARY_ISOLATION=false",
		"ARG PYTHON_EXTERNAL_LIBRARY_GID=65530",
		`if [[ "${PYTHON_LIBRARY_ISOLATION}" == "true" ]]`,
		`if [[ "${PYPY_LIBRARY_ISOLATION}" == "true" ]]`,
		`if [[ "${PYTHON_LIBRARY_ISOLATION}" == "true" || "${PYPY_LIBRARY_ISOLATION}" == "true" ]]`,
		"/usr/lib/python*/dist-packages",
		"/usr/local/lib/python*/site-packages",
		"/usr/lib/pypy*/dist-packages",
		"/usr/local/lib/pypy*/dist-packages",
		"/usr/lib/pypy*/site-packages",
		"/usr/local/lib/pypy*/site-packages",
		"/usr/share/python-wheels",
		"/usr/local/lib/aonohako/python",
		`chown -R "0:${PYTHON_EXTERNAL_LIBRARY_GID}"`,
		`find "${path}" -type d -exec chmod 0750`,
		`find "${path}" -type f ! -perm /0111 -exec chmod 0640`,
		"AONOHAKO_PYTHON_LIBRARY_ISOLATION=${PYTHON_LIBRARY_ISOLATION}",
		"AONOHAKO_PYPY_LIBRARY_ISOLATION=${PYPY_LIBRARY_ISOLATION}",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile missing Python permission marker %q", marker)
		}
	}
}
