package runtimepacks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCIWorkflowPinsExternalDependencies(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("Glob workflows: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("repository contains no workflows")
	}

	actionPattern := regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*([^@\s]+)@([^\s#]+)`)
	immutableRefPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)
	jobLevelFixture := "jobs:\n  shared-checks:\n    uses: example/action/.github/workflows/ci.yml@v1\n"
	if !actionPattern.MatchString(jobLevelFixture) {
		t.Fatal("action policy must inspect job-level reusable workflow references")
	}

	var body strings.Builder
	actionCount := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		body.Write(data)
		matches := actionPattern.FindAllStringSubmatch(string(data), -1)
		actionCount += len(matches)
		for _, match := range matches {
			if !immutableRefPattern.MatchString(match[2]) {
				t.Errorf("%s: action %s uses mutable ref %q", path, match[1], match[2])
			}
		}
	}
	if actionCount == 0 {
		t.Fatal("workflows contain no external actions")
	}

	govulnPattern := regexp.MustCompile(`govulncheck@([^\s]+)`)
	govulnMatch := govulnPattern.FindStringSubmatch(body.String())
	if len(govulnMatch) != 2 {
		t.Fatal("ci workflow does not run govulncheck")
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(govulnMatch[1]) {
		t.Errorf("govulncheck uses mutable or non-release version %q", govulnMatch[1])
	}
}

func TestCIWorkflowSmokesEveryProductionProfile(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	start := strings.Index(body, "\n  toolchain-profile:")
	end := strings.Index(body, "\n  toolchain-summary:")
	if start < 0 || end <= start {
		t.Fatal("ci workflow is missing the toolchain-profile jobs")
	}
	profileJobs := body[start:end]
	for _, required := range []string{
		"production_regular_matrix",
		"production_cached_matrix",
		"toolchain-profile-cache-write:",
		"toolchain-profile-cache-read:",
		"uses: ./.github/workflows/toolchain-profile.yml",
		"./scripts/build_runtime_images.sh",
		"-mode production",
		`-only "${{ matrix.name }}"`,
		`-tag-prefix "aonohako-ci-prod"`,
		`docker run --rm "aonohako-ci-prod:${{ matrix.name }}" aonohako-smoke`,
	} {
		if !strings.Contains(profileJobs, required) {
			t.Fatalf("production profile jobs are missing %q", required)
		}
	}

	reusablePath := filepath.Join("..", "..", ".github", "workflows", "toolchain-profile.yml")
	reusableData, err := os.ReadFile(reusablePath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", reusablePath, err)
	}
	reusableBody := string(reusableData)
	for _, required := range []string{
		`./scripts/build_runtime_images.sh -mode production -only "${{ inputs.image_name }}" -tag-prefix "aonohako-ci-prod"`,
		`docker run --rm "aonohako-ci-prod:${{ inputs.image_name }}" aonohako-smoke`,
		`name: toolchain-profile-${{ inputs.image_name }}`,
	} {
		if !strings.Contains(reusableBody, required) {
			t.Fatalf("cached toolchain workflow is missing %q", required)
		}
	}
}

func TestCIWorkflowRunsPrivilegedExecutionRegressions(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	start := strings.Index(body, "\n  sandbox:")
	end := strings.Index(body, "\n  runtime-matrix:")
	if start < 0 || end <= start {
		t.Fatal("ci workflow is missing the sandbox job")
	}
	sandboxJob := body[start:end]
	for _, testName := range []string{
		"TestSandboxSecurityRegressionSuite",
		"TestRunBlocksForkForSubmittedTrustedRuntimeName",
		"TestRunSPJRejectsTruncatedScore",
		"TestRunInteractiveEnforcesInteractorWallLimit",
	} {
		if !strings.Contains(sandboxJob, testName) {
			t.Errorf("sandbox job does not run privileged regression %s", testName)
		}
	}
	if strings.Contains(sandboxJob, "runtime-binaries") {
		t.Fatal("sandbox must run in parallel with the artifact producer instead of waiting for it")
	}
}

func TestCIWorkflowSharesSmallRuntimeBinaryArtifact(t *testing.T) {
	root := filepath.Join("..", "..")
	workflowPaths := []string{
		filepath.Join(root, ".github", "workflows", "ci.yml"),
		filepath.Join(root, ".github", "workflows", "runtime-smoke.yml"),
		filepath.Join(root, ".github", "workflows", "toolchain-profile.yml"),
	}
	bodies := make(map[string]string, len(workflowPaths))
	for _, path := range workflowPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		bodies[path] = string(data)
		for _, removed := range []string{"actions/cache/", "aonohako-runtime-cache", "runtime-seed"} {
			if strings.Contains(string(data), removed) {
				t.Errorf("%s still contains removed large-cache fan-out marker %q", path, removed)
			}
		}
	}

	ciBody := bodies[workflowPaths[0]]
	producerStart := strings.Index(ciBody, "\n  runtime-binaries:")
	producerEnd := strings.Index(ciBody, "\n  sandbox:")
	if producerStart < 0 || producerEnd <= producerStart {
		t.Fatal("ci workflow is missing the independent runtime-binaries producer")
	}
	producer := ciBody[producerStart:producerEnd]
	for _, required := range []string{
		"--target ci-runtime-artifacts",
		`--output "type=local,dest=${RUNNER_TEMP}/aonohako-runtime-artifacts"`,
		`name: runtime-binaries-${{ github.sha }}`,
		"if-no-files-found: error",
		"retention-days: 1",
		"compression-level: 9",
	} {
		if !strings.Contains(producer, required) {
			t.Errorf("runtime-binaries producer is missing %q", required)
		}
	}

	consumerBody := ciBody + bodies[workflowPaths[1]] + bodies[workflowPaths[2]]
	for _, required := range []string{
		"actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8",
		`chmod 0755 "${RUNNER_TEMP}/aonohako-runtime-artifacts/aonohako-runtime-builder"`,
		"AONOHAKO_RUNTIME_BUILDER=",
		"AONOHAKO_RUNTIME_BINARIES_CONTEXT=",
	} {
		if !strings.Contains(consumerBody, required) {
			t.Errorf("runtime artifact consumers are missing %q", required)
		}
	}

	scriptPath := filepath.Join(root, "scripts", "build_runtime_images.sh")
	scriptData, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", scriptPath, err)
	}
	scriptBody := string(scriptData)
	for _, required := range []string{
		`if [[ -n "${AONOHAKO_RUNTIME_BUILDER:-}" ]]`,
		`exec "${AONOHAKO_RUNTIME_BUILDER}" "$@"`,
		"exec go run ./cmd/runtime-builder \"$@\"",
	} {
		if !strings.Contains(scriptBody, required) {
			t.Errorf("runtime build wrapper is missing %q", required)
		}
	}
}

func TestCIWorkflowTargetsPersistentRegistryCaches(t *testing.T) {
	root := filepath.Join("..", "..")
	ciPath := filepath.Join(root, ".github", "workflows", "ci.yml")
	ciData, err := os.ReadFile(ciPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", ciPath, err)
	}
	ciBody := string(ciData)

	jobsIndex := strings.Index(ciBody, "\njobs:")
	if jobsIndex < 0 {
		t.Fatal("ci workflow is missing jobs")
	}
	if strings.Contains(ciBody[:jobsIndex], "packages: write") {
		t.Fatal("ci workflow must not grant package write permission globally")
	}
	if strings.Count(ciBody, "packages: write") != 2 || strings.Count(ciBody, "packages: read") != 2 {
		t.Fatal("only the two targeted writer and two reader jobs may receive package permissions")
	}
	if strings.Count(ciBody, "github.event_name == 'push' && github.ref == 'refs/heads/main'") != 4 {
		t.Fatal("cache writers and readers must use complementary exact main-push guards")
	}

	for _, required := range []string{
		`select(.name == "ci-idris2" or .name == "ci-cuda-ocelot")`,
		`select(.name == "type-a" or .name == "type-c" or .name == "type-o")`,
		`ghcr.io/${{ github.repository }}-buildcache:v1-ci-${{ matrix.name }}-linux-amd64`,
		`ghcr.io/${{ github.repository }}-buildcache:v1-prod-${{ matrix.name }}-linux-amd64`,
	} {
		if !strings.Contains(ciBody, required) {
			t.Errorf("ci workflow is missing targeted persistent-cache contract %q", required)
		}
	}
	for _, line := range strings.Split(ciBody, "\n") {
		if strings.Contains(line, "-buildcache:") && (strings.Contains(line, "github.sha") || strings.Contains(line, "head_ref")) {
			t.Errorf("persistent cache ref must be stable across commits: %s", line)
		}
	}
	if strings.Contains(ciBody, "pull_request_target") {
		t.Fatal("persistent cache workflow must not run untrusted code through pull_request_target")
	}

	for _, name := range []string{"runtime-smoke.yml", "toolchain-profile.yml"} {
		path := filepath.Join(root, ".github", "workflows", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		body := string(data)
		for _, required := range []string{
			"docker/login-action@af1e73f918a031802d376d3c8bbc3fe56130a9b0 # v4.4.0",
			"continue-on-error: true",
			"AONOHAKO_DOCKER_CACHE_FROM=type=registry,ref=${{ inputs.cache_ref }}",
			"AONOHAKO_DOCKER_CACHE_TO=type=registry,ref=${{ inputs.cache_ref }},mode=min,oci-mediatypes=true,image-manifest=true,ignore-error=true",
			"AONOHAKO_DOCKER_CACHE_TARGET=runtime-toolchain",
			"steps.registry-login.outcome == 'success'",
			"if: inputs.cache_write",
		} {
			if !strings.Contains(body, required) {
				t.Errorf("%s is missing persistent-cache guard %q", name, required)
			}
		}
	}
}
