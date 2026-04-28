package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/profiles"
	"aonohako/internal/runtimepacks"
)

func TestCompileExecuteCasesCoverCILanguages(t *testing.T) {
	catalog, err := runtimepacks.LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	images, err := catalog.CILanguageImages()
	if err != nil {
		t.Fatalf("CILanguageImages: %v", err)
	}
	cases := compileExecuteCases()
	for _, image := range images {
		language := strings.TrimPrefix(image.Name, "ci-")
		if _, ok := cases[language]; !ok {
			t.Fatalf("compile-execute cases are missing language %q", language)
		}
	}
}

func TestCompileExecuteCasesCoverMixinLanguages(t *testing.T) {
	catalog, err := runtimepacks.LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	profile, ok := catalog.Profiles["type-i"]
	if !ok {
		t.Fatalf("missing type-i profile")
	}
	cases := compileExecuteCases()
	for _, language := range profile.Languages {
		if _, ok := cases[language]; !ok {
			t.Fatalf("compile-execute cases are missing mixin language %q", language)
		}
	}
}

func TestCompileExecuteCasesResolveProfilesAndSources(t *testing.T) {
	cases := compileExecuteCases()
	for language, tc := range cases {
		profile, ok := profiles.Resolve(tc.compileLang)
		if !ok {
			t.Fatalf("language %q uses unknown compile profile %q", language, tc.compileLang)
		}
		if profile.RunLang == "" {
			t.Fatalf("language %q resolved profile %q without run language", language, tc.compileLang)
		}
		if len(tc.sources) == 0 {
			t.Fatalf("language %q has no sources", language)
		}
		for _, src := range tc.sources {
			if strings.TrimSpace(src.Name) == "" || strings.TrimSpace(src.DataB64) == "" {
				t.Fatalf("language %q contains an empty source entry: %+v", language, src)
			}
		}
	}
}

func TestSelftestUsageListsCgroupPreflight(t *testing.T) {
	if !strings.Contains(selftestUsage, "cgroup-preflight") {
		t.Fatalf("selftest usage should list cgroup-preflight: %s", selftestUsage)
	}
}

func TestSelftestUsageListsDeploymentContract(t *testing.T) {
	if !strings.Contains(selftestUsage, "deployment-contract") {
		t.Fatalf("selftest usage should list deployment-contract: %s", selftestUsage)
	}
}

func TestSelftestUsageListsRuntimeMemory(t *testing.T) {
	if !strings.Contains(selftestUsage, "runtime-memory") {
		t.Fatalf("selftest usage should list runtime-memory: %s", selftestUsage)
	}
}

func TestDeploymentContractSummaryReportsTmpfsRequirement(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "none")
	t.Setenv("AONOHAKO_REQUIRE_WORK_ROOT_TMPFS", "true")

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	err = runDeploymentContractSuite()
	_ = w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatalf("runDeploymentContractSuite: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	var summary map[string]any
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("decode deployment summary: %v\n%s", err, string(data))
	}
	if summary["require_work_root_tmpfs"] != true {
		t.Fatalf("require_work_root_tmpfs = %#v, want true; summary=%s", summary["require_work_root_tmpfs"], string(data))
	}
	if summary["contract_implemented"] != true {
		t.Fatalf("contract_implemented = %#v, want true; summary=%s", summary["contract_implemented"], string(data))
	}
	if summary["contract"] != "remote-control-plane" {
		t.Fatalf("contract = %#v, want remote-control-plane; summary=%s", summary["contract"], string(data))
	}
}
