package runtimepacks

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCIWorkflowPinsExternalDependencies(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	actionPattern := regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*([^@\s]+)@([^\s#]+)`)
	immutableRefPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)
	jobLevelFixture := "jobs:\n  shared-checks:\n    uses: example/action/.github/workflows/ci.yml@v1\n"
	if !actionPattern.MatchString(jobLevelFixture) {
		t.Fatal("action policy must inspect job-level reusable workflow references")
	}
	matches := actionPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatal("ci workflow contains no external actions")
	}
	for _, match := range matches {
		if !immutableRefPattern.MatchString(match[2]) {
			t.Errorf("action %s uses mutable ref %q", match[1], match[2])
		}
	}

	govulnPattern := regexp.MustCompile(`govulncheck@([^\s]+)`)
	govulnMatch := govulnPattern.FindStringSubmatch(body)
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
		t.Fatal("ci workflow is missing the toolchain-profile job")
	}
	profileJob := body[start:end]
	for _, required := range []string{
		"./scripts/build_runtime_images.sh",
		"-mode production",
		`-only "${{ matrix.name }}"`,
		`-tag-prefix "aonohako-ci-prod"`,
	} {
		if !strings.Contains(profileJob, required) {
			t.Fatalf("toolchain-profile job no longer builds every production matrix image with %q", required)
		}
	}
	if !strings.Contains(profileJob, `docker run --rm "aonohako-ci-prod:${{ matrix.name }}" aonohako-smoke`) {
		t.Fatal("toolchain-profile job must run aonohako-smoke for every production matrix image")
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
}

func TestCIWorkflowSharesSandboxBuildCacheReadOnly(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	const (
		cacheKey     = `${{ github.workflow }}-runtime-seed-${{ runner.os }}-${{ runner.arch }}-${{ github.sha }}`
		cachePath    = `${{ runner.temp }}/aonohako-runtime-cache`
		cacheSave    = `actions/cache/save@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # v6.1.0`
		cacheRestore = `actions/cache/restore@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # v6.1.0`
		cacheEnv     = `echo "AONOHAKO_DOCKER_CACHE_FROM=type=local,src=${RUNNER_TEMP}/aonohako-runtime-cache" >> "${GITHUB_ENV}"`
	)

	if count := strings.Count(body, cacheSave); count != 1 {
		t.Fatalf("ci workflow must have exactly one pinned runtime cache writer, got %d", count)
	}
	if count := strings.Count(body, cacheRestore); count != 4 {
		t.Fatalf("ci workflow must restore the pinned runtime cache in the seed job and three consumers, got %d", count)
	}

	sandboxStart := strings.Index(body, "\n  sandbox:")
	sandboxEnd := strings.Index(body, "\n  runtime-matrix:")
	if sandboxStart < 0 || sandboxEnd <= sandboxStart {
		t.Fatal("ci workflow is missing the sandbox job")
	}
	sandboxJob := body[sandboxStart:sandboxEnd]
	for _, required := range []string{
		"id: runtime-cache",
		cacheRestore,
		cacheSave,
		"path: " + cachePath,
		"key: " + cacheKey,
		"--target runtime-seed",
		`-cache-to "type=local,dest=${RUNNER_TEMP}/aonohako-runtime-cache,mode=max"`,
		cacheEnv,
		"if: steps.runtime-cache.outputs.cache-hit != 'true'",
	} {
		if !strings.Contains(sandboxJob, required) {
			t.Errorf("sandbox job does not seed the shared BuildKit cache with %q", required)
		}
	}
	if strings.Count(sandboxJob, "if: steps.runtime-cache.outputs.cache-hit != 'true'") != 2 {
		t.Error("sandbox job must build and save the seed only after an exact cache miss")
	}
	if !strings.Contains(sandboxJob, "docker buildx build\n          --target runtime-seed\n          --cache-to") {
		t.Error("sandbox job must attach the cache exporter directly to the dedicated runtime-seed build")
	}
	if count := strings.Count(sandboxJob, "-cache-to"); count != 1 {
		t.Errorf("sandbox job must have exactly one cache exporter on the runtime-seed build, got %d", count)
	}

	jobs := []struct {
		name string
		next string
	}{
		{name: "language-smoke", next: "toolchain-profile"},
		{name: "toolchain-profile", next: "toolchain-summary"},
		{name: "mixin-smoke"},
	}
	for _, job := range jobs {
		start := strings.Index(body, "\n  "+job.name+":")
		if start < 0 {
			t.Errorf("ci workflow is missing the %s job", job.name)
			continue
		}
		end := len(body)
		if job.next != "" {
			end = strings.Index(body[start+1:], "\n  "+job.next+":")
			if end < 0 {
				t.Errorf("ci workflow has malformed %s job boundaries", job.name)
				continue
			}
			end += start + 1
		}
		consumerJob := body[start:end]
		for _, required := range []string{
			"- sandbox",
			"id: runtime-cache",
			cacheRestore,
			"path: " + cachePath,
			"key: " + cacheKey,
			cacheEnv,
			"if: steps.runtime-cache.outputs.cache-hit == 'true'",
		} {
			if !strings.Contains(consumerJob, required) {
				t.Errorf("%s job does not consume the exact sandbox cache with %q", job.name, required)
			}
		}
		if strings.Contains(consumerJob, cacheSave) || strings.Contains(consumerJob, "-cache-to") {
			t.Errorf("%s job must consume the sandbox cache read-only", job.name)
		}
		if strings.Contains(consumerJob, "fail-on-cache-miss: true") {
			t.Errorf("%s job must fall back to an uncached build when the optional seed cache is unavailable", job.name)
		}
	}
}
