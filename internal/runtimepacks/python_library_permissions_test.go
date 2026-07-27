package runtimepacks

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aonohako/internal/pythonpolicy"
)

func TestDockerBuildEnablesPythonLibraryIsolationOnlyForPythonImages(t *testing.T) {
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

	nonPythonBuild := (ImageSpec{
		Name:      "plain-runtime",
		BaseImage: "debian:trixie-slim",
		Languages: []string{"plain"},
	}).DockerBuild(".", "aonohako")
	if got := nonPythonBuild.BuildArgs["PYTHON_LIBRARY_ISOLATION"]; got != "false" {
		t.Fatalf("non-Python isolation arg = %q, want false", got)
	}
}

func TestRuntimeDockerfilePermissionIsolatesPythonLibraries(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	for _, marker := range []string{
		"ARG PYTHON_LIBRARY_ISOLATION=false",
		"ARG PYTHON_EXTERNAL_LIBRARY_GID=65530",
		`if [[ "${PYTHON_LIBRARY_ISOLATION}" == "true" ]]`,
		"/usr/lib/python*/dist-packages",
		"/usr/local/lib/python*/site-packages",
		"/usr/share/python-wheels",
		"/usr/local/lib/aonohako/python",
		`chown -R "0:${PYTHON_EXTERNAL_LIBRARY_GID}"`,
		`find "${path}" -type d -exec chmod 0750`,
		`find "${path}" -type f ! -perm /0111 -exec chmod 0640`,
		"AONOHAKO_PYTHON_LIBRARY_ISOLATION=${PYTHON_LIBRARY_ISOLATION}",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile missing Python permission marker %q", marker)
		}
	}
}
