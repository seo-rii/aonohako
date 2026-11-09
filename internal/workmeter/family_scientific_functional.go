package workmeter

func init() {
	registerFamily(familySpec{
		ID: "scientific-functional",
		SourceLanguages: []string{
			"ALGOL68",
			"RACKET",
			"SCHEME",
			"CHEZ_SCHEME",
			"GUILE",
			"GDL",
			"OCTAVE",
			"SMLNJ",
			"HAXE",
			"LISP",
			"PICOLISP",
			"SQLITE",
			"JULIA",
			"RAKU",
			"PROLOG",
			"FACTOR",
			"R",
			"SMALLTALK",
			"GST",
			"DUCKDB",
			"BQN",
			"J",
			"UIUA",
			"JANET",
			"FORTH",
			"GFORTH",
		},
		RunLanguages: []string{"algol68", "bqn", "chez-scheme", "duckdb", "factor", "forth", "gdl", "guile", "haxe", "j", "janet", "julia", "lisp", "octave", "picolisp", "prolog", "r", "racket", "raku", "scheme", "smalltalk", "smlnj", "sqlite", "uiua"},
	})
}
