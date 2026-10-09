package runtimepacks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRepositoryLanguageSmokesPreserveCommandFailures(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	maskedSubstitution := regexp.MustCompile(`(?m)(?:^|;\s*)(?:test|\[)\s+(?:-z\s+)?"\$\(`)
	maskedCompilerArgument := regexp.MustCompile(`(?m)^\s*(?:gcc|carbon)\s+[^\n]*\$\(`)
	maskedFishSubstitution := regexp.MustCompile(`(?m)^test\s+\(`)
	for language, spec := range catalog.Languages {
		t.Run(language, func(t *testing.T) {
			command := spec.Smoke.Command
			body := command[len(command)-1]
			switch filepath.Base(command[0]) {
			case "bash", "zsh":
				if !strings.HasPrefix(body, "set -euo pipefail\n") {
					t.Fatal("shell smoke must stop on command and pipeline failures")
				}
			case "dash":
				if !strings.HasPrefix(body, "set -eu\n") || strings.Contains(body, "|") {
					t.Fatal("POSIX shell smoke must stop on failures and avoid unchecked pipelines")
				}
			case "fish":
				if !strings.Contains(body, "; or exit 1") {
					t.Fatal("Fish smoke must explicitly propagate command failures")
				}
				if maskedFishSubstitution.MatchString(body) {
					t.Fatal("capture Fish command output before comparing it so command failures are preserved")
				}
			case "python3":
				if language != "python" || !strings.Contains(body, "assert ") {
					t.Fatal("direct Python smoke must assert its computed results")
				}
			default:
				t.Fatalf("unvalidated smoke command: %q", command[0])
			}
			if strings.Contains(body, "|| true") {
				t.Fatal("smoke must not discard command exit statuses")
			}
			if maskedSubstitution.MatchString(body) {
				t.Fatal("capture command output before comparing it so command failures are preserved")
			}
			if maskedCompilerArgument.MatchString(body) {
				t.Fatal("capture compiler argument helpers before invoking the compiler so helper failures are preserved")
			}
		})
	}
}

func TestRepositoryShellSmokesHaveValidSyntax(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for language, spec := range catalog.Languages {
		command := spec.Smoke.Command
		if filepath.Base(command[0]) != "bash" {
			continue
		}
		t.Run(language, func(t *testing.T) {
			cmd := exec.Command("bash", "-n", "-c", command[len(command)-1])
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("shell syntax: %v\n%s", err, output)
			}
		})
	}
}

func TestConfiguredSmokesRejectFailedOrNoisyRuntimes(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Execute the catalog scripts themselves with controlled toolchain stand-ins.
	// Printing the expected line must not hide a compiler or runtime failure,
	// and matching one line must not allow unrelated output around it.
	type smokeResultCase struct {
		name         string
		stdout       string
		runtimeExit  int
		compilerExit int
		wantExit     int
		helperExit   int
	}
	languages := []struct {
		name         string
		stdout       string
		runtime      string
		compilers    []string
		auxiliaries  []string
		configHelper string
	}{
		{name: "c", stdout: "ok\n", compilers: []string{"gcc"}},
		{name: "cython", stdout: "ok\n", compilers: []string{"cython3", "gcc"}, configHelper: "python3-config"},
		{name: "java", stdout: "ok\n", runtime: "java", compilers: []string{"javac"}, auxiliaries: []string{"jar"}},
		{name: "apecode", stdout: "3 1 2\n", compilers: []string{"apecc"}},
		{name: "aheui", stdout: "Hello, World!\n", runtime: "aheui"},
		{name: "ruby", stdout: "ok\n", runtime: "ruby"},
		{name: "bc", stdout: "2\n", runtime: "bc"},
		{name: "graphql", stdout: "{\"data\":{\"ok\":\"ok\"}}\n", runtime: "aonohako-graphql-run"},
	}
	for _, language := range languages {
		t.Run(language.name, func(t *testing.T) {
			tests := []smokeResultCase{
				{name: "valid output", stdout: language.stdout},
				{name: "runtime fails after expected output", stdout: language.stdout, runtimeExit: 7, wantExit: 7},
				{name: "extra stdout before expected output", stdout: "unexpected\n" + language.stdout, wantExit: 1},
				{name: "extra stdout after expected output", stdout: language.stdout + "unexpected\n", wantExit: 1},
			}
			if len(language.compilers) != 0 {
				tests = append(tests, smokeResultCase{name: "compiler fails before output check", stdout: language.stdout, compilerExit: 11, wantExit: 11})
			}
			if language.configHelper != "" {
				tests = append(tests, smokeResultCase{name: "argument helper fails after valid flags", stdout: language.stdout, helperExit: 17, wantExit: 17})
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					workDir := t.TempDir()
					binDir := filepath.Join(workDir, "bin")
					if err := os.Mkdir(binDir, 0o755); err != nil {
						t.Fatal(err)
					}
					runtimeBody := "#!/bin/sh\nprintf '%s' \"${AONOHAKO_TEST_STDOUT}\"\nexit \"${AONOHAKO_TEST_RUNTIME_EXIT}\"\n"
					writeTool := func(name, body string) {
						t.Helper()
						if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					if language.runtime != "" {
						writeTool(language.runtime, runtimeBody)
					}
					if language.configHelper != "" {
						writeTool(language.configHelper, "#!/bin/sh\nprintf '%s\\n' '-Iinclude -Llib -lpython'\nexit \"${AONOHAKO_TEST_HELPER_EXIT}\"\n")
					}
					for _, compiler := range language.compilers {
						writeTool(compiler, "#!/bin/sh\nif [ \"${AONOHAKO_TEST_COMPILER_EXIT}\" != 0 ]; then exit \"${AONOHAKO_TEST_COMPILER_EXIT}\"; fi\ncat > Main <<'RUNTIME'\n"+runtimeBody+"RUNTIME\nchmod 0755 Main\n")
					}
					for _, auxiliary := range language.auxiliaries {
						writeTool(auxiliary, "#!/bin/sh\nexit 0\n")
					}
					command := append([]string(nil), catalog.Languages[language.name].Smoke.Command...)
					// Login shells may replace the inherited PATH; set the controlled
					// toolchain search path in the command body instead.
					command[len(command)-1] = "export PATH='" + strings.ReplaceAll(binDir, "'", "'\"'\"'") + ":'\"${PATH}\"\n" + command[len(command)-1]
					cmd := exec.Command(command[0], command[1:]...)
					cmd.Dir = workDir
					cmd.Env = append(os.Environ(),
						"AONOHAKO_TEST_STDOUT="+tc.stdout,
						fmt.Sprintf("AONOHAKO_TEST_RUNTIME_EXIT=%d", tc.runtimeExit),
						fmt.Sprintf("AONOHAKO_TEST_COMPILER_EXIT=%d", tc.compilerExit),
						fmt.Sprintf("AONOHAKO_TEST_HELPER_EXIT=%d", tc.helperExit),
					)
					output, err := cmd.CombinedOutput()
					exitCode := 0
					if err != nil {
						if exitErr, ok := err.(*exec.ExitError); ok {
							exitCode = exitErr.ExitCode()
						} else {
							t.Fatalf("run smoke: %v\n%s", err, output)
						}
					}
					if exitCode != tc.wantExit {
						t.Fatalf("smoke exit = %d, want %d\n%s", exitCode, tc.wantExit, output)
					}
				})
			}
		})
	}
}
