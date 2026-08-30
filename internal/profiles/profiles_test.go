package profiles

import "testing"

func TestResolveSupportsDoolSourceLanguages(t *testing.T) {
	languages := []string{
		"TEXT",
		"C",
		"C89",
		"C99",
		"C11",
		"C17",
		"C18",
		"C23",
		"CPP",
		"CPP98",
		"CPP03",
		"CPP11",
		"CPP14",
		"CPP17",
		"CPP20",
		"CPP23",
		"CPP26",
		"PYTHON3",
		"PYPY3",
		"JAVA",
		"JAVA8",
		"JAVA11",
		"JAVA15",
		"JAVA17",
		"JAVA21",
		"GROOVY",
		"JAVASCRIPT",
		"TYPESCRIPT",
		"GO",
		"ZIG",
		"SCHEME",
		"CHEZ_SCHEME",
		"GUILE",
		"CHICKEN_SCHEME",
		"AWK",
		"TCL",
		"GDL",
		"OCTAVE",
		"VHDL",
		"VERILOG",
		"SYSTEMVERILOG",
		"CRYSTAL",
		"VALA",
		"VLANG",
		"ODIN",
		"C3",
		"HARE",
		"VBNET",
		"VB",
		"GLEAM",
		"CUDA_OCELOT",
		"CARBON",
		"GRAPHQL",
		"ROCQ",
		"LEAN4",
		"LEAN",
		"AGDA",
		"DAFNY",
		"TLA",
		"TLAPLUS",
		"WHY3",
		"WHYML",
		"ISABELLE",
		"FSTAR",
		"ALLOY",
		"ACL2",
		"KFRAMEWORK",
		"RUST",
		"RUST2015",
		"RUST2018",
		"RUST2021",
		"RUST2024",
		"MOONBIT",
		"FENNEL",
		"CHAPEL",
		"ALGOL68",
		"KOKA",
		"SHELL",
		"BASH",
		"POSIX_SH",
		"ZEROLANG",
		"KOTLIN",
		"ADA",
		"ADA2012",
		"ADA2022",
		"COBOL",
		"GNUCOBOL",
		"CYTHON",
		"FORTRAN",
		"FORTRAN95",
		"FORTRAN2003",
		"FORTRAN2008",
		"FORTRAN2018",
		"D",
		"OBJC",
		"OBJECTIVE_C",
		"OBJCPP",
		"OBJECTIVE_CPP",
		"COQ",
		"HASKELL",
		"IDRIS2",
		"SML",
		"HAXE",
		"LISP",
		"PICOLISP",
		"ASM",
		"AHEUI",
		"NASM",
		"DELPHI",
		"OBJECTPASCAL",
		"SWIFT",
		"SQLITE",
		"JULIA",
		"RAKU",
		"ERLANG",
		"MERCURY",
		"PROLOG",
		"SCALA",
		"FSHARP",
		"WHITESPACE",
		"BEFUNGE",
		"BF",
		"MALBOLGE",
		"LOLCODE",
		"APECODE",
		"WASM",
		"ASSEMBLYSCRIPT",
		"FACTOR",
		"OCAML",
		"ELIXIR",
		"COFFEESCRIPT",
		"RESCRIPT",
		"PURESCRIPT",
		"RUBY",
		"PHP",
		"PHP7",
		"PHP8",
		"CSHARP",
		"R",
		"LUA",
		"PERL",
		"VB6",
		"ELM",
		"FREEBASIC",
		"CLASSIC_BASIC",
		"QBASIC",
		"SMALLTALK",
		"GST",
		"GOLFSCRIPT",
		"MOJO",
		"DENO",
		"KOTLIN_JVM",
		"KOTLIN_JVM8",
		"KOTLIN_JVM11",
		"KOTLIN_JVM17",
		"KOTLIN_JVM21",
		"KOTLIN_JAVA",
		"KOTLIN_JAVA8",
		"KOTLIN_JAVA11",
		"KOTLIN_JAVA17",
		"KOTLIN_JAVA21",
		"DUCKDB",
		"BQN",
		"APL",
		"GNU_APL",
		"J",
		"UIUA",
		"JANET",
		"SED",
		"BC",
		"FORTH",
		"GFORTH",
		"UHMLANG",
	}

	for _, language := range languages {
		if _, ok := Resolve(language); !ok {
			t.Fatalf("Resolve(%q) reported unsupported language", language)
		}
	}
}

func TestNormalizeRunLangSupportsExtendedRuntimeSet(t *testing.T) {
	tests := map[string]string{
		"OCAML":        "ocaml",
		"ELIXIR":       "elixir",
		"GO":           "go-binary",
		"go":           "go-binary",
		"go-binary":    "go-binary",
		"MOJO":         "mojo-binary",
		"mojo":         "mojo-binary",
		"mojo-binary":  "mojo-binary",
		"CARBON":       "binary",
		"MOONBIT":      "binary",
		"moonbit":      "binary",
		"FENNEL":       "lua",
		"fennel":       "lua",
		"CHAPEL":       "chapel-binary",
		"chapel":       "chapel-binary",
		"ALGOL68":      "algol68",
		"algol68":      "algol68",
		"KOKA":         "binary",
		"koka":         "binary",
		"PONY":         "pony-binary",
		"pony":         "pony-binary",
		"pony-binary":  "pony-binary",
		"SHELL":        "bash",
		"shell":        "bash",
		"BASH":         "bash",
		"bash":         "bash",
		"POSIX_SH":     "posix-sh",
		"posix_sh":     "posix-sh",
		"posix-sh":     "posix-sh",
		"sh":           "posix-sh",
		"POWERSHELL":   "powershell",
		"powershell":   "powershell",
		"pwsh":         "powershell",
		"ZEROLANG":     "binary",
		"zerolang":     "binary",
		"zero":         "binary",
		"ADA":          "binary",
		"ADA2012":      "binary",
		"ADA2022":      "binary",
		"ADA12":        "binary",
		"ADA22":        "binary",
		"FORTRAN":      "binary",
		"FORTRAN95":    "binary",
		"FORTRAN2003":  "binary",
		"FORTRAN2008":  "binary",
		"FORTRAN2018":  "binary",
		"F95":          "binary",
		"F2003":        "binary",
		"F2008":        "binary",
		"F2018":        "binary",
		"C3":           "c3",
		"D":            "binary",
		"COQ":          "rocq",
		"HASKELL":      "binary",
		"IDRIS2":       "binary",
		"SML":          "binary",
		"LISP":         "lisp",
		"PICOLISP":     "picolisp",
		"picolisp":     "picolisp",
		"ASM":          "binary",
		"AHEUI":        "aheui",
		"NASM":         "binary",
		"DELPHI":       "binary",
		"OBJECTPASCAL": "binary",
		"ZIG":          "binary",
		"SWIFT":        "binary",
		"SQLITE":       "sqlite",
		"JULIA":        "julia",
		"ERLANG":       "erlang",
		"MERCURY":      "binary",
		"PROLOG":       "prolog",
		"R":            "r",
		"GROOVY":       "groovy",
		"SCALA":        "scala",
		"FSHARP":       "fsharp",
		"WASM":         "wasm",
		"BF":           "brainfuck",
		"BEFUNGE":      "befunge",
		"MALBOLGE":     "malbolge",
		"LOLCODE":      "lolcode",
		"APECODE":      "binary",
		"WHITESPACE":   "whitespace",
		"SCHEME":       "scheme",
		"TCL":          "tcl",
		"GAWK":         "awk",
		"VB":           "vbnet",
		"LEAN":         "lean4",
		"TLAPLUS":      "tla",
		"WHYML":        "why3",
		"FSTAR":        "fstar",
		"F*":           "fstar",
		"ALLOY":        "alloy",
		"ACL2":         "acl2",
		"KFRAMEWORK":   "kframework",
		"K":            "kframework",
		"GST":          "smalltalk",
		"GNU_APL":      "apl",
		"J":            "j",
		"VALA":         "binary",
		"HAXE":         "haxe",
		"ELM":          "javascript",
		"RESCRIPT":     "javascript",
		"PURESCRIPT":   "javascript",
		"KOTLIN/JAVA":  "kotlin-jvm",
		"KOTLIN-JAVA":  "kotlin-jvm",
		"RAKU":         "raku",
		"PHP7":         "php",
		"PHP8":         "php",
		"SED":          "sed",
		"BC":           "bc",
		"FORTH":        "forth",
		"GFORTH":       "forth",
	}
	tests["ASSEMBLYSCRIPT"] = "assemblyscript"
	tests["FACTOR"] = "factor"
	tests["CHEZ_SCHEME"] = "chez-scheme"
	tests["GUILE"] = "guile"
	tests["CHICKEN_SCHEME"] = "chicken-scheme"

	for input, want := range tests {
		if got := NormalizeRunLang(input); got != want {
			t.Fatalf("NormalizeRunLang(%q) = %q, want %q", input, got, want)
		}
	}
	if got := NormalizeRunLang("chapel-binary"); got != "chapel-binary" {
		t.Fatalf("NormalizeRunLang(chapel-binary) = %q, want chapel-binary", got)
	}
}

func TestAssemblyScriptProfileCompilesToIsolatedWASIArtifact(t *testing.T) {
	profile, ok := Resolve("ASSEMBLYSCRIPT")
	if !ok {
		t.Fatal("Resolve(ASSEMBLYSCRIPT) reported unsupported language")
	}
	if profile.Extension != "ts" || profile.DefaultTarget != "Main.wasm" || profile.CompileKind != "assemblyscript" || profile.RunLang != "assemblyscript" {
		t.Fatalf("ASSEMBLYSCRIPT profile = %+v", profile)
	}
	if profile.TimeMultiplier != 2 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 128 {
		t.Fatalf("ASSEMBLYSCRIPT resource profile = %+v", profile)
	}
}

func TestChezSchemeProfileUsesDedicatedReaderAndRuntime(t *testing.T) {
	profile, ok := Resolve("CHEZ_SCHEME")
	if !ok {
		t.Fatal("Resolve(CHEZ_SCHEME) reported unsupported language")
	}
	if profile.Extension != "scm" || profile.CompileKind != "chez-scheme" || profile.RunLang != "chez-scheme" {
		t.Fatalf("CHEZ_SCHEME profile = %+v", profile)
	}
	if profile.TimeMultiplier != 2 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 256 {
		t.Fatalf("CHEZ_SCHEME resource profile = %+v", profile)
	}
}

func TestGuileProfileUsesDedicatedReaderAndRuntime(t *testing.T) {
	profile, ok := Resolve("GUILE")
	if !ok {
		t.Fatal("Resolve(GUILE) reported unsupported language")
	}
	if profile.Extension != "scm" || profile.CompileKind != "guile" || profile.RunLang != "guile" {
		t.Fatalf("GUILE profile = %+v", profile)
	}
	if profile.TimeMultiplier != 2 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 256 {
		t.Fatalf("GUILE resource profile = %+v", profile)
	}
}

func TestChickenSchemeProfileCompilesStaticNativeArtifact(t *testing.T) {
	profile, ok := Resolve("CHICKEN_SCHEME")
	if !ok {
		t.Fatal("Resolve(CHICKEN_SCHEME) reported unsupported language")
	}
	if profile.Extension != "scm" || profile.DefaultTarget != "Main" || profile.CompileKind != "chicken-scheme" || profile.RunLang != "chicken-scheme" {
		t.Fatalf("CHICKEN_SCHEME profile = %+v", profile)
	}
	if profile.TimeMultiplier != 1 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 128 {
		t.Fatalf("CHICKEN_SCHEME resource profile = %+v", profile)
	}
}

func TestFactorProfileUsesDedicatedJITRuntime(t *testing.T) {
	profile, ok := Resolve("FACTOR")
	if !ok {
		t.Fatal("Resolve(FACTOR) reported unsupported language")
	}
	if profile.Extension != "factor" || profile.CompileKind != "factor" || profile.RunLang != "factor" {
		t.Fatalf("FACTOR profile = %+v", profile)
	}
	if profile.TimeMultiplier != 2 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 512 {
		t.Fatalf("FACTOR resource profile = %+v", profile)
	}
}

func TestMoonBitProfileCompilesPinnedNativeArtifact(t *testing.T) {
	profile, ok := Resolve("MOONBIT")
	if !ok {
		t.Fatal("Resolve(MOONBIT) reported unsupported language")
	}
	if profile.Extension != "mbt" || profile.DefaultTarget != "Main" || profile.CompileKind != "moonbit" || profile.RunLang != "binary" {
		t.Fatalf("MOONBIT profile = %+v", profile)
	}
	if profile.TimeMultiplier != 2 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 256 {
		t.Fatalf("MOONBIT resource profile = %+v", profile)
	}
}

func TestFennelProfileCompilesHardenedLuaArtifact(t *testing.T) {
	profile, ok := Resolve("FENNEL")
	if !ok {
		t.Fatal("Resolve(FENNEL) reported unsupported language")
	}
	if profile.Extension != "fnl" || profile.DefaultTarget != "Main.lua" || profile.CompileKind != "fennel" || profile.RunLang != "lua" {
		t.Fatalf("FENNEL profile = %+v", profile)
	}
	if profile.TimeMultiplier != 1 || profile.TimeOffsetMs != 500 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 512 {
		t.Fatalf("FENNEL resource profile = %+v", profile)
	}
}

func TestChapelProfileRunsBoundedNativeArtifact(t *testing.T) {
	profile, ok := Resolve("CHAPEL")
	if !ok {
		t.Fatal("Resolve(CHAPEL) reported unsupported language")
	}
	if profile.Extension != "chpl" || profile.DefaultTarget != "Main" || profile.CompileKind != "chapel" || profile.RunLang != "chapel-binary" {
		t.Fatalf("CHAPEL profile = %+v", profile)
	}
	if profile.TimeMultiplier != 1 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 256 {
		t.Fatalf("CHAPEL resource profile = %+v", profile)
	}
}

func TestAlgol68ProfileUsesHardenedInterpreter(t *testing.T) {
	profile, ok := Resolve("ALGOL68")
	if !ok {
		t.Fatal("Resolve(ALGOL68) reported unsupported language")
	}
	if profile.Extension != "a68" || profile.DefaultTarget != "" || profile.CompileKind != "algol68" || profile.RunLang != "algol68" {
		t.Fatalf("ALGOL68 profile = %+v", profile)
	}
	if profile.TimeMultiplier != 2 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 256 {
		t.Fatalf("ALGOL68 resource profile = %+v", profile)
	}
}

func TestKokaProfileUsesPortableNativeCompiler(t *testing.T) {
	profile, ok := Resolve("KOKA")
	if !ok {
		t.Fatal("Resolve(KOKA) reported unsupported language")
	}
	if profile.Extension != "kk" || profile.DefaultTarget != "Main" || profile.CompileKind != "koka" || profile.RunLang != "binary" {
		t.Fatalf("KOKA profile = %+v", profile)
	}
	if profile.TimeMultiplier != 1 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 256 {
		t.Fatalf("KOKA resource profile = %+v", profile)
	}
}

func TestPonyProfileUsesBoundedRuntimeIdentity(t *testing.T) {
	profile, ok := Resolve("PONY")
	if !ok {
		t.Fatal("Resolve(PONY) reported unsupported language")
	}
	if profile.Extension != "pony" || profile.DefaultTarget != "Main" || profile.CompileKind != "pony" || profile.RunLang != "pony-binary" {
		t.Fatalf("PONY profile = %+v", profile)
	}
	if profile.TimeMultiplier != 1 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 256 {
		t.Fatalf("PONY resource profile = %+v", profile)
	}
}

func TestShellProfilesShareCompilerAndExposeRuntimeVariants(t *testing.T) {
	tests := map[string]string{
		"SHELL":    "bash",
		"BASH":     "bash",
		"POSIX_SH": "posix-sh",
	}
	for language, runLang := range tests {
		profile, ok := Resolve(language)
		if !ok {
			t.Fatalf("Resolve(%s) reported unsupported language", language)
		}
		if profile.Extension != "sh" || profile.DefaultTarget != "Main.sh" || profile.CompileKind != "shell" || profile.RunLang != runLang {
			t.Fatalf("%s profile = %+v", language, profile)
		}
		if profile.TimeMultiplier != 1 || profile.TimeOffsetMs != 0 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 64 {
			t.Fatalf("%s resource profile = %+v", language, profile)
		}
	}
}

func TestPowerShellProfileUsesScriptRuntimeAndManagedHeadroom(t *testing.T) {
	profile, ok := Resolve("POWERSHELL")
	if !ok {
		t.Fatal("Resolve(POWERSHELL) reported unsupported language")
	}
	if profile.Extension != "ps1" || profile.DefaultTarget != "Main.ps1" || profile.CompileKind != "powershell" || profile.RunLang != "powershell" {
		t.Fatalf("POWERSHELL profile = %+v", profile)
	}
	if profile.TimeMultiplier != 2 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 2 || profile.MemoryOffsetMB != 1024 {
		t.Fatalf("POWERSHELL resource profile = %+v", profile)
	}
}

func TestNormalizeRunLangCoffeeScriptIsCaseInsensitive(t *testing.T) {
	for _, input := range []string{"COFFEESCRIPT", "coffeescript", "CoffeeScript", " coffeescript "} {
		if got := NormalizeRunLang(input); got != "javascript" {
			t.Fatalf("NormalizeRunLang(%q) = %q, want javascript", input, got)
		}
		profile, ok := Resolve(input)
		if !ok || profile.RunLang != "javascript" {
			t.Fatalf("Resolve(%q) = (%+v, %v), want JavaScript run profile", input, profile, ok)
		}
	}
}

func TestGoProfilePreservesRuntimeIdentityAndMemoryReserve(t *testing.T) {
	profile, ok := Resolve("GO")
	if !ok {
		t.Fatal("Resolve(GO) reported unsupported language")
	}
	if profile.RunLang != "go-binary" || profile.MemoryOffsetMB != 1088 {
		t.Fatalf("GO profile = %+v, want go-binary with 1088 MiB reserve", profile)
	}
}

func TestPicoLispProfileUsesDotLSourceAndInterpreterRuntime(t *testing.T) {
	profile, ok := Resolve("PICOLISP")
	if !ok {
		t.Fatal("Resolve(PICOLISP) reported unsupported language")
	}
	if profile.Extension != "l" || profile.CompileKind != "picolisp" || profile.RunLang != "picolisp" {
		t.Fatalf("PICOLISP profile = %+v, want .l pass-through with PicoLisp runtime", profile)
	}
	if profile.TimeMultiplier != 2 || profile.TimeOffsetMs != 1000 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 256 {
		t.Fatalf("PICOLISP resource profile = %+v, want 2x+1000ms and 1x+256MiB", profile)
	}
}

func TestZerolangProfileBuildsNativeExecutableFromDotZeroSource(t *testing.T) {
	profile, ok := Resolve("ZEROLANG")
	if !ok {
		t.Fatal("Resolve(ZEROLANG) reported unsupported language")
	}
	if profile.Extension != "0" || profile.DefaultTarget != "Main" || profile.CompileKind != "zerolang" || profile.RunLang != "binary" {
		t.Fatalf("ZEROLANG profile = %+v", profile)
	}
	if profile.TimeMultiplier != 1 || profile.TimeOffsetMs != 0 || profile.MemoryMultiplier != 1 || profile.MemoryOffsetMB != 16 {
		t.Fatalf("ZEROLANG resource profile = %+v, want native defaults with 16 MiB reserve", profile)
	}
}

func TestMalbolgeProfileUsesValidatedBundledRuntime(t *testing.T) {
	profile, ok := Resolve("MALBOLGE")
	if !ok {
		t.Fatal("Resolve(MALBOLGE) reported unsupported language")
	}
	if profile.Extension != "mal" || profile.CompileKind != "malbolge" || profile.RunLang != "malbolge" {
		t.Fatalf("MALBOLGE profile = %+v, want .mal validated interpreter runtime", profile)
	}
	if profile.TimeMultiplier != 5 || profile.TimeOffsetMs != 1000 || profile.MemoryOffsetMB != 64 {
		t.Fatalf("MALBOLGE resource profile = %+v, want 5x+1000ms and 64 MiB reserve", profile)
	}
}
