package compile

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/profiles"
	"aonohako/internal/workspacequota"
	"golang.org/x/sys/unix"
)

func b64String(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func b64Bytes(v []byte) string {
	return base64.StdEncoding.EncodeToString(v)
}

func TestBuildEmbeddedRunnerPassesCgroupParent(t *testing.T) {
	runner, err := Build(config.Config{
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetSelfHosted,
				ExecutionTransport: platform.ExecutionTransportEmbedded,
				SandboxBackend:     platform.SandboxBackendHelper,
			},
			Cgroup: config.CgroupConfig{ParentDir: "/sys/fs/cgroup/aonohako"},
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	service, ok := runner.(*Service)
	if !ok {
		t.Fatalf("Build() returned %T, want *Service", runner)
	}
	if service.cgroupParentDir != "/sys/fs/cgroup/aonohako" {
		t.Fatalf("cgroupParentDir = %q", service.cgroupParentDir)
	}
}

func TestBuildRejectsSelfHostedEmbeddedRunnerWithoutCgroupParent(t *testing.T) {
	_, err := Build(config.Config{
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetSelfHosted,
				ExecutionTransport: platform.ExecutionTransportEmbedded,
				SandboxBackend:     platform.SandboxBackendHelper,
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a cgroup parent") {
		t.Fatalf("Build() error = %v", err)
	}
}

func sandboxWritableTempDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp(os.TempDir(), "aonohako-compile-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chown(dir, 65532, 65532); err != nil {
		t.Fatalf("Chown(%q): %v", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod(%q): %v", dir, err)
	}
	return dir
}

func TestRunRejectsInvalidTargetPath(t *testing.T) {
	svc := New()
	tests := []string{"../escape", "nested/Main", "/tmp/Main"}
	for _, target := range tests {
		resp := svc.Run(context.Background(), &model.CompileRequest{
			Lang:   "UHMLANG",
			Target: target,
			Sources: []model.Source{{
				Name:    "Main.uhm",
				DataB64: b64String("text"),
			}},
		})
		if resp.Status != model.CompileStatusInvalid {
			t.Fatalf("target=%q status=%q want=%q", target, resp.Status, model.CompileStatusInvalid)
		}
	}
}

func TestRunRejectsUnknownRuntimeProfile(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{RuntimeProfile: "missing"})
	if resp.Status != model.CompileStatusInvalid || !strings.Contains(resp.Reason, "unknown runtime_profile") {
		t.Fatalf("expected unknown runtime profile invalid response, got %+v", resp)
	}
}

func TestRunRejectsMissingCompileEntrypoint(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang:       "C11",
		EntryPoint: "src/missing.c",
		Sources: []model.Source{{
			Name:    "src/main.c",
			DataB64: b64String("int main(void) { return 0; }\n"),
		}},
	})
	if resp.Status != model.CompileStatusInvalid {
		t.Fatalf("status=%q want=%q response=%+v", resp.Status, model.CompileStatusInvalid, resp)
	}
	if !strings.Contains(resp.Reason, "entry_point") {
		t.Fatalf("expected entry_point validation reason, got %+v", resp)
	}
}

func TestCompileNativeBuildsMultipleCFiles(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not available")
	}

	workDir := sandboxWritableTempDir(t)
	sources := []model.Source{
		{
			Name:    "src/main.c",
			DataB64: b64String("#include \"add.h\"\n#include <stdio.h>\nint main(void) { printf(\"%d\\n\", add(2, 3)); return 0; }\n"),
		},
		{
			Name:    "src/add.c",
			DataB64: b64String("#include \"add.h\"\nint add(int a, int b) { return a + b; }\n"),
		},
		{
			Name:    "src/add.h",
			DataB64: b64String("int add(int a, int b);\n"),
		},
	}
	if err := materializeSources(workDir, sources); err != nil {
		t.Fatalf("materializeSources: %v", err)
	}

	resp := compileNative(context.Background(), workDir, "Main", gatherByExt(sources, ".c", ".h"), "gcc", []string{"-O2"})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("expected multi-file C compile to succeed, got status=%q reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("unexpected artifacts: %+v", resp.Artifacts)
	}
}

func TestRunCompilesNestedGoModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is unavailable")
	}
	resp := New().Run(context.Background(), &model.CompileRequest{
		Lang:       "GO",
		EntryPoint: "src/main.go",
		Sources: []model.Source{
			{Name: "src/go.mod", DataB64: b64String("module example.com/submission\n\ngo 1.22\n")},
			{Name: "src/main.go", DataB64: b64String("package main\nfunc main() {}\n")},
		},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("nested Go module response = %+v", resp)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("artifacts = %+v", resp.Artifacts)
	}
}

func TestRunRejectsOversizedSource(t *testing.T) {
	svc := New()
	large := bytes.Repeat([]byte("a"), 17<<20)
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "UHMLANG",
		Sources: []model.Source{{
			Name:    "Main.uhm",
			DataB64: b64Bytes(large),
		}},
	})
	if resp.Status != model.CompileStatusInvalid {
		t.Fatalf("status=%q want=%q", resp.Status, model.CompileStatusInvalid)
	}
}

func TestRunRejectsDuplicateSourcePath(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "UHMLANG",
		Sources: []model.Source{
			{Name: "Main.uhm", DataB64: b64String("first")},
			{Name: "Main.uhm", DataB64: b64String("second")},
		},
	})
	if resp.Status != model.CompileStatusInvalid || !strings.Contains(resp.Reason, "duplicate source path") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRunRejectsReservedWorkspaceSourcePath(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{
			{Name: ".tmp/Main.py", DataB64: b64String("print('reserved')\n")},
		},
	})
	if resp.Status != model.CompileStatusInvalid || !strings.Contains(resp.Reason, "reserved workspace directory") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRunRejectsTooManySources(t *testing.T) {
	svc := New()
	sources := make([]model.Source, 0, maxSourceFiles+1)
	for i := 0; i < maxSourceFiles+1; i++ {
		sources = append(sources, model.Source{
			Name:    "Main" + strconv.Itoa(i) + ".uhm",
			DataB64: b64String("text"),
		})
	}
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang:    "UHMLANG",
		Sources: sources,
	})
	if resp.Status != model.CompileStatusInvalid {
		t.Fatalf("status=%q want=%q", resp.Status, model.CompileStatusInvalid)
	}
	if !strings.Contains(resp.Reason, "too many sources") {
		t.Fatalf("expected too many sources reason, got %+v", resp)
	}
}

func TestResolveProfileSupportsNewLanguages(t *testing.T) {
	tests := map[string]struct {
		compileKind string
		runLang     string
	}{
		"asm":           {compileKind: "asm", runLang: "binary"},
		"aheui":         {compileKind: "none", runLang: "aheui"},
		"nasm":          {compileKind: "nasm", runLang: "binary"},
		"pascal":        {compileKind: "pascal", runLang: "binary"},
		"delphi":        {compileKind: "delphi", runLang: "binary"},
		"objectpascal":  {compileKind: "objectpascal", runLang: "binary"},
		"nim":           {compileKind: "nim", runLang: "binary"},
		"clojure":       {compileKind: "clojure", runLang: "clojure"},
		"racket":        {compileKind: "racket", runLang: "racket"},
		"scheme":        {compileKind: "scheme", runLang: "scheme"},
		"awk":           {compileKind: "awk", runLang: "awk"},
		"tcl":           {compileKind: "tcl", runLang: "tcl"},
		"gdl":           {compileKind: "gdl", runLang: "gdl"},
		"octave":        {compileKind: "octave", runLang: "octave"},
		"ada":           {compileKind: "ada", runLang: "binary"},
		"cobol":         {compileKind: "cobol", runLang: "binary"},
		"gnucobol":      {compileKind: "cobol", runLang: "binary"},
		"cython":        {compileKind: "cython", runLang: "binary"},
		"dart":          {compileKind: "dart", runLang: "binary"},
		"haskell":       {compileKind: "haskell", runLang: "binary"},
		"idris2":        {compileKind: "idris2", runLang: "binary"},
		"sml":           {compileKind: "sml", runLang: "binary"},
		"haxe":          {compileKind: "haxe", runLang: "haxe"},
		"swift":         {compileKind: "swift", runLang: "binary"},
		"sqlite":        {compileKind: "sqlite", runLang: "sqlite"},
		"julia":         {compileKind: "julia", runLang: "julia"},
		"raku":          {compileKind: "raku", runLang: "raku"},
		"erlang":        {compileKind: "erlang", runLang: "erlang"},
		"mercury":       {compileKind: "mercury", runLang: "binary"},
		"prolog":        {compileKind: "prolog", runLang: "prolog"},
		"r":             {compileKind: "r", runLang: "r"},
		"groovy":        {compileKind: "groovy", runLang: "groovy"},
		"fortan":        {compileKind: "fortran", runLang: "binary"},
		"d":             {compileKind: "d", runLang: "binary"},
		"objective-c":   {compileKind: "objective-c", runLang: "binary"},
		"objective-cpp": {compileKind: "objective-cpp", runLang: "binary"},
		"coq":           {compileKind: "rocq", runLang: "rocq"},
		"rocq":          {compileKind: "rocq", runLang: "rocq"},
		"lean4":         {compileKind: "lean4", runLang: "lean4"},
		"agda":          {compileKind: "agda", runLang: "agda"},
		"dafny":         {compileKind: "dafny", runLang: "dafny"},
		"tla":           {compileKind: "tla", runLang: "tla"},
		"why3":          {compileKind: "why3", runLang: "why3"},
		"isabelle":      {compileKind: "isabelle", runLang: "isabelle"},
		"fstar":         {compileKind: "fstar", runLang: "fstar"},
		"alloy":         {compileKind: "alloy", runLang: "alloy"},
		"acl2":          {compileKind: "acl2", runLang: "acl2"},
		"kframework":    {compileKind: "kframework", runLang: "kframework"},
		"vhdl":          {compileKind: "vhdl", runLang: "vhdl"},
		"verilog":       {compileKind: "verilog", runLang: "verilog"},
		"crystal":       {compileKind: "crystal", runLang: "binary"},
		"vala":          {compileKind: "vala", runLang: "binary"},
		"vlang":         {compileKind: "vlang", runLang: "binary"},
		"odin":          {compileKind: "odin", runLang: "binary"},
		"c3":            {compileKind: "c3", runLang: "c3"},
		"hare":          {compileKind: "hare", runLang: "binary"},
		"vbnet":         {compileKind: "vbnet", runLang: "vbnet"},
		"gleam":         {compileKind: "gleam", runLang: "gleam"},
		"cuda-ocelot":   {compileKind: "cuda-ocelot", runLang: "cuda-ocelot"},
		"carbon":        {compileKind: "carbon", runLang: "binary"},
		"graphql":       {compileKind: "graphql", runLang: "graphql"},
		"zig":           {compileKind: "zig", runLang: "binary"},
		"lisp":          {compileKind: "lisp", runLang: "lisp"},
		"picolisp":      {compileKind: "picolisp", runLang: "picolisp"},
		"scala":         {compileKind: "scala", runLang: "scala"},
		"fsharp":        {compileKind: "fsharp", runLang: "fsharp"},
		"whitespace":    {compileKind: "whitespace", runLang: "whitespace"},
		"befunge":       {compileKind: "befunge", runLang: "befunge"},
		"bf":            {compileKind: "brainfuck", runLang: "brainfuck"},
		"malbolge":      {compileKind: "malbolge", runLang: "malbolge"},
		"lolcode":       {compileKind: "lolcode", runLang: "lolcode"},
		"apecode":       {compileKind: "apecode", runLang: "binary"},
		"wasm":          {compileKind: "wasm", runLang: "wasm"},
		"vb6":           {compileKind: "vb6", runLang: "vb6"},
		"freebasic":     {compileKind: "freebasic", runLang: "binary"},
		"classic-basic": {compileKind: "classic-basic", runLang: "binary"},
		"qbasic":        {compileKind: "classic-basic", runLang: "binary"},
		"smalltalk":     {compileKind: "smalltalk", runLang: "smalltalk"},
		"golfscript":    {compileKind: "golfscript", runLang: "golfscript"},
		"mojo":          {compileKind: "mojo", runLang: "mojo-binary"},
		"moonbit":       {compileKind: "moonbit", runLang: "binary"},
		"zerolang":      {compileKind: "zerolang", runLang: "binary"},
		"deno":          {compileKind: "deno", runLang: "deno"},
		"elm":           {compileKind: "elm", runLang: "javascript"},
		"kotlin-jvm":    {compileKind: "kotlin-jvm", runLang: "kotlin-jvm"},
		"kotlin-java":   {compileKind: "kotlin-jvm", runLang: "kotlin-jvm"},
		"kotlin/java":   {compileKind: "kotlin-jvm", runLang: "kotlin-jvm"},
		"kotlin-java17": {compileKind: "kotlin-jvm", runLang: "kotlin-jvm"},
		"duckdb":        {compileKind: "duckdb", runLang: "duckdb"},
		"bqn":           {compileKind: "bqn", runLang: "bqn"},
		"apl":           {compileKind: "apl", runLang: "apl"},
		"j":             {compileKind: "j", runLang: "j"},
		"uiua":          {compileKind: "uiua", runLang: "uiua"},
		"janet":         {compileKind: "janet", runLang: "janet"},
		"coffeescript":  {compileKind: "coffeescript", runLang: "javascript"},
		"rescript":      {compileKind: "rescript", runLang: "javascript"},
		"purescript":    {compileKind: "purescript", runLang: "javascript"},
		"sed":           {compileKind: "sed", runLang: "sed"},
		"bc":            {compileKind: "bc", runLang: "bc"},
		"forth":         {compileKind: "forth", runLang: "forth"},
		"gforth":        {compileKind: "forth", runLang: "forth"},
	}

	for input, want := range tests {
		profile, ok := resolveProfile(input)
		if !ok {
			t.Fatalf("resolveProfile(%q) reported unsupported language", input)
		}
		if profile.CompileKind != want.compileKind {
			t.Fatalf("resolveProfile(%q) compile kind = %q, want %q", input, profile.CompileKind, want.compileKind)
		}
		if profile.RunLang != want.runLang {
			t.Fatalf("resolveProfile(%q) run lang = %q, want %q", input, profile.RunLang, want.runLang)
		}
	}
}

func TestResolveProfileAcceptsLanguageAliases(t *testing.T) {
	tests := map[string]string{
		"assembly":        "asm",
		"gas":             "asm",
		"freepascal":      "pascal",
		"fpc":             "pascal",
		"nasm64":          "nasm",
		"scheme":          "scheme",
		"gawk":            "awk",
		"gnudatalanguage": "gdl",
		"systemverilog":   "verilog",
		"vb":              "vbnet",
		"lean":            "lean4",
		"tlaplus":         "tla",
		"whyml":           "why3",
		"f*":              "fstar",
		"f-star":          "fstar",
		"k":               "kframework",
		"k-framework":     "kframework",
		"objc":            "objective-c",
		"objcpp":          "objective-cpp",
		"qbasic":          "classic-basic",
		"gst":             "smalltalk",
		"gnu-apl":         "apl",
		"coffee":          "coffeescript",
		"cpp98":           "cpp",
		"c++98":           "cpp",
		"f95":             "fortran",
		"f2003":           "fortran",
		"f2008":           "fortran",
		"f2018":           "fortran",
		"ada12":           "ada",
		"ada22":           "ada",
		"kotlin_java":     "kotlin-jvm",
		"standard-ml":     "sml",
		"zero":            "zerolang",
	}

	for input, wantCompileKind := range tests {
		profile, ok := resolveProfile(input)
		if !ok {
			t.Fatalf("resolveProfile(%q) reported unsupported language", input)
		}
		if profile.CompileKind != wantCompileKind {
			t.Fatalf("resolveProfile(%q) compile kind = %q, want %q", input, profile.CompileKind, wantCompileKind)
		}
	}
}

func TestApplyRequestedVersionOverridesLanguageDefaults(t *testing.T) {
	tests := []struct {
		name      string
		lang      string
		version   string
		wantStd   string
		wantJava  string
		wantRust  string
		wantError bool
	}{
		{name: "c standard", lang: "C11", version: "c23", wantStd: "c23"},
		{name: "c gnu standard", lang: "C11", version: "gnu17", wantStd: "gnu17"},
		{name: "cpp standard", lang: "CPP17", version: "20", wantStd: "c++20"},
		{name: "cpp gnu standard", lang: "CPP17", version: "gnu++23", wantStd: "gnu++23"},
		{name: "java release", lang: "JAVA11", version: "17", wantJava: "17"},
		{name: "java release prefix", lang: "JAVA11", version: "java21", wantJava: "21"},
		{name: "kotlin jvm release alias", lang: "KOTLIN_JVM17", wantJava: "17"},
		{name: "kotlin jvm release", lang: "KOTLIN_JVM", version: "17", wantJava: "17"},
		{name: "kotlin java release prefix", lang: "KOTLIN_JAVA11", version: "java21", wantJava: "21"},
		{name: "kotlin jvm target prefix", lang: "KOTLIN_JVM", version: "jvm-target=11", wantJava: "11"},
		{name: "kotlin jvm 1.8 target", lang: "KOTLIN_JVM", version: "jvm_target=1.8", wantJava: "8"},
		{name: "rust edition", lang: "RUST2021", version: "edition2024", wantRust: "2024"},
		{name: "fortran standard", lang: "FORTRAN", version: "f2008", wantStd: "f2008"},
		{name: "fortran standard prefix", lang: "FORTRAN", version: "fortran2018", wantStd: "f2018"},
		{name: "ada standard", lang: "ADA", version: "ada2012", wantStd: "-gnat2012"},
		{name: "ada standard short", lang: "ADA", version: "22", wantStd: "-gnat2022"},
		{name: "unsupported versioned language", lang: "PYTHON3", version: "3.13", wantError: true},
		{name: "unsupported version value", lang: "RUST2021", version: "2030", wantError: true},
		{name: "unsupported kotlin jvm target", lang: "KOTLIN_JVM", version: "2030", wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			profile, ok := resolveProfile(tc.lang)
			if !ok {
				t.Fatalf("resolveProfile(%q) reported unsupported language", tc.lang)
			}
			got, err := applyRequestedVersion(profile, tc.version)
			if tc.wantError {
				if err == nil {
					t.Fatalf("applyRequestedVersion(%q, %q) succeeded, want error", tc.lang, tc.version)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyRequestedVersion(%q, %q) returned error: %v", tc.lang, tc.version, err)
			}
			if tc.wantStd != "" && got.CompileStd != tc.wantStd {
				t.Fatalf("CompileStd = %q, want %q", got.CompileStd, tc.wantStd)
			}
			if tc.wantJava != "" && got.JavaRelease != tc.wantJava {
				t.Fatalf("JavaRelease = %q, want %q", got.JavaRelease, tc.wantJava)
			}
			if tc.wantRust != "" && got.RustEdition != tc.wantRust {
				t.Fatalf("RustEdition = %q, want %q", got.RustEdition, tc.wantRust)
			}
		})
	}
}

func TestVersionedFortranAndAdaCompileArgs(t *testing.T) {
	fortranCompiler, ok := compileRegistry["fortran"].(nativeCompiler)
	if !ok {
		t.Fatalf("fortran compiler has unexpected type %T", compileRegistry["fortran"])
	}
	fortranArgs := fortranCompiler.flags(CompileJob{
		Profile: profiles.Profile{CompileStd: "f2008"},
	})
	if !strings.Contains(strings.Join(fortranArgs, " "), "-std=f2008") {
		t.Fatalf("fortran args = %v, want -std=f2008", fortranArgs)
	}

	adaCompiler, ok := compileRegistry["ada"].(singleSourceExecutableCompiler)
	if !ok {
		t.Fatalf("ada compiler has unexpected type %T", compileRegistry["ada"])
	}
	adaArgs := adaCompiler.args(CompileJob{
		WorkDir: "/work",
		Target:  "Main",
		Profile: profiles.Profile{CompileStd: "-gnat2022"},
	}, "Main.adb")
	if !strings.Contains(strings.Join(adaArgs, " "), "-gnat2022") {
		t.Fatalf("ada args = %v, want -gnat2022", adaArgs)
	}
}

func TestRunRejectsInvalidWhitespaceProgram(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "WHITESPACE",
		Sources: []model.Source{{
			Name:    "Main.ws",
			DataB64: b64String("not whitespace"),
		}},
	})
	if resp.Status != model.CompileStatusCompileError {
		t.Fatalf("status=%q want=%q", resp.Status, model.CompileStatusCompileError)
	}
}

func TestRunRejectsInvalidBrainfuckProgram(t *testing.T) {
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "BF",
		Sources: []model.Source{{
			Name:    "Main.bf",
			DataB64: b64String("++[>++<-"),
		}},
	})
	if resp.Status != model.CompileStatusCompileError {
		t.Fatalf("status=%q want=%q", resp.Status, model.CompileStatusCompileError)
	}
}

func encodeMalbolgeOpcodes(opcodes string) string {
	var source strings.Builder
	source.Grow(len(opcodes))
	for position, opcode := range []byte(opcodes) {
		index := strings.IndexByte(malbolgeXlat1, opcode)
		if index < 0 {
			panic(fmt.Sprintf("unsupported Malbolge test opcode %q", opcode))
		}
		source.WriteByte(byte(33 + (index-position%len(malbolgeXlat1)+len(malbolgeXlat1))%len(malbolgeXlat1)))
	}
	return source.String()
}

func TestRunAcceptsMalbolgeExtensionsAndIgnoresASCIIWhitespace(t *testing.T) {
	for _, extension := range []string{".mal", ".mb"} {
		t.Run(extension, func(t *testing.T) {
			program := encodeMalbolgeOpcodes("ov")
			program = program[:1] + " \t\r\n\v\f" + program[1:]
			resp := New().Run(context.Background(), &model.CompileRequest{
				Lang: "MALBOLGE",
				Sources: []model.Source{{
					Name:    "Main" + extension,
					DataB64: b64String(program),
				}},
			})
			if resp.Status != model.CompileStatusOK {
				t.Fatalf("status=%q reason=%q, want OK", resp.Status, resp.Reason)
			}
			if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main"+extension {
				t.Fatalf("artifacts=%+v, want preserved %s source", resp.Artifacts, extension)
			}
		})
	}
}

func TestRunAcceptsReferenceMalbolgeHelloWorld(t *testing.T) {
	if len(malbolgeXlat1) != 94 {
		t.Fatalf("Malbolge XLAT1 length = %d, want 94", len(malbolgeXlat1))
	}
	const source = "('&%:9]!~}|z2Vxwv-,POqponl$Hjig%eB@@>}=<M:9wv6WsU2T|nm-,jcL(I&%$#\"`CB]V?Tx<uVtT`Rpo3NlF.Jh++FdbCBA@?]!~|4XzyTT43Qsqq(Lnmkj\"Fhg${z@>"
	resp := New().Run(context.Background(), &model.CompileRequest{
		Lang: "MALBOLGE",
		Sources: []model.Source{{
			Name:    "Main.mal",
			DataB64: b64String(source),
		}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("reference program response=%+v, want OK", resp)
	}
}

func TestRunRejectsMalformedMalbolgePrograms(t *testing.T) {
	invalidOpcode := byte(0)
	for candidate := 33; candidate <= 126; candidate++ {
		if !strings.ContainsRune(validMalbolgeOpcodes, rune(malbolgeXlat1[candidate-33])) {
			invalidOpcode = byte(candidate)
			break
		}
	}
	if invalidOpcode == 0 {
		t.Fatal("could not construct invalid Malbolge opcode")
	}

	tests := []struct {
		name   string
		source string
		reason string
	}{
		{name: "too short", source: encodeMalbolgeOpcodes("v"), reason: "at least two instructions"},
		{name: "non graphical", source: encodeMalbolgeOpcodes("o") + "\x80", reason: "non-graphical ASCII"},
		{name: "invalid positional opcode", source: string([]byte{invalidOpcode}) + encodeMalbolgeOpcodes("v"), reason: "invalid opcode"},
		{name: "too long", source: encodeMalbolgeOpcodes(strings.Repeat("o", malbolgeMemorySize+1)), reason: "exceeds 59049 instructions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := New().Run(context.Background(), &model.CompileRequest{
				Lang: "MALBOLGE",
				Sources: []model.Source{{
					Name:    "Main.mal",
					DataB64: b64String(tc.source),
				}},
			})
			if resp.Status != model.CompileStatusCompileError || !strings.Contains(resp.Reason, tc.reason) {
				t.Fatalf("response=%+v, want Compile Error containing %q", resp, tc.reason)
			}
		})
	}
}

func TestRunPythonCompileSucceedsWithRootBackedSandboxWorkspace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop compile helper to sandbox user")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workRoot := t.TempDir()
	if err := os.Chmod(workRoot, 0o755); err != nil {
		t.Fatalf("Chmod(%q): %v", workRoot, err)
	}
	t.Setenv("AONOHAKO_EXECUTION_MODE", "local-root")
	t.Setenv("AONOHAKO_WORK_ROOT", workRoot)

	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: b64String("print('ok')\n"),
		}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("expected root-backed python compile to succeed, got status=%q reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
	}
	if len(resp.Artifacts) == 0 {
		t.Fatalf("expected compiled python artifacts")
	}
}

func TestRunPythonCompileDoesNotExecuteSitecustomize(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	originalMain := "print('ok')\n"
	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{
			{
				Name:    "Main.py",
				DataB64: b64String(originalMain),
			},
			{
				Name:    "sitecustomize.py",
				DataB64: b64String("from pathlib import Path\nPath('Main.py').write_text(\"print(\\\"pwned\\\")\\n\")\n"),
			},
		},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
	}

	artifacts := map[string]string{}
	for _, artifact := range resp.Artifacts {
		raw, err := base64.StdEncoding.DecodeString(artifact.DataB64)
		if err != nil {
			t.Fatalf("decode artifact %q: %v", artifact.Name, err)
		}
		artifacts[artifact.Name] = string(raw)
	}
	if got := artifacts["Main.py"]; got != originalMain {
		t.Fatalf("expected Main.py artifact to stay unchanged, got %q", got)
	}
	if got := artifacts["sitecustomize.py"]; got == "" {
		t.Fatalf("expected sitecustomize.py artifact to be preserved")
	}
}

func TestCompileCSharpMaterializesProjectSources(t *testing.T) {
	workDir := t.TempDir()
	_ = compileCSharp(context.Background(), workDir, []model.Source{
		{
			Name:    "src/App/App.csproj",
			DataB64: b64String("<Project Sdk=\"Microsoft.NET.Sdk\"></Project>"),
		},
		{
			Name:    "src/App/Program.cs",
			DataB64: b64String("class Program { static void Main() {} }"),
		},
	})

	if _, err := os.Stat(filepath.Join(workDir, "csproj", "src", "App", "App.csproj")); err != nil {
		t.Fatalf("expected App.csproj to be materialized, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "csproj", "src", "App", "Program.cs")); err != nil {
		t.Fatalf("expected Program.cs to preserve directory structure, err=%v", err)
	}
}

func TestCollectArtifactsRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.bin"), bytes.Repeat([]byte("x"), 20<<20), 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	if _, err := collectArtifacts(root, func(string) bool { return true }, ""); err == nil {
		t.Fatalf("expected oversized artifact error")
	}
}

func TestCollectArtifactsRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside.bin: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "artifact.bin")); err != nil {
		t.Fatalf("symlink artifact.bin: %v", err)
	}
	if _, err := collectArtifacts(root, func(string) bool { return true }, ""); err == nil {
		t.Fatalf("expected symlink artifact error")
	}
}

func TestCollectArtifactsRejectsHardlink(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.bin")
	if err := os.WriteFile(original, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write original.bin: %v", err)
	}
	if err := os.Link(original, filepath.Join(root, "artifact.bin")); err != nil {
		t.Fatalf("link artifact.bin: %v", err)
	}
	if _, err := collectArtifacts(root, func(string) bool { return true }, ""); err == nil {
		t.Fatalf("expected hardlink artifact error")
	}
}

func TestCollectArtifactsForCoqKeepsSourceArtifactForExecution(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"Main.v":    "Theorem same_folder_ok : 1 = 1.\nProof. reflexivity. Qed.\n",
		"Main.vo":   "vo",
		"Main.glob": "glob",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	artifacts, err := collectArtifacts(root, func(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".v") }, "")
	if err != nil {
		t.Fatalf("collectArtifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected only coq source artifact, got %+v", artifacts)
	}
	if artifacts[0].Name != "Main.v" {
		t.Fatalf("expected coq compile artifact Main.v, got %+v", artifacts)
	}
}

func TestRunLispCompileDoesNotReturnRuntimeCreatedFiles(t *testing.T) {
	if _, err := exec.LookPath("sbcl"); err != nil {
		t.Skip("sbcl not available")
	}

	svc := New()
	resp := svc.Run(context.Background(), &model.CompileRequest{
		Lang: "LISP",
		Sources: []model.Source{{
			Name: "Main.lisp",
			DataB64: b64String(`(with-open-file (out "same-folder.txt"
                     :direction :output
                     :if-exists :supersede
                     :if-does-not-exist :create)
  (write-line "ok" out))
(format t "ok~%")
`),
		}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", resp.Status, resp.Reason, resp.Stdout, resp.Stderr)
	}
	foundSource := false
	for _, artifact := range resp.Artifacts {
		if artifact.Name == "same-folder.txt" {
			t.Fatalf("lisp compile must not execute top-level writes or return runtime-created files: %+v", resp.Artifacts)
		}
		if artifact.Name == "Main.lisp" {
			foundSource = true
		}
	}
	if !foundSource {
		t.Fatalf("lisp compile should return source artifact, got %+v", resp.Artifacts)
	}
}

func TestReadSingleArtifactRejectsSymlinkParents(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "payload.bin"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write payload.bin: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "subdir")); err != nil {
		t.Fatalf("symlink subdir: %v", err)
	}
	if _, err := readSingleArtifact(root, filepath.Join("subdir", "payload.bin"), "payload.bin", ""); err == nil {
		t.Fatalf("expected symlink parent rejection")
	}
}

func TestRunCommandKillsBackgroundChildren(t *testing.T) {
	workDir := sandboxWritableTempDir(t)
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		workDir,
		"/bin/sh",
		[]string{"-c", "sleep 30 & echo $! > bg.pid"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("runCommand status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
	rawPID, err := os.ReadFile(filepath.Join(workDir, "bg.pid"))
	if err != nil {
		t.Fatalf("read bg.pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatalf("parse bg.pid: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		if err != nil {
			t.Fatalf("kill(%d, 0): %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("background child %d is still alive", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunSandboxedCommandCapsCapturedOutput(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workDir := sandboxWritableTempDir(t)
	stdout, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"python3",
		[]string{
			"-c",
			fmt.Sprintf("import sys; sys.stdout.write('x' * %d); sys.stderr.write('y' * %d)", compileOutputCaptureBytes+1024, compileOutputCaptureBytes+2048),
		},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stderr=%q", status, reason, stderr)
	}
	if len(stdout) != compileOutputCaptureBytes {
		t.Fatalf("stdout length=%d, want cap %d", len(stdout), compileOutputCaptureBytes)
	}
	if len(stderr) != compileOutputCaptureBytes {
		t.Fatalf("stderr length=%d, want cap %d", len(stderr), compileOutputCaptureBytes)
	}
}

func TestCompileAddressSpaceLimitBytesUsesHighFiniteDenoCap(t *testing.T) {
	got := compileAddressSpaceLimitBytes("deno", 4096)
	want := uint64(65536) * 1024 * 1024
	if got != want {
		t.Fatalf("compileAddressSpaceLimitBytes(deno, 4096) = %d, want %d", got, want)
	}
	if got := compileAddressSpaceLimitBytes("gcc", 2048); got != 0 {
		t.Fatalf("compileAddressSpaceLimitBytes(gcc, 2048) = %d, want helper default", got)
	}
}

func TestRunSandboxedCommandDoesNotInheritParentSecrets(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	for _, key := range []string{
		"AONOHAKO_API_BEARER_TOKEN",
		"AONOHAKO_REMOTE_RUNNER_TOKEN",
		"AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET",
		"DATABASE_URL",
		"CUSTOM_SECRET",
	} {
		t.Setenv(key, "should-not-enter-sandbox")
	}

	workDir := sandboxWritableTempDir(t)
	stdout, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"python3",
		[]string{
			"-c",
			"import os\nkeys = ['AONOHAKO_API_BEARER_TOKEN', 'AONOHAKO_REMOTE_RUNNER_TOKEN', 'AONOHAKO_PLATFORM_PRINCIPAL_HMAC_SECRET', 'DATABASE_URL', 'CUSTOM_SECRET']\nleaked = [key for key in keys if os.environ.get(key)]\nprint('leak:' + ','.join(leaked) if leaked else 'clean')",
		},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
	if stdout != "clean\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestRunSandboxedCommandMarksWorkspaceEntryLimitExceeded(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workDir := sandboxWritableTempDir(t)
	_, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"python3",
		[]string{
			"-c",
			fmt.Sprintf("from pathlib import Path\nimport time\nfor i in range(%d):\n    Path(f'f{i:05d}.txt').touch()\nwhile True:\n    time.sleep(1)\n", workspacequota.MaxEntries+16),
		},
		nil,
	)
	if status != model.CompileStatusCompileError || !strings.Contains(reason, "workspace entry limit exceeded") {
		t.Fatalf("status=%q reason=%q stderr=%q", status, reason, stderr)
	}
}

func TestRunSandboxedCommandFinalWorkspaceScanCatchesFastExit(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workDir := sandboxWritableTempDir(t)
	_, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"python3",
		[]string{
			"-c",
			fmt.Sprintf("from pathlib import Path\nfor i in range(%d):\n    Path(f'f{i:05d}.txt').touch()\n", workspacequota.MaxEntries+16),
		},
		nil,
	)
	if status != model.CompileStatusCompileError || !strings.Contains(reason, "workspace entry limit exceeded") {
		t.Fatalf("status=%q reason=%q stderr=%q", status, reason, stderr)
	}
}

func TestRunSandboxedCommandFailsClosedWhenWorkspaceScanFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can traverse unreadable directories")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	workDir := sandboxWritableTempDir(t)
	_, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"python3",
		[]string{
			"-c",
			"import os, time\nos.mkdir('hidden', 0)\nwhile True:\n    time.sleep(1)\n",
		},
		nil,
	)
	if status != model.CompileStatusCompileError || !strings.Contains(reason, "workspace scan failed") {
		t.Fatalf("status=%q reason=%q stderr=%q", status, reason, stderr)
	}
}

func TestCapCompileResponseOutputSetsTruncationFlags(t *testing.T) {
	resp := capCompileResponseOutput(model.CompileResponse{
		Status: model.CompileStatusCompileError,
		Stdout: strings.Repeat("x", compileOutputCaptureBytes+1),
		Stderr: strings.Repeat("y", compileOutputCaptureBytes+2),
	})
	if len(resp.Stdout) != compileOutputCaptureBytes {
		t.Fatalf("stdout length=%d, want cap %d", len(resp.Stdout), compileOutputCaptureBytes)
	}
	if len(resp.Stderr) != compileOutputCaptureBytes {
		t.Fatalf("stderr length=%d, want cap %d", len(resp.Stderr), compileOutputCaptureBytes)
	}
	if !resp.StdoutTruncated {
		t.Fatal("StdoutTruncated=false, want true")
	}
	if !resp.StderrTruncated {
		t.Fatal("StderrTruncated=false, want true")
	}
}

func TestCapCompileResponseOutputSetsResourceReasonCode(t *testing.T) {
	resp := capCompileResponseOutput(model.CompileResponse{
		Status: model.CompileStatusCompileError,
		Reason: "memory limit exceeded",
	})
	if resp.ReasonCode != "memory_limit_exceeded" {
		t.Fatalf("ReasonCode=%q, want memory_limit_exceeded", resp.ReasonCode)
	}

	resp = capCompileResponseOutput(model.CompileResponse{
		Status:     model.CompileStatusCompileError,
		Reason:     "memory limit exceeded",
		ReasonCode: "custom",
	})
	if resp.ReasonCode != "custom" {
		t.Fatalf("existing ReasonCode overwritten with %q", resp.ReasonCode)
	}

	for _, reason := range []string{
		"workspace quota exceeded",
		"workspace entry limit exceeded",
		"workspace depth exceeded",
		"workspace scan failed",
	} {
		resp = capCompileResponseOutput(model.CompileResponse{
			Status: model.CompileStatusCompileError,
			Reason: reason,
		})
		if resp.ReasonCode != "workspace_limit_exceeded" {
			t.Fatalf("ReasonCode=%q for reason %q, want workspace_limit_exceeded", resp.ReasonCode, reason)
		}
	}
}

func TestCapCompileResponseOutputRedactsInternalWorkDirReason(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "aonohako-compile-123")
	reason := "open " + filepath.Join(workDir, "Main") + ": no such file or directory"
	resp := capCompileResponseOutput(model.CompileResponse{
		Status: model.CompileStatusInternal,
		Stdout: filepath.Join(workDir, "compiler-output-kept") + "\n",
		Stderr: filepath.Join(workDir, "compiler-stderr-kept") + "\n",
		Reason: reason,
	}, workDir)

	if strings.Contains(resp.Reason, workDir) {
		t.Fatalf("reason still contains workDir: %q", resp.Reason)
	}
	if !strings.Contains(resp.Reason, "$WORKDIR/Main") {
		t.Fatalf("reason = %q, want redacted workdir marker", resp.Reason)
	}
	if !strings.Contains(resp.Stdout, workDir) || !strings.Contains(resp.Stderr, workDir) {
		t.Fatalf("compiler stdout/stderr should be preserved, got stdout=%q stderr=%q", resp.Stdout, resp.Stderr)
	}
}

func TestCapCompileResponseOutputKeepsCompilerFailureReason(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "aonohako-compile-123")
	resp := capCompileResponseOutput(model.CompileResponse{
		Status: model.CompileStatusCompileError,
		Reason: "compile failed at " + filepath.Join(workDir, "Main.c"),
	}, workDir)

	if !strings.Contains(resp.Reason, workDir) {
		t.Fatalf("compile-error reason should be preserved, got %q", resp.Reason)
	}
}

func TestCappedTextBufferCapsAggregation(t *testing.T) {
	buf := newCompileOutputBuffer()
	buf.Append(strings.Repeat("x", compileOutputCaptureBytes-1))
	buf.Append("yyy")
	if len(buf.String()) != compileOutputCaptureBytes {
		t.Fatalf("buffer length=%d, want cap %d", len(buf.String()), compileOutputCaptureBytes)
	}
	if !buf.Truncated() {
		t.Fatal("Truncated=false, want true")
	}
	if got := buf.String()[compileOutputCaptureBytes-1:]; got != "y" {
		t.Fatalf("tail = %q, want first byte from overflowing append", got)
	}
}

func TestRunSandboxedCommandAllowsWritesBesideNestedCompileSources(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop compile helper to sandbox user")
	}

	workDir := sandboxWritableTempDir(t)
	sourceDir := filepath.Join(workDir, "src", "App")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Program.cs"), []byte("class Program {}"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	stdout, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"/bin/sh",
		[]string{"-c", "mkdir -p src/App/obj && touch src/App/obj/generated.txt"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "obj", "generated.txt")); err != nil {
		t.Fatalf("expected nested generated file: %v", err)
	}
}

func TestRunSandboxedCommandPreventsRemovingOrReplacingSubmittedCompileSources(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop compile helper to sandbox user")
	}

	workDir := sandboxWritableTempDir(t)
	if err := materializeSources(workDir, []model.Source{
		{
			Name:    "pkg/Main.py",
			DataB64: b64String("print('safe')\n"),
		},
	}); err != nil {
		t.Fatalf("materializeSources: %v", err)
	}
	if err := hardenCompileWorkspace(workDir); err != nil {
		t.Fatalf("hardenCompileWorkspace: %v", err)
	}

	stdout, stderr, status, reason := RunSandboxedCommand(
		context.Background(),
		workDir,
		"/bin/sh",
		[]string{"-c", "rm -f pkg/Main.py || true; printf 'print(\"pwned\")\\n' > pkg/Main.py 2>/dev/null || true; printf 'ok\\n' > pkg/generated.txt"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}

	raw, err := os.ReadFile(filepath.Join(workDir, "pkg", "Main.py"))
	if err != nil {
		t.Fatalf("read Main.py: %v", err)
	}
	if string(raw) != "print('safe')\n" {
		t.Fatalf("submitted source changed: %q", string(raw))
	}
	if _, err := os.Stat(filepath.Join(workDir, "pkg", "generated.txt")); err != nil {
		t.Fatalf("expected generated sibling file: %v", err)
	}
}

func TestRunCommandRejectsNetworkSockets(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import errno, socket, sys\ntry:\n    socket.socket()\nexcept OSError as exc:\n    sys.exit(0 if exc.errno in (errno.EPERM, errno.EACCES) else 1)\nsys.exit(1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected socket denial probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandAllowsIsabelleUnixSocketChannelWithoutInetSockets(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	binDir := t.TempDir()
	if err := os.Chmod(binDir, 0o755); err != nil {
		t.Fatalf("chmod bin dir: %v", err)
	}
	if err := os.Symlink(python, filepath.Join(binDir, "isabelle")); err != nil {
		t.Fatalf("symlink isabelle probe: %v", err)
	}
	script := "import errno, os, socket\npath = os.path.join(os.environ['TMPDIR'], 'isabelle.sock')\nserver = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\nserver.bind(path)\nserver.listen(1)\nclient = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\nclient.connect(path)\nconn, _ = server.accept()\nclient.sendall(b'ok')\nprint(conn.recv(2).decode())\ntry:\n    socket.socket(socket.AF_INET, socket.SOCK_STREAM)\n    print('inet-open')\nexcept OSError as exc:\n    print('inet-blocked' if exc.errno in (errno.EPERM, errno.EACCES) else f'unexpected:{exc.errno}')\n"
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		"isabelle",
		[]string{"-c", script},
		[]string{"PATH=" + binDir + ":/usr/local/bin:/usr/bin:/bin"},
	)
	if status != model.CompileStatusOK || stdout != "ok\ninet-blocked\n" {
		t.Fatalf("expected isabelle unix channel support without inet sockets, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsUnixSocketConnectToHost(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	socketDir := t.TempDir()
	if err := os.Chmod(socketDir, 0o777); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	socketPath := filepath.Join(socketDir, "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0o777); err != nil {
		t.Fatalf("chmod unix socket: %v", err)
	}

	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\n    s.settimeout(0.5)\n    s.connect(%q)\n    print('connected')\nexcept OSError:\n    print('blocked')\n",
		socketPath,
	)
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", script},
		nil,
	)
	if status != model.CompileStatusOK || stdout != "blocked\n" {
		t.Fatalf("expected unix socket connect denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandAllowsLocalUnixSocketPairs(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import socket, sys\na, b = socket.socketpair()\na.sendall(b'ok')\nsys.exit(0 if b.recv(2) == b'ok' else 1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected local unix socketpair probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsUnixSocketSendmsgToHost(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("aonohako-compile-dgram-%d.sock", time.Now().UnixNano()))
	_ = os.Remove(socketPath)
	addr := &net.UnixAddr{Name: socketPath, Net: "unixgram"}
	listener, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		t.Fatalf("listen unixgram socket: %v", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o777); err != nil {
		t.Fatalf("chmod unixgram socket: %v", err)
	}

	script := fmt.Sprintf(
		"import socket\ntry:\n    s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)\n    s.sendmsg([b'escape'], [], 0, %q)\n    print('sent')\nexcept OSError:\n    print('blocked')\n",
		socketPath,
	)
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", script},
		nil,
	)
	if status != model.CompileStatusOK || stdout != "blocked\n" {
		t.Fatalf("expected unix sendmsg denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
	_ = listener.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 64)
	if n, _, err := listener.ReadFromUnix(buf); err == nil {
		t.Fatalf("expected no datagram delivery, got %q", string(buf[:n]))
	}
}

func TestRunCommandRejectsNamespaceEscape(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import ctypes, errno, sys\nlibc = ctypes.CDLL(None, use_errno=True)\nif libc.unshare(0x20000) == 0:\n    sys.exit(1)\nsys.exit(0 if ctypes.get_errno() in (errno.EPERM, errno.ENOSYS) else 1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected unshare denial probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsUnsafeCloneFlagsAndClone3(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		cc, err = exec.LookPath("gcc")
	}
	if err != nil {
		t.Skip("C compiler is unavailable on this runner")
	}
	workDir := sandboxWritableTempDir(t)
	binaryPath := filepath.Join(workDir, "clone-probe")
	source := `
#define _GNU_SOURCE
#include <errno.h>
#include <linux/sched.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

static int reject_child(long rc, const char *name) {
	if (rc == 0) {
		_exit(101);
	}
	if (rc > 0) {
		int status = 0;
		waitpid((pid_t)rc, &status, 0);
		fprintf(stderr, "%s escaped\n", name);
		return 1;
	}
	return 0;
}

int main(void) {
	errno = 0;
	long classic = syscall(SYS_clone, (unsigned long)CLONE_UNTRACED | SIGCHLD, 0, 0, 0, 0);
	if (reject_child(classic, "clone") != 0 || errno != EPERM) {
		fprintf(stderr, "clone errno=%d (%s)\n", errno, strerror(errno));
		return 1;
	}
#ifdef SYS_clone3
	struct clone_args args = {0};
	args.exit_signal = SIGCHLD;
	errno = 0;
	long modern = syscall(SYS_clone3, &args, sizeof(args));
	if (reject_child(modern, "clone3") != 0 || errno != ENOSYS) {
		fprintf(stderr, "clone3 errno=%d (%s)\n", errno, strerror(errno));
		return 1;
	}
#endif
	return 0;
}
`
	build := exec.Command(cc, "-O2", "-x", "c", "-", "-o", binaryPath)
	build.Stdin = strings.NewReader(source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile clone probe: %v\n%s", err, string(output))
	}

	stdout, stderr, status, reason := runCommand(
		context.Background(),
		workDir,
		binaryPath,
		nil,
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected unsafe clone denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsProcessGroupEscape(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import errno, os, sys\ntry:\n    os.setpgid(0, 0)\nexcept OSError as exc:\n    sys.exit(0 if exc.errno in (errno.EPERM, errno.EACCES) else 1)\nsys.exit(1)\n"},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected process-group denial probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandRejectsFilesystemPrivilegeSyscalls(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", "import errno, os, sys\nopen('owned.txt', 'w').close()\nchecks = [\n    ('chmod', lambda: os.chmod('owned.txt', 0o777)),\n    ('chown', lambda: os.chown('owned.txt', os.getuid(), os.getgid())),\n    ('mknod', lambda: os.mknod('node')),\n]\nfor name, action in checks:\n    try:\n        action()\n        print(name + ':escaped')\n        sys.exit(1)\n    except OSError as exc:\n        if exc.errno not in (errno.EPERM, errno.EACCES, errno.ENOSYS):\n            print(name + ':error:' + str(exc.errno))\n            sys.exit(1)\nprint('blocked')\n"},
		nil,
	)
	if status != model.CompileStatusOK || stdout != "blocked\n" {
		t.Fatalf("expected filesystem privilege syscall denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandAllowsChmodForExecutableCompilers(t *testing.T) {
	for _, commandName := range []string{"apecc", "zero"} {
		t.Run(commandName, func(t *testing.T) {
			workDir := sandboxWritableTempDir(t)
			binDir := filepath.Join(workDir, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatalf("mkdir bin dir: %v", err)
			}
			compiler := filepath.Join(binDir, commandName)
			if err := os.WriteFile(compiler, []byte("#!/bin/sh\nset -eu\n: > Main\n/bin/chmod 755 Main\n"), 0o755); err != nil {
				t.Fatalf("write fake %s: %v", commandName, err)
			}

			stdout, stderr, status, reason := runCommand(
				context.Background(),
				workDir,
				commandName,
				nil,
				[]string{"PATH=" + binDir},
			)
			if status != model.CompileStatusOK {
				t.Fatalf("expected %s chmod to succeed, got status=%q reason=%q stdout=%q stderr=%q", commandName, status, reason, stdout, stderr)
			}
			info, err := os.Stat(filepath.Join(workDir, "Main"))
			if err != nil {
				t.Fatalf("stat compiled output: %v", err)
			}
			if info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("compiled output mode = %v, want executable bit", info.Mode().Perm())
			}
		})
	}
}

func TestRunCommandRejectsKernelAttackSurfaceSyscalls(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		cc, err = exec.LookPath("gcc")
	}
	if err != nil {
		t.Skip("C compiler is unavailable on this runner")
	}

	code := `
#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

static int check(const char *name, long nr) {
	errno = 0;
	long rc = syscall(nr, 0, 0, 0, 0, 0, 0);
	if (rc == -1 && (errno == EPERM || errno == EACCES || errno == ENOSYS)) {
		return 0;
	}
	printf("%s:%ld:%s\n", name, rc, strerror(errno));
	return 1;
}

static int check_personality(void) {
	errno = 0;
	long query_rc = syscall(SYS_personality, 0xffffffffUL, 0, 0, 0, 0, 0);
	if (query_rc == -1) {
		printf("personality_query:%s\n", strerror(errno));
		return 1;
	}
	errno = 0;
	long set_rc = syscall(SYS_personality, 0, 0, 0, 0, 0, 0);
	if (set_rc == -1 && errno == EPERM) {
		return 0;
	}
	printf("personality_set:%ld:%s\n", set_rc, strerror(errno));
	return 1;
}

static int check_x32_socket(void) {
#if defined(__x86_64__) && defined(SYS_socket)
	errno = 0;
	long rc = syscall(0x40000000UL | SYS_socket, 2, 1, 0, 0, 0, 0);
	if (rc == -1 && errno == EPERM) {
		return 0;
	}
	printf("x32_socket:%ld:%s\n", rc, strerror(errno));
	return 1;
#else
	return 0;
#endif
}

int main(void) {
	int failed = 0;
	failed |= check_x32_socket();
#ifdef SYS_bpf
	failed |= check("bpf", SYS_bpf);
#endif
#ifdef SYS_userfaultfd
	failed |= check("userfaultfd", SYS_userfaultfd);
#endif
#ifdef SYS_io_uring_setup
	failed |= check("io_uring_setup", SYS_io_uring_setup);
#endif
#ifdef SYS_perf_event_open
	failed |= check("perf_event_open", SYS_perf_event_open);
#endif
#ifdef SYS_cachestat
	failed |= check("cachestat", SYS_cachestat);
#endif
#ifdef SYS_open_by_handle_at
	failed |= check("open_by_handle_at", SYS_open_by_handle_at);
#endif
#ifdef SYS_name_to_handle_at
	failed |= check("name_to_handle_at", SYS_name_to_handle_at);
#endif
#ifdef SYS_fanotify_init
	failed |= check("fanotify_init", SYS_fanotify_init);
#endif
#ifdef SYS_fanotify_mark
	failed |= check("fanotify_mark", SYS_fanotify_mark);
#endif
#ifdef SYS_lookup_dcookie
	failed |= check("lookup_dcookie", SYS_lookup_dcookie);
#endif
#ifdef SYS_add_key
	failed |= check("add_key", SYS_add_key);
#endif
#ifdef SYS_request_key
	failed |= check("request_key", SYS_request_key);
#endif
#ifdef SYS_keyctl
	failed |= check("keyctl", SYS_keyctl);
#endif
#ifdef SYS_init_module
	failed |= check("init_module", SYS_init_module);
#endif
#ifdef SYS_finit_module
	failed |= check("finit_module", SYS_finit_module);
#endif
#ifdef SYS_delete_module
	failed |= check("delete_module", SYS_delete_module);
#endif
#ifdef SYS_kexec_load
	failed |= check("kexec_load", SYS_kexec_load);
#endif
#ifdef SYS_kexec_file_load
	failed |= check("kexec_file_load", SYS_kexec_file_load);
#endif
#ifdef SYS_acct
	failed |= check("acct", SYS_acct);
#endif
#ifdef SYS_nfsservctl
	failed |= check("nfsservctl", SYS_nfsservctl);
#endif
#ifdef SYS_quotactl
	failed |= check("quotactl", SYS_quotactl);
#endif
#ifdef SYS_quotactl_fd
	failed |= check("quotactl_fd", SYS_quotactl_fd);
#endif
#ifdef SYS_process_madvise
	failed |= check("process_madvise", SYS_process_madvise);
#endif
#ifdef SYS_process_mrelease
	failed |= check("process_mrelease", SYS_process_mrelease);
#endif
#ifdef SYS_get_mempolicy
	failed |= check("get_mempolicy", SYS_get_mempolicy);
#endif
#ifdef SYS_mbind
	failed |= check("mbind", SYS_mbind);
#endif
#ifdef SYS_set_mempolicy
	failed |= check("set_mempolicy", SYS_set_mempolicy);
#endif
#ifdef SYS_set_mempolicy_home_node
	failed |= check("set_mempolicy_home_node", SYS_set_mempolicy_home_node);
#endif
#ifdef SYS_migrate_pages
	failed |= check("migrate_pages", SYS_migrate_pages);
#endif
#ifdef SYS_move_pages
	failed |= check("move_pages", SYS_move_pages);
#endif
#ifdef SYS_kcmp
	failed |= check("kcmp", SYS_kcmp);
#endif
#ifdef SYS_seccomp
	failed |= check("seccomp", SYS_seccomp);
#endif
#ifdef SYS_landlock_create_ruleset
	failed |= check("landlock_create_ruleset", SYS_landlock_create_ruleset);
#endif
#ifdef SYS_landlock_add_rule
	failed |= check("landlock_add_rule", SYS_landlock_add_rule);
#endif
#ifdef SYS_landlock_restrict_self
	failed |= check("landlock_restrict_self", SYS_landlock_restrict_self);
#endif
#ifdef SYS_lsm_get_self_attr
	failed |= check("lsm_get_self_attr", SYS_lsm_get_self_attr);
#endif
#ifdef SYS_lsm_set_self_attr
	failed |= check("lsm_set_self_attr", SYS_lsm_set_self_attr);
#endif
#ifdef SYS_lsm_list_modules
	failed |= check("lsm_list_modules", SYS_lsm_list_modules);
#endif
#ifdef SYS_clock_settime
	failed |= check("clock_settime", SYS_clock_settime);
#endif
#ifdef SYS_settimeofday
	failed |= check("settimeofday", SYS_settimeofday);
#endif
#ifdef SYS_adjtimex
	failed |= check("adjtimex", SYS_adjtimex);
#endif
#ifdef SYS_syslog
	failed |= check("syslog", SYS_syslog);
#endif
#ifdef SYS_reboot
	failed |= check("reboot", SYS_reboot);
#endif
#ifdef SYS_swapon
	failed |= check("swapon", SYS_swapon);
#endif
#ifdef SYS_swapoff
	failed |= check("swapoff", SYS_swapoff);
#endif
#ifdef SYS_memfd_create
	failed |= check("memfd_create", SYS_memfd_create);
#endif
#ifdef SYS_open_tree
	failed |= check("open_tree", SYS_open_tree);
#endif
#ifdef SYS_move_mount
	failed |= check("move_mount", SYS_move_mount);
#endif
#ifdef SYS_fsopen
	failed |= check("fsopen", SYS_fsopen);
#endif
#ifdef SYS_fsconfig
	failed |= check("fsconfig", SYS_fsconfig);
#endif
#ifdef SYS_fsmount
	failed |= check("fsmount", SYS_fsmount);
#endif
#ifdef SYS_fspick
	failed |= check("fspick", SYS_fspick);
#endif
#ifdef SYS_mount_setattr
	failed |= check("mount_setattr", SYS_mount_setattr);
#endif
#ifdef SYS_statmount
	failed |= check("statmount", SYS_statmount);
#endif
#ifdef SYS_listmount
	failed |= check("listmount", SYS_listmount);
#endif
#ifdef SYS_pidfd_open
	failed |= check("pidfd_open", SYS_pidfd_open);
#endif
#ifdef SYS_pidfd_getfd
	failed |= check("pidfd_getfd", SYS_pidfd_getfd);
#endif
#ifdef SYS_pidfd_send_signal
	failed |= check("pidfd_send_signal", SYS_pidfd_send_signal);
#endif
#ifdef SYS_fchmodat2
	failed |= check("fchmodat2", SYS_fchmodat2);
#endif
#ifdef SYS_personality
	failed |= check_personality();
#endif
	if (failed != 0) {
		return 1;
	}
	puts("blocked");
	return 0;
}
`
	workDir := sandboxWritableTempDir(t)
	binPath := filepath.Join(workDir, "kernel-syscall-probe")
	compileCmd := exec.Command(cc, "-O2", "-x", "c", "-", "-o", binPath)
	compileCmd.Stdin = strings.NewReader(code)
	output, err := compileCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compile syscall probe: %v\n%s", err, string(output))
	}

	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		binPath,
		nil,
		nil,
	)
	if status != model.CompileStatusOK || stdout != "blocked\n" {
		t.Fatalf("expected kernel syscall denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandCannotReadOrWriteRootOwnedHostPaths(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to drop compile helper to sandbox user")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	secretDir := t.TempDir()
	if err := os.Chmod(secretDir, 0o700); err != nil {
		t.Fatalf("chmod secret dir: %v", err)
	}
	secretPath := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top-secret"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	script := fmt.Sprintf(
		"from pathlib import Path\nfor label, action in [('read', lambda: Path(%q).read_text()), ('write', lambda: Path(%q).write_text('escape'))]:\n    try:\n        action()\n        print(label + ':escaped')\n    except Exception:\n        print(label + ':blocked')\n",
		secretPath,
		filepath.Join(secretDir, "created.txt"),
	)
	stdout, stderr, status, reason := runCommand(
		context.Background(),
		sandboxWritableTempDir(t),
		python,
		[]string{"-c", script},
		nil,
	)
	if status != model.CompileStatusOK || stdout != "read:blocked\nwrite:blocked\n" {
		t.Fatalf("expected host path read/write denial, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}

func TestRunCommandDoesNotLeakInheritedFileDescriptors(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	workDir := sandboxWritableTempDir(t)
	fdFile, err := os.CreateTemp(workDir, "inherited-fd-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer fdFile.Close()

	if _, err := fdFile.WriteString("secret"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if _, err := fdFile.Seek(0, 0); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	fd := int(fdFile.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatalf("F_GETFD: %v", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
		t.Fatalf("F_SETFD: %v", err)
	}

	stdout, stderr, status, reason := runCommand(
		context.Background(),
		workDir,
		python,
		[]string{"-c", "import errno, os, sys\nfd = int(sys.argv[1])\ntry:\n    os.read(fd, 1)\nexcept OSError as exc:\n    sys.exit(0 if exc.errno == errno.EBADF else 1)\nsys.exit(1)\n", strconv.Itoa(fd)},
		nil,
	)
	if status != model.CompileStatusOK {
		t.Fatalf("expected inherited fd probe to exit cleanly, got status=%q reason=%q stdout=%q stderr=%q", status, reason, stdout, stderr)
	}
}
