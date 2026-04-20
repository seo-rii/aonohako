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
		"JAVASCRIPT",
		"TYPESCRIPT",
		"GO",
		"RUST",
		"RUST2018",
		"RUST2021",
		"RUST2024",
		"KOTLIN",
		"HASKELL",
		"SWIFT",
		"SQLITE",
		"JULIA",
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
		"HASKELL":    "binary",
		"SWIFT":      "binary",
		"SQLITE":     "sqlite",
		"JULIA":      "julia",
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
