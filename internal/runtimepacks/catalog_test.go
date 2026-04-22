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

func TestRuntimeEntrypointPassesThroughToRequestedCommand(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "runtime_entrypoint.sh")
	cmd := exec.Command("/bin/sh", path, "sh", "-c", "printf ok")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime_entrypoint.sh: %v\n%s", err, string(out))
	}
	if string(out) != "ok" {
		t.Fatalf("runtime_entrypoint.sh must exec the requested command without mutation, got %q", string(out))
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
		"declare -A enabled_languages=()",
		"declare -A reported_tools=()",
		`while IFS= read -r raw_language; do`,
		`if ! command -v "$1" >/dev/null 2>&1; then`,
		`output="$(printf "%s" "${output}" | tr -d '\r' | sed -n '/./{s/|/\\|/g;p;q;}')"`,
		`if has_language "aheui"; then`,
		`if has_language "python"; then`,
		`if has_language "ada"; then`,
		`if has_language "swift"; then`,
		`report_once "Python" python3 --version`,
		`report_once "Ada" gnatmake -v`,
		`report_python_pkg_once "Aheui" "aheui"`,
		`report_python_pkg_once "NumPy" "numpy"`,
		`report_python_pkg_once "Torch" "torch"`,
		`report_python_pkg_once "JAXLIB" "jaxlib"`,
		`report_python_module_once "JungolRobot" "jungol_robot"`,
		`report_python_module_once "robot_judge" "robot_judge"`,
		`report_once "GNU as" as --version`,
		`report_once "NASM" nasm -v`,
		`report_once "PyPy" pypy3 --version`,
		`report_once "Free Pascal" fpc -iV`,
		`report_once "Nim" nim --version`,
		`report_once "Clojure" clojure -e "(println (clojure-version))"`,
		`report_once "Racket" racket --version`,
		`report_once "Dart" dart --version`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("report_toolchain_versions.sh must contain %q", marker)
		}
	}
}

func TestAggregateToolchainSummariesScriptMergesConsistentVersions(t *testing.T) {
	root := t.TempDir()
	for _, fixture := range []struct {
		profile string
		body    string
	}{
		{
			profile: "type-a",
			body:    "## Runtime Toolchain Versions\n\n- Image: `a`\n\n| Tool | Version |\n| --- | --- |\n| GCC | `14.2.0` |\n| Python | `3.13.3` |\n",
		},
		{
			profile: "type-b",
			body:    "## Runtime Toolchain Versions\n\n- Image: `b`\n\n| Tool | Version |\n| --- | --- |\n| GCC | `14.2.0` |\n| Swift | `6.1` |\n",
		},
	} {
		dir := filepath.Join(root, "toolchain-profile-"+fixture.profile)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(fixture.body), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", dir, err)
		}
	}

	path := filepath.Join("..", "..", "scripts", "aggregate_toolchain_summaries.py")
	cmd := exec.Command("python3", path, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("aggregate_toolchain_summaries.py: %v\n%s", err, string(out))
	}

	body := string(out)
	for _, want := range []string{
		"## Runtime Toolchain Versions",
		"- Profiles: `type-a`, `type-b`",
		"| GCC | `14.2.0` | `type-a`, `type-b` |",
		"| Python | `3.13.3` | `type-a` |",
		"| Swift | `6.1` | `type-b` |",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("aggregate summary missing %q in %q", want, body)
		}
	}
	if strings.Contains(body, "### Version Differences") {
		t.Fatalf("aggregate summary should omit conflict table when versions match, got %q", body)
	}
}

func TestAggregateToolchainSummariesScriptSeparatesVersionConflicts(t *testing.T) {
	root := t.TempDir()
	for _, fixture := range []struct {
		profile string
		body    string
	}{
		{
			profile: "type-a",
			body:    "## Runtime Toolchain Versions\n\n- Image: `a`\n\n| Tool | Version |\n| --- | --- |\n| GCC | `14.2.0` |\n| Python | `3.13.3` |\n",
		},
		{
			profile: "type-b",
			body:    "## Runtime Toolchain Versions\n\n- Image: `b`\n\n| Tool | Version |\n| --- | --- |\n| Python | `3.12.9` |\n| Swift | `6.1` |\n",
		},
		{
			profile: "type-c",
			body:    "## Runtime Toolchain Versions\n\n- Image: `c`\n\n| Tool | Version |\n| --- | --- |\n| GCC | `14.2.0` |\n| Python | `3.13.3` |\n",
		},
	} {
		dir := filepath.Join(root, "toolchain-profile-"+fixture.profile)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(fixture.body), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", dir, err)
		}
	}

	path := filepath.Join("..", "..", "scripts", "aggregate_toolchain_summaries.py")
	cmd := exec.Command("python3", path, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("aggregate_toolchain_summaries.py: %v\n%s", err, string(out))
	}

	body := string(out)
	for _, want := range []string{
		"| GCC | `14.2.0` | `type-a`, `type-c` |",
		"| Swift | `6.1` | `type-b` |",
		"### Version Differences",
		"| Python | `3.12.9` | `type-b` |",
		"| Python | `3.13.3` | `type-a`, `type-c` |",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("aggregate summary missing %q in %q", want, body)
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
	if !strings.Contains(body, "toolchain-profile:") {
		t.Fatalf("ci workflow must define a dedicated production-profile artifact job")
	}
	if !strings.Contains(body, "toolchain-summary:") {
		t.Fatalf("ci workflow must define a dedicated toolchain summary job")
	}
	summarySection := body[strings.Index(body, "toolchain-summary:"):]
	if idx := strings.Index(summarySection, "\n  mixin-smoke:"); idx >= 0 {
		summarySection = summarySection[:idx]
	}
	if !strings.Contains(body, "production_matrix") {
		t.Fatalf("ci workflow must publish a production profile matrix")
	}
	if !strings.Contains(body, "aonohako-ci-prod:${{ matrix.name }}") {
		t.Fatalf("ci workflow must build production-profile images in the profile matrix")
	}
	if !strings.Contains(body, "AONOHAKO_LANGUAGES=\"${{ matrix.languages }}\"") {
		t.Fatalf("ci workflow must include the language list in the profile summaries")
	}
	if !strings.Contains(body, "actions/upload-artifact@v4") || !strings.Contains(body, "actions/download-artifact@v4") {
		t.Fatalf("ci workflow must aggregate toolchain summary data through artifacts")
	}
	if !strings.Contains(summarySection, "    if: ${{ always() }}") {
		t.Fatalf("toolchain summary job must remain always-on")
	}
	if !strings.Contains(summarySection, "      - uses: actions/checkout@v4") {
		t.Fatalf("toolchain summary job must check out the repository before running aggregation scripts")
	}
	if !strings.Contains(body, `docker save "aonohako-ci-prod:${{ matrix.name }}"`) {
		t.Fatalf("ci workflow must export production-profile images into artifact files")
	}
	if !strings.Contains(body, "toolchain-summary-bundle") {
		t.Fatalf("ci workflow must publish a final bundle artifact for toolchain reports")
	}
	if !strings.Contains(summarySection, "scripts/aggregate_toolchain_summaries.py toolchain-artifacts") {
		t.Fatalf("ci workflow must aggregate per-profile summaries into the job summary")
	}
	if !strings.Contains(summarySection, `summary="$(python3 scripts/aggregate_toolchain_summaries.py toolchain-artifacts)"`) {
		t.Fatalf("ci workflow must fail closed if summary aggregation fails")
	}
}

func TestWorkflowSandboxJobCoversRootBackedWorkspacePermissionChecks(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}

	body := string(data)
	for _, marker := range []string{
		"TestMaterializeFilesKeepsNestedPathsReadableAndWritableToSandboxUser",
		"TestMaterializeFilesBuildsReadableSubmissionJarForSandboxUser",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("sandbox workflow must cover %q", marker)
		}
	}
}
