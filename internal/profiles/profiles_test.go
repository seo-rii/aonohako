package profiles

import "testing"

func TestResolveSupportsDoolSourceLanguages(t *testing.T) {
	languages := []string{
		"TEXT",
		"C",
		"C99",
		"C11",
		"C18",
		"CPP",
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
		"GROOVY",
		"JAVASCRIPT",
		"TYPESCRIPT",
		"GO",
		"ZIG",
		"RUST",
		"RUST2018",
		"RUST2021",
		"RUST2024",
		"KOTLIN",
		"FORTRAN",
		"D",
		"COQ",
		"HASKELL",
		"LISP",
		"SWIFT",
		"SQLITE",
		"JULIA",
		"ERLANG",
		"PROLOG",
		"SCALA",
		"FSHARP",
		"WHITESPACE",
		"BF",
		"WASM",
		"OCAML",
		"ELIXIR",
		"RUBY",
		"PHP",
		"CSHARP",
		"R",
		"LUA",
		"PERL",
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
		"OCAML":      "ocaml",
		"ELIXIR":     "elixir",
		"FORTRAN":    "binary",
		"D":          "binary",
		"COQ":        "coq",
		"HASKELL":    "binary",
		"LISP":       "lisp",
		"ZIG":        "binary",
		"SWIFT":      "binary",
		"SQLITE":     "sqlite",
		"JULIA":      "julia",
		"ERLANG":     "erlang",
		"PROLOG":     "prolog",
		"R":          "r",
		"GROOVY":     "groovy",
		"SCALA":      "scala",
		"FSHARP":     "fsharp",
		"WASM":       "wasm",
		"BF":         "brainfuck",
		"WHITESPACE": "whitespace",
	}

	for input, want := range tests {
		if got := NormalizeRunLang(input); got != want {
			t.Fatalf("NormalizeRunLang(%q) = %q, want %q", input, got, want)
		}
	}
}
