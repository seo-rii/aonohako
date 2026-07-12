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
	if !strings.Contains(profileJob, `./scripts/build_runtime_images.sh -mode production -only "${{ matrix.name }}" -tag-prefix "aonohako-ci-prod"`) {
		t.Fatal("toolchain-profile job no longer builds every production matrix image")
	}
	if !strings.Contains(profileJob, `docker run --rm "aonohako-ci-prod:${{ matrix.name }}" aonohako-smoke`) {
		t.Fatal("toolchain-profile job must run aonohako-smoke for every production matrix image")
	}
}
