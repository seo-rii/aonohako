package main

import (
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
