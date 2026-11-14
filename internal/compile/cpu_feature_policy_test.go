package compile

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/model"
)

// Only fixed test fixtures use this runner; production always uses the sandbox.
type cpuPolicyFixtureRunner struct{}

func (cpuPolicyFixtureRunner) Run(ctx context.Context, dir, bin string, args, env []string) CommandResult {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	result := CommandResult{Status: model.CompileStatusOK}
	if err := cmd.Run(); err != nil {
		result.Status, result.Reason = model.CompileStatusCompileError, err.Error()
	}
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	return result
}

func TestCPUFeaturePolicyWithRealCompilers(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("CPU feature policy requires Linux x86-64")
	}
	for _, bin := range []string{"gcc", "g++", "objdump"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	avxSource := `#pragma GCC target("avx2")
#include <immintrin.h>
void twice(int *p) {
    __m256i v = _mm256_loadu_si256((const __m256i *)p);
    _mm256_storeu_si256((__m256i *)p, _mm256_add_epi32(v, v));
}
int main(void) { return 0; }
`
	for _, tc := range []struct {
		name, lang, source string
		features           []cpupolicy.Feature
		helpers            map[string]string
		wantViolation      bool
	}{
		{name: "default permits AVX pragma", lang: "C11", source: avxSource},
		{name: "AVX pragma", lang: "C11", source: avxSource, features: []cpupolicy.Feature{cpupolicy.AVX}, wantViolation: true},
		{name: "SIMD includes AVX", lang: "CPP17", source: avxSource, features: []cpupolicy.Feature{cpupolicy.SIMD}, wantViolation: true},
		{name: "ordinary C floating point", lang: "C11", source: `#include <stdio.h>
double calc(double a, double b) { return a + b * b; }
int main(void) { double a, b; if (scanf("%lf%lf", &a, &b) != 2) return 1; printf("%f\n", calc(a, b)); }
`, features: []cpupolicy.Feature{cpupolicy.SIMD}},
		{name: "ordinary C++ standard library", lang: "CPP17", source: `#include <iostream>
int main() { double a, b; std::cin >> a >> b; std::cout << a + b * b << '\n'; }
`, features: []cpupolicy.Feature{cpupolicy.SIMD}},
		{name: "auto-vectorized loop becomes scalar", lang: "C11", source: `void add(int *restrict a, const int *restrict b) { for (int i=0; i<1024; ++i) a[i] += b[i]; }
int main(void) { return 0; }
`, features: []cpupolicy.Feature{cpupolicy.SIMD}},
		{name: "SSE intrinsic", lang: "CPP17", source: `#include <emmintrin.h>
void twice(int *p) { __m128i v = _mm_loadu_si128((const __m128i *)p); _mm_storeu_si128((__m128i *)p, _mm_add_epi32(v, v)); }
int main() { return 0; }
`, features: []cpupolicy.Feature{cpupolicy.SIMD}, wantViolation: true},
		{name: "AVX restriction allows SSE", lang: "CPP17", source: `#include <emmintrin.h>
void twice(int *p) { __m128i v = _mm_loadu_si128((const __m128i *)p); _mm_storeu_si128((__m128i *)p, _mm_add_epi32(v, v)); }
int main() { return 0; }
`, features: []cpupolicy.Feature{cpupolicy.AVX}},
		{name: "vector extension", lang: "C11", source: `typedef int vec __attribute__((vector_size(16)));
vec add(vec a, vec b) { return a + b; }
int main(void) { return 0; }
`, features: []cpupolicy.Feature{cpupolicy.SIMD}, wantViolation: true},
		{name: "target attribute", lang: "C11", source: `#include <immintrin.h>
__attribute__((target("avx2"))) void twice(int *p) { __m256i v = _mm256_loadu_si256((const __m256i *)p); _mm256_storeu_si256((__m256i *)p, _mm256_add_epi32(v, v)); }
int main(void) { return 0; }
`, features: []cpupolicy.Feature{cpupolicy.AVX}, wantViolation: true},
		{name: "inline assembly", lang: "C11", source: `int main(void) { __asm__ volatile("vzeroupper"); return 0; }
`, features: []cpupolicy.Feature{cpupolicy.AVX}, wantViolation: true},
		{name: "raw instruction bytes", lang: "C11", source: `int main(void) { __asm__ volatile(".byte 0xc5, 0xf8, 0x77"); return 0; }
`, features: []cpupolicy.Feature{cpupolicy.AVX}, wantViolation: true},
		{name: "submitted header", lang: "CPP17", source: "#include \"helper.h\"\nint main() { return 0; }\n", helpers: map[string]string{"helper.h": `void vector_code() { __asm__ volatile("vzeroupper"); }`}, features: []cpupolicy.Feature{cpupolicy.AVX}, wantViolation: true},
		{name: "second translation unit", lang: "C11", source: "int main(void) { return 0; }\n", helpers: map[string]string{"helper.c": `void vector_code(void) { __asm__ volatile("vzeroupper"); }`}, features: []cpupolicy.Feature{cpupolicy.AVX}, wantViolation: true},
		{name: "multiple scalar translation units", lang: "C11", source: "int answer(void); int main(void) { return answer() != 42; }\n", helpers: map[string]string{"helper.c": `int answer(void) { return 42; }`}, features: []cpupolicy.Feature{cpupolicy.AVX, cpupolicy.SIMD}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			profile, ok := resolveProfile(tc.lang)
			if !ok {
				t.Fatal("fixture profile missing")
			}
			name := "Main.c"
			if profile.CompileKind == "cpp" {
				name = "Main.cpp"
			}
			sources := []model.Source{{Name: name, DataB64: b64String(tc.source)}}
			for name, source := range tc.helpers {
				sources = append(sources, model.Source{Name: name, DataB64: b64String(source)})
			}
			if err := materializeSources(workDir, sources); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			response := compileRegistry[profile.CompileKind].Compile(ctx, CompileJob{
				WorkDir: workDir, Target: "Main", Profile: profile,
				Request: &model.CompileRequest{Lang: tc.lang, Sources: sources, DisallowedCPUFeatures: tc.features},
				Runner:  cpuPolicyFixtureRunner{},
			})
			if tc.wantViolation {
				if response.Status != model.CompileStatusCompileError || response.ReasonCode != "disallowed_cpu_feature" || len(response.Artifacts) != 0 {
					t.Fatalf("expected policy rejection without artifacts: status=%s reason=%s code=%s stderr=%s", response.Status, response.Reason, response.ReasonCode, response.Stderr)
				}
			} else if response.Status != model.CompileStatusOK || len(response.Artifacts) != 1 || response.Artifacts[0].Name != "Main" {
				t.Fatalf("expected compiled executable: status=%s reason=%s code=%s stderr=%s", response.Status, response.Reason, response.ReasonCode, response.Stderr)
			}
		})
	}
}

type cpuPolicySequenceRunner struct {
	t        *testing.T
	results  []CommandResult
	commands []recordedCommand
}

func (r *cpuPolicySequenceRunner) Run(_ context.Context, workDir, bin string, args, env []string) CommandResult {
	r.t.Helper()
	r.commands = append(r.commands, recordedCommand{workDir: workDir, bin: bin, args: slices.Clone(args), env: slices.Clone(env)})
	i := len(r.commands) - 1
	if i >= len(r.results) {
		r.t.Fatalf("unexpected command %s %v", bin, args)
	}
	if bin == "gcc" && !slices.Contains(args, "-c") && r.results[i].Status == model.CompileStatusOK {
		if err := os.WriteFile(filepath.Join(workDir, "Main"), []byte("fixture artifact"), 0o755); err != nil {
			r.t.Fatal(err)
		}
	}
	return r.results[i]
}

func TestCPUFeaturePolicyInspectionFailuresNeverLink(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("requires Linux x86-64")
	}
	for _, tc := range []struct {
		name       string
		inspection CommandResult
	}{
		{"missing objdump", CommandResult{Status: model.CompileStatusInternal, Reason: "not found"}},
		{"timeout", CommandResult{Status: model.CompileStatusTimeout, Reason: "context deadline exceeded"}},
		{"truncated output", CommandResult{Status: model.CompileStatusOK, Stdout: strings.Repeat(" ", compileOutputCaptureBytes+1)}},
		{"empty output", CommandResult{Status: model.CompileStatusOK}},
		{"inspector warning", CommandResult{Status: model.CompileStatusOK, Stderr: "warning: corrupt section"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &cpuPolicySequenceRunner{t: t, results: []CommandResult{
				{Status: model.CompileStatusOK, Stdout: "compiler stdout", Stderr: "compiler warning"}, tc.inspection,
			}}
			profile, _ := resolveProfile("C11")
			response := compileRegistry["c"].Compile(context.Background(), CompileJob{
				WorkDir: t.TempDir(), Target: "Main", Profile: profile,
				Request: &model.CompileRequest{Lang: "C11", Sources: []model.Source{{Name: "Main.c"}}, DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX}}, Runner: runner,
			})
			if response.Status == model.CompileStatusOK || response.ReasonCode != "cpu_feature_inspection_failed" || len(response.Artifacts) != 0 || len(runner.commands) != 2 {
				t.Fatalf("response=%+v commands=%+v", response, runner.commands)
			}
			if response.Stdout != "compiler stdout" || !strings.HasPrefix(response.Stderr, "compiler warning") {
				t.Fatalf("compiler diagnostics lost: %+v", response)
			}
		})
	}
}

func TestCPUFeaturePolicyLinksInspectedObjectsWithoutRecompiling(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("requires Linux x86-64")
	}
	assembly := "\nfixture.o:     file format elf64-x86-64\n\nDisassembly of section .text:\n\n0000000000000000 <main>:\n   0:\txor    %eax,%eax\n   2:\tret\n"
	runner := &cpuPolicySequenceRunner{t: t, results: []CommandResult{
		{Status: model.CompileStatusOK}, {Status: model.CompileStatusOK, Stdout: assembly},
		{Status: model.CompileStatusOK}, {Status: model.CompileStatusOK, Stdout: assembly},
		{Status: model.CompileStatusOK},
	}}
	profile, _ := resolveProfile("C11")
	response := compileRegistry["c"].Compile(context.Background(), CompileJob{
		WorkDir: t.TempDir(), Target: "Main", Profile: profile,
		Request: &model.CompileRequest{Lang: "C11", Sources: []model.Source{{Name: "Main.c"}, {Name: "helper.c"}}, DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.SIMD}}, Runner: runner,
	})
	if response.Status != model.CompileStatusOK || len(response.Artifacts) != 1 || response.Stdout != "" {
		t.Fatalf("response=%+v", response)
	}
	link := runner.commands[4]
	for _, index := range []int{0, 2} {
		compile := runner.commands[index]
		object := compile.args[len(compile.args)-1]
		inspection := runner.commands[index+1]
		if inspection.bin != "objdump" || inspection.args[len(inspection.args)-1] != object || !slices.Contains(link.args, object) {
			t.Fatalf("link did not use inspected object: %+v", runner.commands)
		}
	}
	for _, arg := range link.args {
		if strings.HasSuffix(arg, ".c") {
			t.Fatalf("link recompiles source: %v", link.args)
		}
	}
	if !slices.Contains(link.args, "-fno-lto") {
		t.Fatalf("LTO not disabled: %v", link.args)
	}
}

func TestServiceRejectsCPUFeaturePolicyBeforeMaterialization(t *testing.T) {
	for _, tc := range []struct {
		lang     string
		features []cpupolicy.Feature
	}{
		{"C11", []cpupolicy.Feature{"avx2"}},
		{"C11", []cpupolicy.Feature{cpupolicy.AVX, cpupolicy.AVX}},
		{"PYTHON3", []cpupolicy.Feature{cpupolicy.SIMD}},
	} {
		response := New().Run(context.Background(), &model.CompileRequest{
			Lang: tc.lang, DisallowedCPUFeatures: tc.features,
			Sources: []model.Source{{Name: "Main.c", DataB64: "invalid base64"}},
		})
		if response.Status != model.CompileStatusInvalid || !strings.Contains(response.Reason, "disallowed_cpu_features") {
			t.Fatalf("request reached source materialization: %+v", response)
		}
	}
}

func TestCPUFeaturePolicyThroughCompileSandbox(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("requires Linux x86-64")
	}
	for _, bin := range []string{"gcc", "objdump"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	for _, tc := range []struct {
		name, source, status, code string
	}{
		{"scalar", "int main(void) { return 0; }", model.CompileStatusOK, ""},
		{"AVX", `int main(void) { __asm__ volatile("vzeroupper"); return 0; }`, model.CompileStatusCompileError, "disallowed_cpu_feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := New().Run(context.Background(), &model.CompileRequest{
				Lang: "C11", Sources: []model.Source{{Name: "Main.c", DataB64: b64String(tc.source)}},
				DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX},
			})
			if response.Status != tc.status || response.ReasonCode != tc.code {
				t.Fatalf("sandbox compile status=%s code=%s reason=%s stderr=%s", response.Status, response.ReasonCode, response.Reason, response.Stderr)
			}
		})
	}
}
