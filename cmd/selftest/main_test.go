package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/profiles"
	"aonohako/internal/runtimepacks"
	"aonohako/internal/runvalidation"
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

func TestRuntimeStartupMemoryCoversResourceSensitiveLanguages(t *testing.T) {
	limits := runtimeStartupMemoryMB()
	compileCases := compileExecuteCases()
	for _, language := range []string{"go", "rust", "zig", "kotlin-jvm", "erlang", "julia", "swift", "dart"} {
		memoryMB, ok := limits[language]
		if !ok || memoryMB <= 0 {
			t.Fatalf("runtime startup memory is missing language %q", language)
		}
		if _, ok := compileCases[language]; !ok {
			t.Fatalf("runtime startup language %q has no compile-execute case", language)
		}
	}
	if got := limits["go"]; got != 1120 {
		t.Fatalf("Go constrained startup memory = %d, want 1120", got)
	}
}

func TestStrictRuntimeMemoryCasesCoverNativeAndScriptRuntimes(t *testing.T) {
	cases := strictRuntimeMemoryCases()
	for _, language := range []string{"go", "rust", "ruby", "php", "lua", "perl"} {
		tc, ok := cases[language]
		if !ok {
			t.Fatalf("strict runtime-memory cases are missing language %q", language)
		}
		profile, ok := profiles.Resolve(tc.compileLang)
		if !ok || profile.RunLang == "" {
			t.Fatalf("language %q has invalid compile profile %q", language, tc.compileLang)
		}
		if tc.memoryMB <= 0 || len(tc.sources) == 0 {
			t.Fatalf("language %q has incomplete runtime-memory case: %+v", language, tc)
		}
	}
}

func TestLanguageSecurityCasesCoverRiskyRuntimeFamilies(t *testing.T) {
	cases := languageSecurityCases(1)
	for _, language := range []string{
		"plain",
		"go",
		"rust",
		"python",
		"pypy",
		"javascript",
		"typescript",
		"coffeescript",
		"deno",
		"java",
		"ruby",
		"perl",
		"php",
		"tcl",
	} {
		if len(cases[language]) == 0 {
			t.Fatalf("language-security cases are missing language %q", language)
		}
	}
}

func TestLanguageSecurityCasesResolveProfilesAndSources(t *testing.T) {
	cases := languageSecurityCases(1)
	for language, languageCases := range cases {
		for _, tc := range languageCases {
			profile, ok := profiles.Resolve(tc.compileLang)
			if !ok {
				t.Fatalf("language %q security case %q uses unknown compile profile %q", language, tc.name, tc.compileLang)
			}
			if profile.RunLang == "" {
				t.Fatalf("language %q security case %q resolved profile %q without run language", language, tc.name, tc.compileLang)
			}
			if strings.TrimSpace(tc.name) == "" {
				t.Fatalf("language %q has a security case without a name", language)
			}
			if strings.TrimSpace(tc.expectedStdout) == "" {
				t.Fatalf("language %q security case %q has no expected stdout", language, tc.name)
			}
			if len(tc.sources) == 0 {
				t.Fatalf("language %q security case %q has no sources", language, tc.name)
			}
			for _, src := range tc.sources {
				if strings.TrimSpace(src.Name) == "" || strings.TrimSpace(src.DataB64) == "" {
					t.Fatalf("language %q security case %q contains an empty source entry: %+v", language, tc.name, src)
				}
			}
		}
	}
}

func TestSelftestUsageListsCgroupPreflight(t *testing.T) {
	if !strings.Contains(selftestUsage, "cgroup-preflight") {
		t.Fatalf("selftest usage should list cgroup-preflight: %s", selftestUsage)
	}
}

func TestSelftestUsageListsMountPreflight(t *testing.T) {
	if !strings.Contains(selftestUsage, "mount-preflight") {
		t.Fatalf("selftest usage should list mount-preflight: %s", selftestUsage)
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

func TestUHMLangMemoryStressProgramFitsRunRequestLimits(t *testing.T) {
	program := uhmlangMemoryStressProgram()
	if len([]byte(program)) > runvalidation.MaxBinaryFileBytes {
		t.Fatalf("uhmlang memory stress program is %d bytes, max binary size is %d", len([]byte(program)), runvalidation.MaxBinaryFileBytes)
	}
	encoded := encodeScript(program)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode encoded uhmlang stress program: %v", err)
	}
	if len(decoded) != len([]byte(program)) {
		t.Fatalf("decoded length = %d, want %d", len(decoded), len([]byte(program)))
	}
}

func TestSelftestUsageListsLanguageSecurity(t *testing.T) {
	if !strings.Contains(selftestUsage, "language-security") {
		t.Fatalf("selftest usage should list language-security: %s", selftestUsage)
	}
}

func TestDeploymentContractSummaryReportsTmpfsRequirement(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "none")
	t.Setenv("AONOHAKO_REQUIRE_WORK_ROOT_TMPFS", "true")
	t.Setenv("AONOHAKO_WORK_ROOT_MAX_BYTES", "12345")
	t.Setenv("AONOHAKO_WORK_ROOT_MAX_FILES", "678")

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
	if summary["work_root_max_bytes"] != float64(12345) {
		t.Fatalf("work_root_max_bytes = %#v, want 12345; summary=%s", summary["work_root_max_bytes"], string(data))
	}
	if summary["work_root_max_files"] != float64(678) {
		t.Fatalf("work_root_max_files = %#v, want 678; summary=%s", summary["work_root_max_files"], string(data))
	}
	if summary["contract_implemented"] != true {
		t.Fatalf("contract_implemented = %#v, want true; summary=%s", summary["contract_implemented"], string(data))
	}
	if summary["contract"] != "remote-control-plane" {
		t.Fatalf("contract = %#v, want remote-control-plane; summary=%s", summary["contract"], string(data))
	}
	capabilities, ok := summary["capabilities"].([]any)
	if !ok || len(capabilities) == 0 {
		t.Fatalf("capabilities missing from deployment summary: %s", string(data))
	}
}
