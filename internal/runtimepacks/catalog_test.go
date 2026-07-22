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
      sandbox_tools: [node, git]
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
      sandbox_tools: [git]
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
	if !reflect.DeepEqual(typeA.SandboxTools, []string{"git", "node"}) {
		t.Fatalf("type-a sandbox tools = %v", typeA.SandboxTools)
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
	if !reflect.DeepEqual(ci[1].SandboxTools, []string{"git"}) {
		t.Fatalf("ci[1] sandbox tools = %v", ci[1].SandboxTools)
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
	if !reflect.DeepEqual(ci[2].SandboxTools, []string{"git", "node"}) {
		t.Fatalf("ci[2] sandbox tools = %v", ci[2].SandboxTools)
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

func TestLoadCatalogRejectsUnsafeNamesAndDuplicateProfileLanguages(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unsafe language name",
			body: `
languages:
  'bad"; echo injected; #': {}
profiles: {}
`,
			want: "language name",
		},
		{
			name: "docker-unsafe language name",
			body: `
languages:
  c++: {}
profiles: {}
`,
			want: "language name",
		},
		{
			name: "language name too long for prefixed docker tag",
			body: fmt.Sprintf(`
languages:
  %s: {}
profiles: {}
`, strings.Repeat("a", maxCILanguageNameLength+1)),
			want: "language name",
		},
		{
			name: "unsafe profile name",
			body: `
languages:
  plain: {}
profiles:
  'bad"; echo injected; #':
    base_image: debian:trixie-slim
    languages: [plain]
`,
			want: "profile name",
		},
		{
			name: "profile name too long for docker tag",
			body: fmt.Sprintf(`
languages:
  plain: {}
profiles:
  %s:
    base_image: debian:trixie-slim
    languages: [plain]
`, strings.Repeat("a", maxDockerTagLength+1)),
			want: "profile name",
		},
		{
			name: "duplicate profile language",
			body: `
languages:
  plain: {}
profiles:
  type-a:
    base_image: debian:trixie-slim
    languages: [plain, plain]
`,
			want: "duplicate language plain",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadCatalog(writeCatalogFixture(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadCatalog error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBuildImagePreservesRepeatedInstallScriptCommands(t *testing.T) {
	path := writeCatalogFixture(t, `
languages:
  mercury:
    install:
      script:
        - echo 'deb http://example.invalid/deb trixie main' > /etc/apt/sources.list.d/example.list
        - apt-get update
        - apt-get install -y example-package
    smoke:
      command: ["mercury", "--version"]
profiles:
  type-a:
    base_image: debian:trixie-slim
    install:
      script:
        - apt-get update
        - apt-get install -y base-package
    languages: [mercury]
`)

	catalog, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	production, err := catalog.ProductionImages()
	if err != nil {
		t.Fatalf("ProductionImages returned error: %v", err)
	}
	want := []string{
		"apt-get update",
		"apt-get install -y base-package",
		"echo 'deb http://example.invalid/deb trixie main' > /etc/apt/sources.list.d/example.list",
		"apt-get update",
		"apt-get install -y example-package",
	}
	if !reflect.DeepEqual(production[0].InstallScript, want) {
		t.Fatalf("install script = %v, want %v", production[0].InstallScript, want)
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
		SandboxTools: []string{"git", "node"},
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
	if build.BuildArgs["SANDBOX_TOOLS"] != "git node" {
		t.Fatalf("sandbox tool args = %q", build.BuildArgs["SANDBOX_TOOLS"])
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

func TestRuntimeEntrypointTightensCloudRunWorkRoot(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "runtime_entrypoint.sh")
	workRoot := t.TempDir()
	if err := os.Chmod(workRoot, 0o777); err != nil {
		t.Fatalf("Chmod(%q): %v", workRoot, err)
	}

	cmd := exec.Command("/bin/sh", path, "sh", "-c", "printf ok")
	cmd.Env = append(os.Environ(),
		"AONOHAKO_DEPLOYMENT_TARGET=cloudrun",
		"AONOHAKO_WORK_ROOT="+workRoot,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime_entrypoint.sh: %v\n%s", err, string(out))
	}
	if string(out) != "ok" {
		t.Fatalf("runtime_entrypoint.sh must exec the requested command, got %q", string(out))
	}
	info, err := os.Stat(workRoot)
	if err != nil {
		t.Fatalf("Stat(%q): %v", workRoot, err)
	}
	if got := info.Mode().Perm(); got != 0o711 {
		t.Fatalf("cloudrun work root mode = %03o, want 711", got)
	}
}

func TestRuntimeEntrypointLeavesSelfHostedWorkRootModeAlone(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "runtime_entrypoint.sh")
	workRoot := t.TempDir()
	if err := os.Chmod(workRoot, 0o777); err != nil {
		t.Fatalf("Chmod(%q): %v", workRoot, err)
	}

	cmd := exec.Command("/bin/sh", path, "sh", "-c", "printf ok")
	cmd.Env = append(os.Environ(),
		"AONOHAKO_DEPLOYMENT_TARGET=selfhosted",
		"AONOHAKO_WORK_ROOT="+workRoot,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime_entrypoint.sh: %v\n%s", err, string(out))
	}
	if string(out) != "ok" {
		t.Fatalf("runtime_entrypoint.sh must exec the requested command, got %q", string(out))
	}
	info, err := os.Stat(workRoot)
	if err != nil {
		t.Fatalf("Stat(%q): %v", workRoot, err)
	}
	if got := info.Mode().Perm(); got != 0o777 {
		t.Fatalf("selfhosted work root mode = %03o, want 777", got)
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
		"echo \"- Image ID: \\`${AONOHAKO_IMAGE_ID}\\`\"",
		`DOCKER_RUN_ARGS+=(--env "AONOHAKO_LANGUAGES=${AONOHAKO_LANGUAGES}")`,
		"declare -A enabled_languages=()",
		"declare -A reported_tools=()",
		`while IFS= read -r raw_language; do`,
		`if ! command -v "$1" >/dev/null 2>&1; then`,
		`output="<not installed>"`,
		`output="<package probe failed>"`,
		`if output="$("$@" </dev/null 2>&1)"; then`,
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
		`printf 'puts [info patchlevel]\n' | tclsh`,
		`report_once "GnuCOBOL" cobc --version`,
		`report_once "Cython" cython3 --version`,
		`report_once "Haxe" haxe --version`,
		`report_once "CoffeeScript" coffee --version`,
		`report_once "Raku" raku --version`,
		`report_once "MLton" mlton`,
		`report_once "Clang" clang --version`,
		`report_once "Clang++" clang++ --version`,
		`report_once "FreeBASIC" fbc -version`,
		`report_once "GNU sed" sed --version`,
		`report_once "bc" bc --version`,
		`report_once "Gforth" gforth --version`,
		`report_once "Dafny" env DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1 DOTNET_PROCESSOR_COUNT=1 COMPlus_ThreadPool_ForceMinWorkerThreads=1 dafny --version`,
		`report_once "TLA+ TLC" bash -c 'java -cp /usr/local/lib/aonohako/tla2tools.jar tlc2.TLC -version 2>&1 | grep -m1 "^TLC2 Version "'`,
		`echo "## Runtime Compile Options"`,
		`report_compile_option "java" "javac --release 11 -encoding UTF-8"`,
		`report_compile_option "kotlin-jvm" "kotlinc -J-Xms64m -J-Xmx<compiler cap> -J-Xss1m -J-XX:+UseSerialGC -jvm-target 1.8 -include-runtime -d <target>.jar; optional javac --release 8 plus jar uf"`,
		`report_compile_option "typescript" "tsc --module commonjs --target es2019 --sourceMap --outDir dist"`,
		`report_compile_option "rust" "rustc --edition 2018 -O --cfg ONLINE_JUDGE -o <target>"`,
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("report_toolchain_versions.sh must contain %q", marker)
		}
	}
}

func TestToolchainVersionReportKeepsProfileScopeAndCompletesAllProbes(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "report_toolchain_versions.sh")
	binDir := t.TempDir()

	dockerScript := `#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = image ]; then
    exit 0
fi
shift
while [ "$#" -gt 0 ]; do
    case "$1" in
        --rm|-i)
            shift
            ;;
        --env)
            export "$2"
            shift 2
            ;;
        --entrypoint)
            shift 2
            ;;
        *)
            shift
            ;;
    esac
done
exec bash
`
	if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(dockerScript), 0o755); err != nil {
		t.Fatalf("WriteFile fake docker: %v", err)
	}

	dafnyScript := `#!/usr/bin/env bash
set -eu
[ "${DOTNET_SYSTEM_GLOBALIZATION_INVARIANT:-}" = 1 ]
[ "${DOTNET_PROCESSOR_COUNT:-}" = 1 ]
[ "${COMPlus_ThreadPool_ForceMinWorkerThreads:-}" = 1 ]
printf '%s\n' '4.11.0+test'
`
	if err := os.WriteFile(filepath.Join(binDir, "dafny"), []byte(dafnyScript), 0o755); err != nil {
		t.Fatalf("WriteFile fake dafny: %v", err)
	}

	javaScript := `#!/usr/bin/env bash
if [ "${1:-}" = -version ]; then
    printf '%s\n' 'openjdk version "21-test"' >&2
    exit 0
fi
printf '%s\n' 'TLC2 Version 2.19 of 08 August 2024 (rev: test)'
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "java"), []byte(javaScript), 0o755); err != nil {
		t.Fatalf("WriteFile fake java: %v", err)
	}

	erlangScript := `#!/usr/bin/env bash
while IFS= read -r _; do
    :
done
printf '%s\n' 27
`
	if err := os.WriteFile(filepath.Join(binDir, "erl"), []byte(erlangScript), 0o755); err != nil {
		t.Fatalf("WriteFile fake erl: %v", err)
	}

	mltonScript := `#!/usr/bin/env bash
set -eu
[ "$#" -eq 0 ]
printf '%s\n' 'MLton 20241230'
`
	if err := os.WriteFile(filepath.Join(binDir, "mlton"), []byte(mltonScript), 0o755); err != nil {
		t.Fatalf("WriteFile fake mlton: %v", err)
	}

	cmd := exec.Command("bash", path, "test:image")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"AONOHAKO_LANGUAGES=erlang,sml,dafny,tla",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("report_toolchain_versions.sh: %v\n%s", err, string(out))
	}

	body := string(out)
	for _, want := range []string{
		"- Languages: `erlang,sml,dafny,tla`",
		"| Erlang | `27` |",
		"| MLton | `MLton 20241230` |",
		"| Java runtime | `openjdk version \"21-test\"` |",
		"| Dafny | `4.11.0+test` |",
		"| TLA+ TLC | `TLC2 Version 2.19 of 08 August 2024 (rev: test)` |",
		"## Runtime Compile Options",
		"| `erlang` | `erlc` |",
		"| `sml` | `mlton -output <target>` |",
		"| `dafny` | `dafny verify --cores 1` |",
		"| `tla` | `pass-through .tla/.cfg artifacts` |",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("toolchain report missing %q in:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"| Java compiler |", "| GNU sed |", "<command failed>"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("toolchain report unexpectedly contains %q in:\n%s", unwanted, body)
		}
	}

	for _, tool := range []string{"bash", "sed", "tr"} {
		target, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("LookPath(%q): %v", tool, err)
		}
		if err := os.Symlink(target, filepath.Join(binDir, tool)); err != nil {
			t.Fatalf("Symlink(%q): %v", tool, err)
		}
	}
	cmd = exec.Command("bash", path, "test:image")
	cmd.Env = append(os.Environ(), "PATH="+binDir, "AONOHAKO_LANGUAGES=gdl")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("report_toolchain_versions.sh missing-probe run: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "| GNU Data Language | `<not installed>` |") {
		t.Fatalf("missing required tool was not reported explicitly:\n%s", string(out))
	}
	cmd = exec.Command("bash", path, "test:image")
	cmd.Env = append(os.Environ(), "PATH="+binDir, "AONOHAKO_LANGUAGES=tcl")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("report_toolchain_versions.sh missing Tcl run: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "| Tcl | `<not installed>` |") {
		t.Fatalf("missing Tcl runtime was not reported explicitly:\n%s", string(out))
	}
}

func TestToolchainVersionReportHasProbeForEveryCatalogLanguage(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	path := filepath.Join("..", "..", "scripts", "report_toolchain_versions.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	for language := range catalog.Languages {
		if !strings.Contains(body, `has_language "`+language+`"`) {
			t.Fatalf("toolchain report has no required probe branch for catalog language %q", language)
		}
		if !strings.Contains(body, `report_compile_option "`+language+`"`) {
			t.Fatalf("toolchain report has no compile-option row for catalog language %q", language)
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
			body:    "## Runtime Toolchain Versions\n\n- Image: `a`\n\n| Tool | Version |\n| --- | --- |\n| GCC | `14.2.0` |\n| Python | `3.13.3` |\n\n## Runtime Compile Options\n\n| Language | Compile options |\n| --- | --- |\n| `python` | `python3 -I -S -m compileall -b .` |\n",
		},
		{
			profile: "type-b",
			body:    "## Runtime Toolchain Versions\n\n- Image: `b`\n\n| Tool | Version |\n| --- | --- |\n| GCC | `14.2.0` |\n| Swift | `6.1` |\n\n## Runtime Compile Options\n\n| Language | Compile options |\n| --- | --- |\n| `java` | `javac --release 11 -encoding UTF-8` |\n",
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
		"## Runtime Compile Options",
		"| `java` | `javac --release 11 -encoding UTF-8` | `type-b` |",
		"| `python` | `python3 -I -S -m compileall -b .` | `type-a` |",
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
	profileSpec := profile + "=python"
	imageRef := "aonohako-ci-prod:" + profile
	imageID := "sha256:" + strings.Repeat("a", 64)
	dir := filepath.Join(root, "toolchain-profile-"+profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	archivePath := filepath.Join(dir, profile+".docker.tar.gz")
	archiveBody := []byte("docker archive fixture")
	archiveDigest := fmt.Sprintf("%x", sha256.Sum256(archiveBody))
	archiveRelative := filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".docker.tar.gz"))
	manifest := strings.Join([]string{
		"MANIFEST.txt",
		"SHA256SUMS",
		"SUMMARY.md",
		filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, "summary.md")),
		archiveRelative,
		archiveRelative + ".sha256",
		filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".grype.json")),
		filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".provenance.json")),
		filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".sbom.spdx.json")),
	}, "\n") + "\n"
	validProfileSummary := []byte(fmt.Sprintf("## Runtime Toolchain Versions\n\n- Image: `%s`\n- Image ID: `%s`\n- Languages: `python`\n\n| Tool | Version |\n| --- | --- |\n| Python | `3.13` |\n\n## Runtime Compile Options\n\n| Language | Compile options |\n| --- | --- |\n| `python` | `python3 -m compileall` |\n", imageRef, imageID))
	validSBOM := []byte(fmt.Sprintf(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":%q,"documentNamespace":"https://anchore.example/type-a","creationInfo":{"creators":["Organization: Anchore, Inc","Tool: syft-1.42.4"]},"packages":[{"name":"python","SPDXID":"SPDXRef-Package-python"}],"relationships":[{"spdxElementId":"SPDXRef-DOCUMENT","relatedSpdxElement":"SPDXRef-Package-python","relationshipType":"DESCRIBES"}]}`, imageRef))
	validGrype := []byte(fmt.Sprintf(`{"matches":[],"source":{"type":"image","target":{"userInput":%q,"imageID":%q}},"distro":{"name":"debian","version":"13"},"descriptor":{"name":"grype","version":"0.111.0"}}`, imageRef, imageID))
	validProvenance := []byte(fmt.Sprintf(`{"profile":%q,"image":%q,"image_id":%q,"artifacts":{"summary.md":"%x","sbom.spdx.json":"%x","grype.json":"%x"}}`, profile, imageRef, imageID, sha256.Sum256(validProfileSummary), sha256.Sum256(validSBOM), sha256.Sum256(validGrype)))
	validAggregateSummary := []byte("## Runtime Toolchain Versions\n\n- Profiles: `type-a`\n\n| Tool | Version | Profiles |\n| --- | --- | --- |\n| Python | `3.13` | `type-a` |\n\n## Runtime Compile Options\n\n| Language | Compile options | Profiles |\n| --- | --- | --- |\n| `python` | `python3 -m compileall` | `type-a` |\n")
	for path, body := range map[string][]byte{
		filepath.Join(dir, "summary.md"):               validProfileSummary,
		filepath.Join(dir, profile+".sbom.spdx.json"):  validSBOM,
		filepath.Join(dir, profile+".grype.json"):      validGrype,
		filepath.Join(dir, profile+".provenance.json"): validProvenance,
		archivePath: archiveBody,
		filepath.Join(dir, profile+".docker.tar.gz.sha256"): []byte(archiveDigest + "  " + archiveRelative + "\n"),
		filepath.Join(root, "SHA256SUMS"):                   []byte(archiveDigest + "  " + archiveRelative + "\n"),
		filepath.Join(root, "SUMMARY.md"):                   validAggregateSummary,
		filepath.Join(root, "MANIFEST.txt"):                 []byte(manifest),
	} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}

	path := filepath.Join("..", "..", "scripts", "verify_toolchain_artifacts.py")
	cmd := exec.Command("python3", path, root, profileSpec)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("verify_toolchain_artifacts.py: %v\n%s", err, string(out))
	}
	if !strings.Contains(string(out), "verified 1 toolchain profile artifact set(s)") {
		t.Fatalf("verification output missing success line: %q", string(out))
	}
	cmd = exec.Command("python3", path, root, profileSpec, "type-b=java")
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "profile inventory") {
		t.Fatalf("verifier unexpectedly accepted a missing expected profile: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".docker.tar.gz.sha256"), []byte(archiveDigest+"  "+archivePath+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile nonportable sidecar: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "must reference bundle path") {
		t.Fatalf("verifier unexpectedly accepted a nonportable sidecar path: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".docker.tar.gz.sha256"), []byte(archiveDigest+"  "+archiveRelative+"\n"), 0o644); err != nil {
		t.Fatalf("Restore archive sidecar: %v", err)
	}

	for _, path := range []string{
		archivePath,
		filepath.Join(dir, profile+".docker.tar.gz.sha256"),
	} {
		if err := os.Remove(path); err != nil {
			t.Fatalf("Remove archive fixture %q: %v", path, err)
		}
	}
	diagnosticRelative := filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".docker.tar.gz.error.json"))
	manifest = strings.Join([]string{
		"MANIFEST.txt",
		"SHA256SUMS",
		"SUMMARY.md",
		filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, "summary.md")),
		diagnosticRelative,
		filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".grype.json")),
		filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".provenance.json")),
		filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".sbom.spdx.json")),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "SHA256SUMS"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile empty bundle digest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "MANIFEST.txt"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile diagnostic manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".docker.tar.gz.error.json"), []byte(`{"skipped":"docker archive export skipped","profile":"type-a"}`), 0o644); err != nil {
		t.Fatalf("WriteFile archive error fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("verify_toolchain_artifacts.py should accept archive diagnostics: %v\n%s", err, string(out))
	}

	if err := os.WriteFile(filepath.Join(dir, profile+".docker.tar.gz.error.json"), []byte(`{"error":"docker archive export failed"}`), 0o644); err != nil {
		t.Fatalf("WriteFile invalid archive diagnostic: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "contains an archive error diagnostic") {
		t.Fatalf("verifier unexpectedly accepted invalid archive diagnostic: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".docker.tar.gz.error.json"), []byte(`{"skipped":"docker archive export skipped","profile":"type-a"}`), 0o644); err != nil {
		t.Fatalf("Restore archive diagnostic: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".sbom.spdx.json"), []byte(`{"spdxVersion":"SPDX-garbage","packages":[]}`), 0o644); err != nil {
		t.Fatalf("WriteFile invalid SPDX fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "required SPDX 2.3 document metadata") {
		t.Fatalf("verifier unexpectedly accepted malformed SPDX metadata: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".sbom.spdx.json"), validSBOM, 0o644); err != nil {
		t.Fatalf("Restore SPDX fixture: %v", err)
	}
	staleSyftSBOM := []byte(strings.Replace(string(validSBOM), "Tool: syft-1.42.4", "Tool: syft-0.1.0", 1))
	if err := os.WriteFile(filepath.Join(dir, profile+".sbom.spdx.json"), staleSyftSBOM, 0o644); err != nil {
		t.Fatalf("WriteFile stale Syft fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "pinned Syft 1.42.4") {
		t.Fatalf("verifier unexpectedly accepted stale Syft metadata: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".sbom.spdx.json"), validSBOM, 0o644); err != nil {
		t.Fatalf("Restore SPDX fixture: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, profile+".grype.json"), []byte(`{"error":"grype scan failed"}`), 0o644); err != nil {
		t.Fatalf("WriteFile scanner error fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("verify_toolchain_artifacts.py unexpectedly accepted a scanner error diagnostic: %s", string(out))
	}
	if !strings.Contains(string(out), "contains scanner error diagnostic") || !strings.Contains(string(out), profile+".grype.json") {
		t.Fatalf("verification failure did not explain scanner error diagnostic: %q", string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".grype.json"), validGrype, 0o644); err != nil {
		t.Fatalf("Restore grype fixture: %v", err)
	}
	mismatchedProvenance := []byte(strings.Replace(string(validProvenance), imageID, "sha256:"+strings.Repeat("b", 64), 1))
	if err := os.WriteFile(filepath.Join(dir, profile+".provenance.json"), mismatchedProvenance, 0o644); err != nil {
		t.Fatalf("WriteFile mismatched provenance fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "image ID does not match") {
		t.Fatalf("verifier unexpectedly accepted mismatched immutable provenance: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".provenance.json"), validProvenance, 0o644); err != nil {
		t.Fatalf("Restore provenance fixture: %v", err)
	}
	invalidMatchGrype := []byte(fmt.Sprintf(`{"matches":[null],"source":{"type":"image","target":{"userInput":%q}},"distro":{},"descriptor":{"name":"grype","version":"0.111.0"}}`, imageRef))
	if err := os.WriteFile(filepath.Join(dir, profile+".grype.json"), invalidMatchGrype, 0o644); err != nil {
		t.Fatalf("WriteFile invalid Grype match fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "invalid vulnerability match") {
		t.Fatalf("verifier unexpectedly accepted malformed Grype matches: %v\n%s", err, string(out))
	}
	staleSourceGrype := []byte(`{"matches":[],"source":{"type":"image","target":{"userInput":"aonohako-ci-prod:type-z"}},"distro":{},"descriptor":{"name":"grype","version":"0.111.0"}}`)
	if err := os.WriteFile(filepath.Join(dir, profile+".grype.json"), staleSourceGrype, 0o644); err != nil {
		t.Fatalf("WriteFile stale Grype source fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "source image must equal") {
		t.Fatalf("verifier unexpectedly accepted stale Grype source: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".grype.json"), validGrype, 0o644); err != nil {
		t.Fatalf("Restore grype fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".grype.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("WriteFile empty grype fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "missing the Grype matches array") {
		t.Fatalf("verifier unexpectedly accepted structurally empty Grype JSON: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, profile+".grype.json"), validGrype, 0o644); err != nil {
		t.Fatalf("Restore grype fixture: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte("## Runtime Toolchain Versions\n\n- Languages: `sml`\n\n| Tool | Version |\n| --- | --- |\n| MLton | `<command failed>` |\n\n## Runtime Compile Options\n\n| Language | Compile options |\n| --- | --- |\n| `sml` | `mlton` |\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed summary fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "failed version probe") {
		t.Fatalf("verifier unexpectedly accepted failed version probe: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), validProfileSummary, 0o644); err != nil {
		t.Fatalf("Restore summary fixture: %v", err)
	}
	staleImageSummary := []byte(strings.Replace(string(validProfileSummary), imageRef, "aonohako-ci-prod:type-z", 1))
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), staleImageSummary, 0o644); err != nil {
		t.Fatalf("WriteFile stale image summary fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "image must equal") {
		t.Fatalf("verifier unexpectedly accepted a stale summary image: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), validProfileSummary, 0o644); err != nil {
		t.Fatalf("Restore summary fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(fmt.Sprintf("## Runtime Toolchain Versions\n\n- Image: `%s`\n- Image ID: `%s`\n- Languages: `python`\n\n| Tool | Version |\n| --- | --- |\n| Python | `3.13` |\n\n## Runtime Compile Options\n\n| Language | Compile options |\n| --- | --- |\n", imageRef, imageID)), 0o644); err != nil {
		t.Fatalf("WriteFile incomplete language summary fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "compile-option") {
		t.Fatalf("verifier unexpectedly accepted a missing language probe: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), validProfileSummary, 0o644); err != nil {
		t.Fatalf("Restore summary fixture: %v", err)
	}
	staleAggregate := []byte(strings.Replace(string(validAggregateSummary), "Python | `3.13`", "Python | `3.12`", 1))
	if err := os.WriteFile(filepath.Join(root, "SUMMARY.md"), staleAggregate, 0o644); err != nil {
		t.Fatalf("WriteFile stale aggregate fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "does not exactly match") {
		t.Fatalf("verifier unexpectedly accepted stale aggregate content: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(root, "SUMMARY.md"), validAggregateSummary, 0o644); err != nil {
		t.Fatalf("Restore aggregate fixture: %v", err)
	}
	extraRelative := filepath.ToSlash(filepath.Join("toolchain-profile-"+profile, profile+".zzz.json"))
	if err := os.WriteFile(filepath.Join(root, extraRelative), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("WriteFile extra bundle file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "MANIFEST.txt"), []byte(manifest+extraRelative+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile overbroad manifest: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "inventory") {
		t.Fatalf("verifier unexpectedly accepted an overbroad manifest: %v\n%s", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(root, "MANIFEST.txt"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("Restore manifest fixture: %v", err)
	}
	if err := os.Remove(filepath.Join(root, extraRelative)); err != nil {
		t.Fatalf("Remove extra bundle file: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, profile+".grype.json")); err != nil {
		t.Fatalf("Remove grype fixture: %v", err)
	}
	cmd = exec.Command("python3", path, root, profileSpec)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("verify_toolchain_artifacts.py unexpectedly succeeded after removing grype report: %s", string(out))
	}
	if !strings.Contains(string(out), "missing") || !strings.Contains(string(out), profile+".grype.json") {
		t.Fatalf("verification failure did not explain missing grype report: %q", string(out))
	}
}

func TestCheckGrypeReportScriptEnforcesFixableHighSeverityPolicy(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "check_grype_report.py")
	tests := []struct {
		name        string
		body        string
		wantSuccess bool
		want        []string
	}{
		{
			name: "no fixable high severity findings",
			body: `{"matches":[` +
				`{"vulnerability":{"id":"CVE-1","severity":"High","fix":{"versions":[],"state":"not-fixed"}},"artifact":{"name":"base","version":"1"}},` +
				`{"vulnerability":{"id":"CVE-2","severity":"Medium","fix":{"versions":["2"],"state":"fixed","available":[{"version":"2"}]}},"artifact":{"name":"library","version":"1"}}` +
				`]}`,
			wantSuccess: true,
			want:        []string{"policy passed", "2 match(es)"},
		},
		{
			name: "fixable high and critical findings",
			body: `{"matches":[` +
				`{"vulnerability":{"id":"CVE-CRITICAL","severity":"Critical","fix":{"versions":["3"],"state":"fixed"}},"artifact":{"name":"runtime","version":"2"}},` +
				`{"vulnerability":{"id":"GHSA-HIGH","severity":"High","fix":{"versions":[],"state":"fixed","available":[{"version":"5"}]}},"artifact":{"name":"package","version":"4"}}` +
				`]}`,
			want: []string{
				"2 fixable high/critical finding(s)",
				"Critical CVE-CRITICAL: runtime 2 -> 3",
				"High GHSA-HIGH: package 4 -> 5",
			},
		},
		{
			name: "scanner operational error",
			body: `{"error":"grype scan failed"}`,
			want: []string{"scanner operational error", "grype scan failed"},
		},
		{
			name: "invalid json",
			body: `{"matches":`,
			want: []string{"not valid JSON"},
		},
		{
			name: "invalid report shape",
			body: `{"descriptor":{}}`,
			want: []string{"no valid matches array"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reportPath := filepath.Join(t.TempDir(), "grype.json")
			if err := os.WriteFile(reportPath, []byte(tt.body), 0o644); err != nil {
				t.Fatalf("WriteFile(%q): %v", reportPath, err)
			}

			cmd := exec.Command("python3", path, reportPath)
			out, err := cmd.CombinedOutput()
			if tt.wantSuccess && err != nil {
				t.Fatalf("check_grype_report.py: %v\n%s", err, string(out))
			}
			if !tt.wantSuccess && err == nil {
				t.Fatalf("check_grype_report.py unexpectedly succeeded: %s", string(out))
			}
			for _, want := range tt.want {
				if !strings.Contains(string(out), want) {
					t.Fatalf("check_grype_report.py output missing %q in %q", want, string(out))
				}
			}
		})
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
	imageSBOMStart := strings.Index(body, "\n  image-sbom:")
	sandboxStart := strings.Index(body, "\n  sandbox:")
	if imageSBOMStart < 0 || sandboxStart <= imageSBOMStart {
		t.Fatalf("ci workflow has malformed image-sbom job boundaries")
	}
	imageSBOMSection := body[imageSBOMStart:sandboxStart]
	if !strings.Contains(imageSBOMSection, "scripts/install_anchore_tool.sh syft v1.42.4") ||
		!strings.Contains(imageSBOMSection, `-o spdx-json="${sbom_path}"`) ||
		!strings.Contains(imageSBOMSection, `-o syft-json="${syft_json_path}"`) {
		t.Fatalf("ci workflow must generate SPDX and reusable native SBOMs with one retryable pinned Syft install")
	}
	if !strings.Contains(body, "sbom-ci-python.spdx.json") {
		t.Fatalf("ci workflow must publish a named SBOM artifact for the sandbox runtime image")
	}
	if !strings.Contains(imageSBOMSection, "scripts/install_anchore_tool.sh grype v0.111.0") ||
		!strings.Contains(imageSBOMSection, `"${RUNNER_TEMP}/anchore-bin/grype" "sbom:${syft_json_path}" -o json > grype-ci-python.json`) {
		t.Fatalf("ci workflow must scan the reusable sandbox runtime SBOM with a retryable pinned Grype install")
	}
	if strings.Contains(body, `printf '{"error":"grype scan failed"}`) || strings.Contains(body, `printf '{"error":"syft scan failed"`) {
		t.Fatalf("ci workflow must fail closed instead of replacing scanner failures with JSON sentinels")
	}
	if !strings.Contains(body, "python3 scripts/check_grype_report.py grype-ci-python.json") {
		t.Fatalf("ci workflow must enforce the fixable high/critical policy for the sandbox runtime image")
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
	for name, section := range map[string]string{"image-sbom": imageSBOMSection, "toolchain-profile": profileSection} {
		pruneIdx := strings.Index(section, "docker buildx prune --all --force")
		syftIdx := strings.Index(section, "scripts/install_anchore_tool.sh syft v1.42.4")
		if pruneIdx < 0 || syftIdx < 0 || pruneIdx > syftIdx || !strings.Contains(section, "docker image prune -f") {
			t.Fatalf("%s job must prune the active Buildx cache before SBOM scans to avoid daemon-export disk exhaustion", name)
		}
	}
	if strings.Contains(body, "docker builder prune -af") {
		t.Fatalf("ci workflow must not use legacy builder pruning for docker-container Buildx caches")
	}
	if !strings.Contains(body, `chmod -R u+w "${GOMODCACHE}" 2>/dev/null || true`) ||
		!strings.Contains(body, `rm -rf "${GOCACHE}" "${GOMODCACHE}" /tmp/stereoscope-*`) ||
		!strings.Contains(body, `|| true`) {
		t.Fatalf("ci workflow must clean Go caches and stale stereoscope temp files best-effort before production-profile exports")
	}
	archiveIdx := strings.Index(profileSection, `docker archive export skipped to conserve CI storage`)
	pruneIdx := strings.Index(profileSection, "docker buildx prune --all --force")
	syftIdx := strings.Index(profileSection, "scripts/install_anchore_tool.sh syft v1.42.4")
	if archiveIdx < 0 || pruneIdx < 0 || syftIdx < 0 || archiveIdx > syftIdx || pruneIdx > syftIdx {
		t.Fatalf("ci workflow must write archive diagnostics and prune the active builder before production-profile scanner exports")
	}
	if !strings.Contains(profileSection, `image_ref="aonohako-ci-prod:${{ matrix.name }}"`) || !strings.Contains(profileSection, `--source-name "${image_ref}"`) {
		t.Fatalf("ci workflow must preserve the tagged image reference in SPDX document provenance")
	}
	if !strings.Contains(profileSection, `image_id="$(docker image inspect "${image_ref}" --format '{{.Id}}')"`) ||
		!strings.Contains(profileSection, `echo "image_id=${image_id}" >> "${GITHUB_OUTPUT}"`) ||
		!strings.Contains(profileSection, `image_id="${{ steps.profile.outputs.image_id }}"`) {
		t.Fatalf("ci workflow must capture one immutable image ID for the summary and scanner provenance")
	}
	if !strings.Contains(profileSection, `"${HOME}/.cache/syft"`) || !strings.Contains(profileSection, `"${HOME}/.cache/grype"`) {
		t.Fatalf("ci workflow must clean scanner caches after production-profile scans")
	}
	if !strings.Contains(profileSection, `docker image rm "${image_ref}" || true`) {
		t.Fatalf("ci workflow must remove production-profile images after the reusable Syft catalog is generated")
	}
	if !strings.Contains(profileSection, `-o syft-json="${syft_json_path}"`) ||
		!strings.Contains(profileSection, `"${RUNNER_TEMP}/anchore-bin/grype" "sbom:${syft_json_path}" -o json > "${report_path}" || scan_status=$?`) ||
		!strings.Contains(profileSection, `rm -f "${syft_json_path}"`) ||
		!strings.Contains(profileSection, `exit "${scan_status}"`) {
		t.Fatalf("ci workflow must reuse and clean the native Syft catalog while failing closed on Grype operational errors")
	}
	if strings.Contains(profileSection, `"${RUNNER_TEMP}/anchore-bin/grype" "${image_ref}"`) ||
		strings.Contains(imageSBOMSection, `"${RUNNER_TEMP}/anchore-bin/grype" "${image_ref}"`) ||
		strings.Contains(imageSBOMSection, `"${RUNNER_TEMP}/anchore-bin/grype" "aonohako-sbom:ci-python"`) {
		t.Fatalf("ci workflow must not export Docker images a second time for Grype scans")
	}
	if !strings.Contains(profileSection, `provenance_path="toolchain-artifacts/${{ matrix.name }}/${{ matrix.name }}.provenance.json"`) || !strings.Contains(profileSection, `"summary.md":$summary_sha256`) || !strings.Contains(profileSection, `"sbom.spdx.json":$sbom_sha256`) || !strings.Contains(profileSection, `"grype.json":$grype_sha256`) {
		t.Fatalf("ci workflow must bind profile reports to immutable image provenance and their exact digests")
	}
	setupGoIdx := strings.Index(imageSBOMSection, "uses: actions/setup-go@")
	buildxIdx := strings.Index(imageSBOMSection, "uses: docker/setup-buildx-action@")
	if setupGoIdx < 0 || buildxIdx < 0 || setupGoIdx > buildxIdx || !strings.Contains(imageSBOMSection[setupGoIdx:buildxIdx], "cache: false") {
		t.Fatal("image-sbom setup-go step must disable its unused host cache")
	}
	if strings.Contains(profileSection, "uses: actions/setup-go@") {
		t.Fatal("toolchain-profile jobs must use the prebuilt runtime-builder artifact instead of restoring a host Go cache")
	}
	if !strings.Contains(profileSection, "AONOHAKO_RUNTIME_BUILDER=") || !strings.Contains(profileSection, "AONOHAKO_RUNTIME_BINARIES_CONTEXT=") {
		t.Fatal("toolchain-profile jobs must consume the prebuilt runtime binary artifact")
	}
	if !strings.Contains(body, "AONOHAKO_LANGUAGES=\"${{ matrix.languages }}\"") {
		t.Fatalf("ci workflow must include the language list in the profile summaries")
	}
	if !strings.Contains(body, "actions/upload-artifact@") || !strings.Contains(body, "actions/download-artifact@") {
		t.Fatalf("ci workflow must aggregate toolchain summary data through artifacts")
	}
	if !strings.Contains(summarySection, "    if: ${{ always() }}") {
		t.Fatalf("toolchain summary job must remain always-on")
	}
	if !strings.Contains(summarySection, "      - uses: actions/checkout@") {
		t.Fatalf("toolchain summary job must check out the repository before running aggregation scripts")
	}
	if strings.Contains(body, `docker save "aonohako-ci-prod:${{ matrix.name }}"`) {
		t.Fatalf("ci workflow must not export full production-profile images into artifacts")
	}
	if !strings.Contains(body, `docker archive export skipped to conserve CI storage`) || !strings.Contains(body, "toolchain-artifacts/SHA256SUMS") {
		t.Fatalf("ci workflow must record archive skip diagnostics and keep summary SHA256 aggregation available")
	}
	if !strings.Contains(body, `"${archive_path}.error.json"`) || !strings.Contains(body, "*.docker.tar.gz.error.json") {
		t.Fatalf("ci workflow must publish archive export diagnostics when runner disk cannot hold an image archive")
	}
	if !strings.Contains(summarySection, "python3 scripts/verify_toolchain_artifacts.py toolchain-artifacts") {
		t.Fatalf("ci workflow must fail closed when production-profile artifacts are incomplete or digest mismatched")
	}
	if !strings.Contains(summarySection, "PRODUCTION_MATRIX: ${{ needs.runtime-matrix.outputs.production_matrix }}") || !strings.Contains(summarySection, `python3 scripts/verify_toolchain_artifacts.py toolchain-artifacts "${expected_profiles[@]}"`) {
		t.Fatalf("ci workflow must verify the exact production profile inventory")
	}
	if !strings.Contains(summarySection, `jq -r '.[] | "\(.name)=\(.languages)"'`) {
		t.Fatalf("ci workflow must bind each expected profile to its matrix language inventory")
	}
	if strings.Contains(summarySection, "continue-on-error: true") || !strings.Contains(summarySection, `echo "No toolchain profile artifacts were produced."`) || !strings.Contains(summarySection, "exit 1") {
		t.Fatalf("ci workflow must fail closed when profile artifact download or discovery fails")
	}
	shaIdx := strings.Index(summarySection, "find toolchain-artifacts -type f -name '*.docker.tar.gz'")
	manifestIdx := strings.Index(summarySection, "printf '%s\\n' SUMMARY.md MANIFEST.txt SHA256SUMS")
	verifyIdx := strings.Index(summarySection, "python3 scripts/verify_toolchain_artifacts.py toolchain-artifacts")
	if shaIdx < 0 || manifestIdx < 0 || verifyIdx < 0 || !(shaIdx < manifestIdx && manifestIdx < verifyIdx) {
		t.Fatalf("ci workflow must create bundle-relative archive digests and the exact manifest before verification")
	}
	if strings.Contains(summarySection, "find toolchain-artifacts -type f | sort") || !strings.Contains(summarySection, "sed 's#^toolchain-artifacts/##'") {
		t.Fatalf("ci workflow manifest must use bundle-relative paths from the selected upload inventory")
	}
	if !strings.Contains(summarySection, `relative_path="${archive#toolchain-artifacts/}"`) || !strings.Contains(summarySection, `> "${archive}.sha256"`) {
		t.Fatalf("ci workflow must regenerate portable bundle-relative archive sidecars")
	}
	for _, selected := range []string{"-name summary.md", "-name '*.sbom.spdx.json'", "-name '*.grype.json'", "-name '*.provenance.json'", "-name '*.docker.tar.gz'", "-name '*.docker.tar.gz.sha256'", "-name '*.docker.tar.gz.error.json'"} {
		if !strings.Contains(summarySection, selected) {
			t.Fatalf("ci workflow manifest must select %q", selected)
		}
	}
	if !strings.Contains(summarySection, "toolchain-artifacts/toolchain-profile-*/**/*.docker.tar.gz\n") {
		t.Fatalf("ci workflow upload must match manifest entries for real image archives")
	}
	if !strings.Contains(summarySection, "toolchain-artifacts/toolchain-profile-*/**/*.sbom.spdx.json\n") || !strings.Contains(summarySection, "toolchain-artifacts/toolchain-profile-*/**/*.grype.json\n") || !strings.Contains(summarySection, "toolchain-artifacts/toolchain-profile-*/**/*.provenance.json\n") {
		t.Fatalf("ci workflow summary bundle must retain the scanner evidence it verifies")
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
		`install -d -m 0755 /work /usr/local/bin/aonohako-sandbox-tests`,
		`/usr/local/bin/aonohako-sandbox-tests/execute.test`,
		`/usr/local/bin/aonohako-sandbox-tests/compile.test`,
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
