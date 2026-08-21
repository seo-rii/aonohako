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
		"LOLCODE",
		"APECODE",
		"WASM",
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

	for input, want := range tests {
		if got := NormalizeRunLang(input); got != want {
			t.Fatalf("NormalizeRunLang(%q) = %q, want %q", input, got, want)
		}
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
