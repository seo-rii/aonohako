package workmeter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCPPFamilyRequiresSourceProvenance(t *testing.T) {
	for _, language := range []string{"C", "C89", "C23", "CPP", "CPP98", "CPP26", "cpp20"} {
		if family, ok := FamilyForLanguage(language); !ok || family != "cpp" {
			t.Fatalf("%s: %s %v", language, family, ok)
		}
	}
	for _, language := range []string{"binary", "unknown"} {
		if family, ok := FamilyForLanguage(language); ok {
			t.Fatalf("ambiguous language %s matched %s", language, family)
		}
	}
	for _, language := range []string{"go-binary", "RUST", "PYTHON3", "PYPY3"} {
		if family, ok := FamilyForLanguage(language); ok && family == "cpp" {
			t.Fatalf("non-C/C++ language %s matched C/C++", language)
		}
	}
}
func TestWorkmeterConfigIsOptIn(t *testing.T) {
	cfg, err := ParseConfig("", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Family("CPP20"); err == nil {
		t.Fatal("default enables workmeter")
	}
	cfg, err = ParseConfig("cpp", "", "", DispatchGLIBC241)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cfg.Family("CPP20"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"unknown", "cpp,cpp", "cpp,"} {
		if _, err = ParseConfig(value, "", "", ""); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if _, err = ParseConfig("cpp", "relative", "", ""); err == nil {
		t.Fatal("accepted relative executable")
	}
	if _, err = ParseConfig("cpp", "", "", "guess"); err == nil {
		t.Fatal("accepted unknown dispatch")
	}
}
func TestSessionPinsEnvironmentAndPreservesArguments(t *testing.T) {
	dir := t.TempDir()
	launcher := filepath.Join(dir, "drrun")
	client := filepath.Join(dir, "client.so")
	os.WriteFile(launcher, []byte("x"), 0700)
	os.WriteFile(client, []byte("x"), 0600)
	cfg, _ := ParseConfig("cpp", launcher, client, DispatchGLIBC241)
	s, err := prepare(context.Background(), dir, cfg, "CPP20", os.Geteuid(), os.Getegid(), func(context.Context) (string, error) { return "2.41", nil })
	if err != nil {
		t.Fatal(err)
	}
	command := []string{"/tmp/target", "one two", "--flag"}
	wrapped := s.Wrap(command)
	if strings.Join(wrapped[len(wrapped)-3:], "|") != strings.Join(command, "|") {
		t.Fatal("target arguments changed")
	}
	env := s.Environment([]string{"PATH=/bin", "GLIBC_TUNABLES=unreviewed"})
	if len(env) != 2 || env[1] != "GLIBC_TUNABLES="+portableGLIBC241 {
		t.Fatal("wrong fixed environment")
	}
	_, err = prepare(context.Background(), dir, cfg, "CPP20", os.Geteuid(), os.Getegid(), func(context.Context) (string, error) { return "2.36", nil })
	if err == nil {
		t.Fatal("accepted unreviewed libc")
	}
}
