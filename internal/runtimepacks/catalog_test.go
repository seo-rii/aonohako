package runtimepacks

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeCatalogFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-images.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadCatalogBuildsProductionAndCIMatrices(t *testing.T) {
	path := writeCatalogFixture(t, `
languages:
  plain:
    smoke:
      command: ["/bin/sh", "-c", "printf '#!/bin/sh\necho ok\n' > Main && chmod +x Main && [ \"$(./Main)\" = ok ]"]
  python:
    install:
      apt: [python3, python3-numpy]
    smoke:
      command: ["python3", "-c", "import numpy; print(numpy.arange(3).sum())"]
  java:
    install:
      apt: [default-jdk-headless]
    smoke:
      command: ["java", "-version"]
profiles:
  type-a:
    base_image: debian:trixie-slim
    languages: [plain, python]
  type-b:
    base_image: debian:trixie-slim
    languages: [java]
`)

	catalog, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}

	production, err := catalog.ProductionImages()
	if err != nil {
		t.Fatalf("ProductionImages returned error: %v", err)
	}
	if len(production) != 2 {
		t.Fatalf("expected 2 production images, got %d", len(production))
	}

	typeA := production[0]
	if typeA.Name != "type-a" {
		t.Fatalf("first production image name = %q, want type-a", typeA.Name)
	}
	if !reflect.DeepEqual(typeA.Languages, []string{"plain", "python"}) {
		t.Fatalf("type-a languages = %v", typeA.Languages)
	}
	if !reflect.DeepEqual(typeA.AptPackages, []string{"python3", "python3-numpy"}) {
		t.Fatalf("type-a apt packages = %v", typeA.AptPackages)
	}

	ci, err := catalog.CILanguageImages()
	if err != nil {
		t.Fatalf("CILanguageImages returned error: %v", err)
	}
	if len(ci) != 3 {
		t.Fatalf("expected 3 CI images, got %d", len(ci))
	}

	if ci[0].Name != "ci-java" || !reflect.DeepEqual(ci[0].Languages, []string{"java"}) {
		t.Fatalf("ci[0] = %+v", ci[0])
	}
	if !reflect.DeepEqual(ci[0].AptPackages, []string{"default-jdk-headless"}) {
		t.Fatalf("ci[0] apt packages = %v", ci[0].AptPackages)
	}

	if ci[1].Name != "ci-plain" || !reflect.DeepEqual(ci[1].Languages, []string{"plain"}) {
		t.Fatalf("ci[1] = %+v", ci[1])
	}

	if ci[2].Name != "ci-python" || !reflect.DeepEqual(ci[2].SmokeCommand, []string{"python3", "-c", "import numpy; print(numpy.arange(3).sum())"}) {
		t.Fatalf("ci[2] = %+v", ci[2])
	}
}

func TestLoadCatalogRejectsUnknownLanguageReference(t *testing.T) {
	path := writeCatalogFixture(t, `
languages:
  python:
    install:
      apt: [python3]
profiles:
  type-a:
    base_image: debian:trixie-slim
    languages: [python, java]
`)

	if _, err := LoadCatalog(path); err == nil {
		t.Fatalf("expected unknown language validation error")
	}
}

func TestImageSpecDockerBuildUsesCatalogPackages(t *testing.T) {
	spec := ImageSpec{
		Name:         "type-a",
		BaseImage:    "debian:trixie-slim",
		Languages:    []string{"plain", "python"},
		AptPackages:  []string{"python3", "python3-numpy"},
		PipPackages:  []string{"requests"},
		NPMPackages:  []string{"typescript"},
		SmokeCommand: []string{"python3", "-c", "print('ok')"},
	}

	build := spec.DockerBuild("/workspace/aonohako", "ghcr.io/seo-rii/aonohako")
	if build.Tag != "ghcr.io/seo-rii/aonohako:type-a" {
		t.Fatalf("build tag = %q", build.Tag)
	}
	if build.File != "docker/runtime.Dockerfile" {
		t.Fatalf("docker file = %q", build.File)
	}
	if build.BuildArgs["RUNTIME_BASE"] != "debian:trixie-slim" {
		t.Fatalf("build args = %#v", build.BuildArgs)
	}
	if build.BuildArgs["APT_PACKAGES"] != "python3 python3-numpy" {
		t.Fatalf("apt args = %q", build.BuildArgs["APT_PACKAGES"])
	}
	if build.BuildArgs["PIP_PACKAGES"] != "requests" {
		t.Fatalf("pip args = %q", build.BuildArgs["PIP_PACKAGES"])
	}
	if build.BuildArgs["NPM_PACKAGES"] != "typescript" {
		t.Fatalf("npm args = %q", build.BuildArgs["NPM_PACKAGES"])
	}
	if build.BuildArgs["SMOKE_COMMAND"] != "python3\t-c\tprint('ok')" {
		t.Fatalf("smoke arg = %q", build.BuildArgs["SMOKE_COMMAND"])
	}
}

func TestImageSpecDockerBuildCarriesInstallScript(t *testing.T) {
	spec := ImageSpec{
		Name:          "type-z",
		BaseImage:     "debian:trixie-slim",
		Languages:     []string{"kotlin"},
		InstallScript: []string{"echo installing", "echo done"},
	}

	build := spec.DockerBuild("/workspace/aonohako", "ghcr.io/seo-rii/aonohako")
	if build.BuildArgs["INSTALL_SCRIPT"] != "echo installing\necho done" {
		t.Fatalf("install script arg = %q", build.BuildArgs["INSTALL_SCRIPT"])
	}
}

func TestSmokeScriptRunsSandboxSelftestBeforeLanguageSmoke(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "smoke_runtime.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "suite=image-permissions") {
		t.Fatalf("smoke_runtime.sh must default to the image-permissions selftest suite")
	}
	if !strings.Contains(body, "*,python,*)") {
		t.Fatalf("smoke_runtime.sh must upgrade to the full permissions selftest for python images")
	}
	if !strings.Contains(body, "aonohako-selftest \"${suite}\"") {
		t.Fatalf("smoke_runtime.sh must run the selected sandbox selftest before language smoke")
	}
}

func TestRuntimeEntrypointHardensScratchDirsBeforeExec(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "runtime_entrypoint.sh")
	root := t.TempDir()
	dirs := []string{
		filepath.Join(root, "tmp"),
		filepath.Join(root, "var-tmp"),
		filepath.Join(root, "dev-shm"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatalf("Chmod(%q): %v", dir, err)
		}
	}

	cmdText := "for dir in \"$@\"; do stat -c %a \"$dir\"; done"
	cmdArgs := []string{path, "sh", "-c", cmdText, "sh"}
	cmdArgs = append(cmdArgs, dirs...)
	cmd := exec.Command("/bin/sh", cmdArgs...)
	cmd.Env = append(os.Environ(), "AONOHAKO_SCRATCH_DIRS="+strings.Join(dirs, " "))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime_entrypoint.sh: %v\n%s", err, string(out))
	}

	if string(out) != "755\n755\n755\n" {
		t.Fatalf("runtime_entrypoint.sh must chmod scratch dirs before exec, got %q", string(out))
	}
}

func TestSmokeScriptPreservesMultilineSmokeCommands(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "smoke_runtime.sh")
	binDir := t.TempDir()
	selftestPath := filepath.Join(binDir, "aonohako-selftest")
	if err := os.WriteFile(selftestPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(%q): %v", selftestPath, err)
	}

	cmd := exec.Command("/bin/bash", path)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"AONOHAKO_SMOKE_COMMAND=bash\t-lc\tprintf 'first\\n'\nprintf 'second\\n'",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke_runtime.sh: %v\n%s", err, string(out))
	}
	if string(out) != "first\nsecond\n" {
		t.Fatalf("smoke_runtime.sh must preserve multiline command bodies, got %q", string(out))
	}
}

func TestToolchainVersionReportScriptCoversNewRuntimesAndPythonLibraries(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "report_toolchain_versions.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	for _, marker := range []string{
		"echo \"- Languages: \\`${AONOHAKO_LANGUAGES}\\`\"",
		`if ! command -v "$1" >/dev/null 2>&1; then`,
		`report_python_pkg "Aheui" "aheui"`,
		`report_python_module "JungolRobot" "jungol_robot"`,
		`report_python_module "robot_judge" "robot_judge"`,
		`report "GNU as" as --version`,
		`report "NASM" nasm -v`,
		`report "PyPy" pypy3 --version`,
		`report "Free Pascal" fpc -iV`,
		`report "Nim" nim --version`,
		`report "Clojure" clojure -e "(println (clojure-version))"`,
		`report "Racket" racket --version`,
		`report "Ada" gnatmake -v`,
		`report "Dart" dart --version`,
		`report_python_pkg "NumPy" "numpy"`,
		`report_python_pkg "Torch" "torch"`,
		`report_python_pkg "JAXLIB" "jaxlib"`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("report_toolchain_versions.sh must contain %q", marker)
		}
	}
}

func TestWorkflowPublishesConsolidatedToolchainSummary(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	if !strings.Contains(body, "scripts/report_toolchain_versions.sh") {
		t.Fatalf("ci workflow must invoke report_toolchain_versions.sh")
	}
	if !strings.Contains(body, "toolchain-summary:") {
		t.Fatalf("ci workflow must define a dedicated toolchain summary job")
	}
	if !strings.Contains(body, "go run ./cmd/runtime-matrix -mode production") {
		t.Fatalf("ci workflow must iterate production profiles for one consolidated summary")
	}
	if !strings.Contains(body, "aonohako-ci-prod:${name}") {
		t.Fatalf("ci workflow must report production-profile images in the consolidated summary")
	}
	if !strings.Contains(body, "AONOHAKO_LANGUAGES=\"${languages}\"") {
		t.Fatalf("ci workflow must include the language list in the consolidated summary")
	}
	if !strings.Contains(body, ">> \"$GITHUB_STEP_SUMMARY\"") {
		t.Fatalf("ci workflow must publish toolchain versions to the job summary")
	}
}
