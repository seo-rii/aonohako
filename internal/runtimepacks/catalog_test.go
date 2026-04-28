package runtimepacks

import (
	"crypto/sha256"
	"fmt"
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
    install:
      apt: [curl]
      script: ["echo profile-a"]
    languages: [plain, python]
  type-b:
    base_image: debian:trixie-slim
    install:
      apt: [wget]
      script: ["echo profile-b"]
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
	if !reflect.DeepEqual(typeA.AptPackages, []string{"curl", "python3", "python3-numpy"}) {
		t.Fatalf("type-a apt packages = %v", typeA.AptPackages)
	}
	if !reflect.DeepEqual(typeA.InstallScript, []string{"echo profile-a"}) {
		t.Fatalf("type-a install script = %v", typeA.InstallScript)
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
	if !reflect.DeepEqual(ci[0].AptPackages, []string{"default-jdk-headless", "wget"}) {
		t.Fatalf("ci[0] apt packages = %v", ci[0].AptPackages)
	}
	if !reflect.DeepEqual(ci[0].InstallScript, []string{"echo profile-b"}) {
		t.Fatalf("ci[0] install script = %v", ci[0].InstallScript)
	}

	if ci[1].Name != "ci-plain" || !reflect.DeepEqual(ci[1].Languages, []string{"plain"}) {
		t.Fatalf("ci[1] = %+v", ci[1])
	}
	if !reflect.DeepEqual(ci[1].AptPackages, []string{"curl"}) {
		t.Fatalf("ci[1] apt packages = %v", ci[1].AptPackages)
	}
	if !reflect.DeepEqual(ci[1].InstallScript, []string{"echo profile-a"}) {
		t.Fatalf("ci[1] install script = %v", ci[1].InstallScript)
	}

	if ci[2].Name != "ci-python" || !reflect.DeepEqual(ci[2].SmokeCommand, []string{"python3", "-c", "import numpy; print(numpy.arange(3).sum())"}) {
		t.Fatalf("ci[2] = %+v", ci[2])
	}
	if !reflect.DeepEqual(ci[2].AptPackages, []string{"curl", "python3", "python3-numpy"}) {
		t.Fatalf("ci[2] apt packages = %v", ci[2].AptPackages)
	}
	if !reflect.DeepEqual(ci[2].InstallScript, []string{"echo profile-a"}) {
		t.Fatalf("ci[2] install script = %v", ci[2].InstallScript)
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
	if !strings.Contains(body, "aonohako-selftest compile-execute") {
		t.Fatalf("smoke_runtime.sh must run compile-execute smoke through aonohako before legacy language commands")
	}
	if !strings.Contains(body, "aonohako-selftest runtime-memory") {
		t.Fatalf("smoke_runtime.sh must run runtime memory stress through aonohako before legacy language commands")
	}
	if !strings.Contains(body, "export AONOHAKO_EXECUTION_MODE=local-root") || !strings.Contains(body, `work_root="${AONOHAKO_SMOKE_WORK_ROOT:-/work}"`) || !strings.Contains(body, `export AONOHAKO_WORK_ROOT="${work_root}"`) {
		t.Fatalf("smoke_runtime.sh must force a dedicated local-root work root for compile/execute smoke")
	}
	if !strings.Contains(body, `chmod 0755 "${work_root}"`) {
		t.Fatalf("smoke_runtime.sh must keep the smoke work root traversable for sandboxed helpers")
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
	workRoot := filepath.Join(t.TempDir(), "work")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"AONOHAKO_SMOKE_WORK_ROOT="+workRoot,
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
		`report_once "GCC" gcc -dumpfullversion -dumpversion`,
		`report_once "G++" g++ -dumpfullversion -dumpversion`,
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

func TestVerifyToolchainArtifactsScriptRequiresCompleteProfileArtifacts(t *testing.T) {
	root := t.TempDir()
	profile := "type-a"
	dir := filepath.Join(root, "toolchain-profile-"+profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	archivePath := filepath.Join(dir, profile+".docker.tar.gz")
	archiveBody := []byte("docker archive fixture")
	archiveDigest := fmt.Sprintf("%x", sha256.Sum256(archiveBody))
	for path, body := range map[string][]byte{
		filepath.Join(dir, "summary.md"):              []byte("## Runtime Toolchain Versions\n"),
		filepath.Join(dir, profile+".sbom.spdx.json"): []byte(`{"spdxVersion":"SPDX-2.3"}`),
		filepath.Join(dir, profile+".grype.json"):     []byte(`{"matches":[]}`),
		archivePath: archiveBody,
		filepath.Join(dir, profile+".docker.tar.gz.sha256"): []byte(archiveDigest + "  " + archivePath + "\n"),
		filepath.Join(root, "SHA256SUMS"):                   []byte(archiveDigest + "  " + archivePath + "\n"),
	} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}

	path := filepath.Join("..", "..", "scripts", "verify_toolchain_artifacts.py")
	cmd := exec.Command("python3", path, root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("verify_toolchain_artifacts.py: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "verified 1 toolchain profile artifact set(s)") {
		t.Fatalf("verification output missing success line: %q", string(out))
	}

	if err := os.Remove(filepath.Join(dir, profile+".grype.json")); err != nil {
		t.Fatalf("Remove grype fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("verify_toolchain_artifacts.py unexpectedly succeeded after removing grype report: %s", string(out))
	}
	if !strings.Contains(string(out), "missing") || !strings.Contains(string(out), profile+".grype.json") {
		t.Fatalf("verification failure did not explain missing grype report: %q", string(out))
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
	if !strings.Contains(body, "image-sbom:") {
		t.Fatalf("ci workflow must define a dedicated runtime image SBOM job")
	}
	if !strings.Contains(body, "scripts/install_anchore_tool.sh syft v1.42.4") || !strings.Contains(body, "-o spdx-json=sbom-ci-python.spdx.json") {
		t.Fatalf("ci workflow must generate SBOMs with a retryable pinned Syft install")
	}
	if !strings.Contains(body, "sbom-ci-python.spdx.json") {
		t.Fatalf("ci workflow must publish a named SBOM artifact for the sandbox runtime image")
	}
	if !strings.Contains(body, "scripts/install_anchore_tool.sh grype v0.111.0") || !strings.Contains(body, `"${RUNNER_TEMP}/anchore-bin/grype" "aonohako-sbom:ci-python" -o json > grype-ci-python.json`) {
		t.Fatalf("ci workflow must scan the sandbox runtime image with a retryable pinned Grype install")
	}
	if !strings.Contains(body, `printf '{"error":"grype scan failed"}\n' > grype-ci-python.json`) || !strings.Contains(body, "grype-ci-python.json") {
		t.Fatalf("ci workflow must publish a non-blocking Grype report artifact for the sandbox runtime image")
	}
	summarySection := body[strings.Index(body, "toolchain-summary:"):]
	if idx := strings.Index(summarySection, "\n  mixin-smoke:"); idx >= 0 {
		summarySection = summarySection[:idx]
	}
	profileSection := body[strings.Index(body, "toolchain-profile:"):]
	if idx := strings.Index(profileSection, "\n  toolchain-summary:"); idx >= 0 {
		profileSection = profileSection[:idx]
	}
	if !strings.Contains(body, "production_matrix") {
		t.Fatalf("ci workflow must publish a production profile matrix")
	}
	if !strings.Contains(body, "FORCE_JAVASCRIPT_ACTIONS_TO_NODE24: true") {
		t.Fatalf("ci workflow must force JavaScript actions onto Node 24 to avoid runner deprecation noise")
	}
	if !strings.Contains(body, "GOCACHE: ${{ github.workspace }}/.cache/go-build") {
		t.Fatalf("ci workflow must place GOCACHE under the workspace so setup-go cache restores into an empty per-job directory")
	}
	if !strings.Contains(body, "GOMODCACHE: ${{ github.workspace }}/.cache/go-mod") {
		t.Fatalf("ci workflow must place GOMODCACHE under the workspace so setup-go cache restores without colliding with preexisting module files")
	}
	if !strings.Contains(body, "group: ci-${{ github.workflow }}-${{ github.ref }}") || !strings.Contains(body, "cancel-in-progress: true") {
		t.Fatalf("ci workflow must cancel superseded same-ref runs")
	}
	if !strings.Contains(body, "aonohako-ci-prod:${{ matrix.name }}") {
		t.Fatalf("ci workflow must build production-profile images in the profile matrix")
	}
	if !strings.Contains(body, "docker builder prune -af") || !strings.Contains(body, "docker image prune -f") {
		t.Fatalf("ci workflow must prune build cache before production-profile SBOM scans to avoid daemon-export disk exhaustion")
	}
	if !strings.Contains(body, `rm -rf "${GOCACHE}" "${GOMODCACHE}" /tmp/stereoscope-*`) {
		t.Fatalf("ci workflow must clean Go caches and stale stereoscope temp files before production-profile exports")
	}
	if !strings.Contains(body, `printf '{"error":"syft scan failed","profile":"%s"}\n'`) {
		t.Fatalf("ci workflow must keep production-profile Syft artifacts non-blocking under runner disk pressure")
	}
	archiveIdx := strings.Index(profileSection, `docker save "aonohako-ci-prod:${{ matrix.name }}"`)
	syftIdx := strings.Index(profileSection, "scripts/install_anchore_tool.sh syft v1.42.4")
	if archiveIdx < 0 || syftIdx < 0 || archiveIdx > syftIdx {
		t.Fatalf("ci workflow must create required production-profile archives before best-effort scanner exports")
	}
	if !strings.Contains(profileSection, `"${HOME}/.cache/syft"`) || !strings.Contains(profileSection, `"${HOME}/.cache/grype"`) {
		t.Fatalf("ci workflow must clean scanner caches after production-profile scans")
	}
	if !strings.Contains(body, "AONOHAKO_LANGUAGES=\"${{ matrix.languages }}\"") {
		t.Fatalf("ci workflow must include the language list in the profile summaries")
	}
	if !strings.Contains(body, "actions/upload-artifact@v7") || !strings.Contains(body, "actions/download-artifact@v8") {
		t.Fatalf("ci workflow must aggregate toolchain summary data through artifacts")
	}
	if !strings.Contains(summarySection, "    if: ${{ always() }}") {
		t.Fatalf("toolchain summary job must remain always-on")
	}
	if !strings.Contains(summarySection, "      - uses: actions/checkout@v6") {
		t.Fatalf("toolchain summary job must check out the repository before running aggregation scripts")
	}
	if !strings.Contains(body, `docker save "aonohako-ci-prod:${{ matrix.name }}"`) {
		t.Fatalf("ci workflow must export production-profile images into artifact files")
	}
	if !strings.Contains(body, `sha256sum "toolchain-artifacts/${{ matrix.name }}/${{ matrix.name }}.docker.tar.gz"`) || !strings.Contains(body, "toolchain-artifacts/SHA256SUMS") {
		t.Fatalf("ci workflow must publish SHA256 digest metadata for production-profile image artifacts")
	}
	if !strings.Contains(summarySection, "python3 scripts/verify_toolchain_artifacts.py toolchain-artifacts") {
		t.Fatalf("ci workflow must fail closed when production-profile artifacts are incomplete or digest mismatched")
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
	if !strings.Contains(body, `docker run --rm "aonohako-ci-mix:type-i" aonohako-smoke`) {
		t.Fatalf("mixin smoke must run through aonohako-smoke so CI exercises compile and execute sequentially")
	}
}

func TestWorkflowSandboxJobCoversRootBackedWorkspacePermissionChecks(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	suitePath := filepath.Join("..", "execute", "security_ci_test.go")
	suiteData, err := os.ReadFile(suitePath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", suitePath, err)
	}

	body := string(data)
	for _, marker := range []string{
		`chmod 0755 /work`,
		"TestSandboxSecurityRegressionSuite",
		"TestCompileSandboxSecurityRegressionSuite",
		"aonohako-selftest compile-security",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("sandbox workflow must cover %q", marker)
		}
	}
	suiteBody := string(suiteData)
	for _, marker := range []string{
		"TestMaterializeFilesKeepsNestedPathsReadableAndWritableToSandboxUser",
		"TestMaterializeFilesBuildsReadableSubmissionJarForSandboxUser",
		"TestRunBlocksUnixSocketConnectWhenNetworkDisabled",
		"TestRunBlocksUnixDatagramSendWhenNetworkDisabled",
		"TestRunBlocksUnixDatagramSendToAccessibleSocketWhenNetworkDisabled",
		"TestRunSPJUsesCleanWorkspaceAndReadableFiles",
	} {
		if !strings.Contains(suiteBody, marker) {
			t.Fatalf("sandbox security suite must cover %q", marker)
		}
	}

	compileSuitePath := filepath.Join("..", "compile", "security_ci_test.go")
	compileSuiteData, err := os.ReadFile(compileSuitePath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", compileSuitePath, err)
	}
	compileSuiteBody := string(compileSuiteData)
	for _, marker := range []string{
		"TestRunPythonCompileDoesNotExecuteSitecustomize",
		"TestRunSandboxedCommandPreventsRemovingOrReplacingSubmittedCompileSources",
		"TestRunCommandCannotReadOrWriteRootOwnedHostPaths",
		"TestRunCommandDoesNotLeakInheritedFileDescriptors",
	} {
		if !strings.Contains(compileSuiteBody, marker) {
			t.Fatalf("compile sandbox security suite must cover %q", marker)
		}
	}
}
