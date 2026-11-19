package durante

import (
	"context"

	"aonohako/internal/cpupolicy"
	"os"
	"slices"
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
func TestDuranteConfigIsOptIn(t *testing.T) {
	cfg, err := ParseConfig("", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Family("CPP20"); err == nil {
		t.Fatal("default enables durante")
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
	launcher := runtimeInstallationFixture(t, dir)
	client := runtimeClientFixture(t, dir)
	cfg, _ := ParseConfig("cpp", launcher, client, DispatchGLIBC241)
	s, err := prepare(context.Background(), dir, cfg, "CPP20", os.Geteuid(), os.Getegid(), func(context.Context) (string, error) { return "2.41", nil })
	if err != nil {
		t.Fatal(err)
	}
	command := []string{"/tmp/target", "one two", "--flag"}
	wrapped := s.Wrap(command)
	want := []string{launcher, "-quiet", "-follow_children", "-disable_traces", "-vm_size", "32M", "-vmheap_size", "32M", "-c", client, "-outdir", s.dir, "-rep_bytes", "--", "/tmp/target", "one two", "--flag"}
	if !slices.Equal(wrapped, want) {
		t.Fatalf("unexpected fixed engine invocation: %q", wrapped)
	}
	if strings.Join(wrapped[len(wrapped)-3:], "|") != strings.Join(command, "|") {
		t.Fatal("target arguments changed")
	}
	argv, env, err := s.Runtime("binary", command, []string{"PATH=/bin", "GLIBC_TUNABLES=unreviewed"})
	if err != nil || !slices.Equal(argv, command) || len(env) != 2 || env[1] != "GLIBC_TUNABLES="+portableGLIBC241 {
		t.Fatal("wrong fixed environment")
	}
	_, err = prepare(context.Background(), dir, cfg, "CPP20", os.Geteuid(), os.Getegid(), func(context.Context) (string, error) { return "2.36", nil })
	if err == nil {
		t.Fatal("accepted unreviewed libc")
	}
}

func TestCPUFeaturePolicySessionUsesConfiguredRuntimeWithoutMeasurementOptIn(t *testing.T) {
	dir := t.TempDir()
	launcher := runtimeInstallationFixture(t, dir)
	client := runtimeClientFixture(t, dir)
	// A policy-only request does not select a measurement family or impose
	// the separately configured measurement dispatch environment.
	cfg, err := ParseConfig("", launcher, client, DispatchGLIBC241)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), dir, cfg, "CPP20", os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("ordinary measurement became implicitly enabled")
	}
	session, err := PrepareCPUFeaturePolicy(context.Background(), dir, cfg, os.Geteuid(), os.Getegid())
	if err != nil {
		t.Fatal(err)
	}
	if session.config.DispatchProfile != DispatchDefault || session.family != "" {
		t.Fatalf("policy-only session acquired a measurement profile: %+v", session)
	}
	features := []cpupolicy.Feature{cpupolicy.SIMD, cpupolicy.AVX}
	if err := session.SetDisallowedCPUFeatures(features); err != nil {
		t.Fatal(err)
	}
	features[0] = cpupolicy.AVX
	command := []string{"/tmp/target", "one two", "--flag"}
	want := []string{launcher, "-quiet", "-follow_children", "-disable_traces", "-vm_size", "32M", "-vmheap_size", "32M", "-c", client, "-outdir", session.dir, "-rep_bytes", "-disallow_cpu_feature", "avx", "-disallow_cpu_feature", "simd", "--", "/tmp/target", "one two", "--flag"}
	if wrapped := session.Wrap(command); !slices.Equal(wrapped, want) {
		t.Fatalf("native restrictions did not share one bounded runtime invocation: %q", wrapped)
	}
	for _, invalid := range [][]cpupolicy.Feature{{cpupolicy.AVX, cpupolicy.AVX}, {"AVX"}, {"avx2"}} {
		if err := session.SetDisallowedCPUFeatures(invalid); err == nil {
			t.Fatalf("accepted invalid policy %v", invalid)
		}
		if !slices.Equal(session.Wrap(command), want) {
			t.Fatal("failed policy update changed existing options")
		}
	}
	if err := session.SetDisallowedCPUFeatures(nil); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(session.Wrap(command), "-disallow_cpu_feature") {
		t.Fatal("empty policy changed ordinary client invocation")
	}
}

func TestCPUFeaturePolicySessionRequiresPreparedTrustedInstallation(t *testing.T) {
	for _, session := range []*Session{nil, {}} {
		if err := session.SetDisallowedCPUFeatures([]cpupolicy.Feature{cpupolicy.AVX}); err == nil {
			t.Fatal("unprepared session accepted a policy")
		}
	}
	dir := t.TempDir()
	launcher := runtimeInstallationFixture(t, dir)
	client := runtimeClientFixture(t, dir)
	cfg, err := ParseConfig("", launcher, client, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(client, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCPUFeaturePolicy(context.Background(), dir, cfg, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("policy-only session accepted a writable client")
	}
	cfg.Client = "relative-client.so"
	if _, err := PrepareCPUFeaturePolicy(context.Background(), dir, cfg, os.Geteuid(), os.Getegid()); err == nil {
		t.Fatal("policy-only session accepted a relative client")
	}
}
