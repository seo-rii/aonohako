package runtimepacks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func uhmlangCatalogEntry(t *testing.T) LanguageSpec {
	t.Helper()
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	language, ok := catalog.Languages["uhmlang"]
	if !ok {
		t.Fatal("UHMLANG is missing from the runtime catalog")
	}
	return language
}

func TestUHMLANGPinnedFork(t *testing.T) {
	script := strings.Join(uhmlangCatalogEntry(t).Install.Script, "\n")
	if !regexp.MustCompile(`(?m)^\s*revision=[0-9a-f]{40}$`).MatchString(script) {
		t.Fatal("UHMLANG source must be pinned to a complete commit SHA")
	}
	for _, required := range []string{
		"https://github.com/seo-rii/umjunsik-lang.git",
		`fetch --quiet --depth=1 origin "$revision"`,
		`test "$(git -C "$work/source" rev-parse HEAD)" = "$revision"`,
		`UHMLANG_BINARY="$work/umjunsik-lang-go"`,
		"go test -count=1 -timeout=120s ./...",
		`trap 'rm -rf -- "$work"' EXIT`,
		"/usr/local/share/licenses/umjunsik-lang-go/LICENSE",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("installer missing %q", required)
		}
	}
	for _, forbidden := range []string{"rycont/umjunsik-lang.git", "judge.patch", "git apply", "sed -i"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("consumer-side patching returned: %q", forbidden)
		}
	}
	if strings.Index(script, "go test ") > strings.Index(script, "install -m 0755") {
		t.Fatal("regressions must pass before the interpreter is installed")
	}
}

// Exercise the actual catalog smoke script, not a separately maintained copy.
func TestUHMLANGSmokeFailureHandling(t *testing.T) {
	for _, tool := range []string{"bash", "timeout", "sleep"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	smoke := uhmlangCatalogEntry(t).Smoke.Command
	if len(smoke) != 3 || smoke[0] != "bash" || smoke[1] != "-lc" {
		t.Fatalf("unexpected smoke command: %q", smoke)
	}
	fake := filepath.Join(t.TempDir(), "interpreter")
	stub := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == Main.uhm ]]; then cat > /dev/null; fi
case "$1" in
  Hello.uhm) result=X ;;
  Main.uhm) result=6 ;;
  Jump.uhm|Blank.uhm) result=2 ;;
  Mixed.uhm) result=$'4\n4' ;;
  *) exit 99 ;;
esac
if [[ "$1" == "${UHMLANG_FAIL_SOURCE:-}" && "${UHMLANG_FAIL_MODE:-}" == wrong ]]; then
  result=wrong
fi
printf '%s' "$result"
if [[ "$1" == "${UHMLANG_FAIL_SOURCE:-}" ]]; then
  case "${UHMLANG_FAIL_MODE:-}" in
    exit) exit 7 ;;
    timeout) exec sleep 30 ;;
  esac
fi
`
	if err := os.WriteFile(fake, []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(smoke[2], "/usr/bin/umjunsik-lang-go", shellQuoteUHMLANG(fake))
	// Real timeout behavior, with a short deadline only in this injection test.
	script = strings.ReplaceAll(script, "timeout 5s", "timeout 0.2s")
	check := func(t *testing.T, source, mode string, wantExit int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-lc", script)
		cmd.Dir = t.TempDir()
		cmd.Env = append(os.Environ(), "UHMLANG_FAIL_SOURCE="+source, "UHMLANG_FAIL_MODE="+mode)
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("smoke script hung: %v\n%s", ctx.Err(), output)
		}
		gotExit := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				gotExit = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if gotExit != wantExit {
			t.Fatalf("exit=%d, want %d; output=%q", gotExit, wantExit, output)
		}
	}
	t.Run("success", func(t *testing.T) { check(t, "", "", 0) })
	for _, source := range []string{"Hello.uhm", "Main.uhm", "Jump.uhm", "Blank.uhm", "Mixed.uhm"} {
		for _, failure := range []struct {
			mode string
			exit int
		}{{"wrong", 1}, {"exit", 7}, {"timeout", 124}} {
			t.Run(source+"/"+failure.mode, func(t *testing.T) { check(t, source, failure.mode, failure.exit) })
		}
	}
}

func shellQuoteUHMLANG(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// Opt-in network test. CI executes the catalog installer and the smoke command
// with only tool/output paths relocated; no interpreter source is patched here.
func TestUHMLANGForkBuild(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_UHMLANG_LIVE") != "1" {
		t.Skip("set AONOHAKO_TEST_UHMLANG_LIVE=1 to build and test the pinned fork")
	}
	language := uhmlangCatalogEntry(t)
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	gofmtBinary, err := exec.LookPath("gofmt")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "umjunsik-lang-go")
	alias := filepath.Join(dir, "umjunsik-lang-go-alias")
	license := filepath.Join(dir, "LICENSE")
	replace := strings.NewReplacer(
		"/usr/local/go/bin/gofmt", shellQuoteUHMLANG(gofmtBinary),
		"/usr/local/go/bin/go", shellQuoteUHMLANG(goBinary),
		"/usr/local/bin/umjunsik-lang-go", shellQuoteUHMLANG(binary),
		"/usr/bin/umjunsik-lang-go", shellQuoteUHMLANG(alias),
		"/usr/local/bin/gofmt", shellQuoteUHMLANG(filepath.Join(dir, "gofmt")),
		"/usr/local/bin/go", shellQuoteUHMLANG(filepath.Join(dir, "go")),
		"/usr/local/share/licenses/umjunsik-lang-go/LICENSE", shellQuoteUHMLANG(license),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", replace.Replace(strings.Join(language.Install.Script, "\n")))
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fork installer: %v\n%s", err, output)
	}
	t.Logf("fork installer and native regressions:\n%s", output)
	if _, err := os.Stat(license); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(alias)
	if err != nil || resolved != binary {
		t.Fatalf("entry point: %q (%v)", resolved, err)
	}
	smoke := language.Smoke.Command
	if len(smoke) != 3 {
		t.Fatalf("unexpected smoke command: %q", smoke)
	}
	cmd = exec.CommandContext(ctx, smoke[0], smoke[1], replace.Replace(smoke[2]))
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installed interpreter smoke: %v\n%s", err, output)
	}
}
