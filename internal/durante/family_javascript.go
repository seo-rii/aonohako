package durante

func init() {
	registerFamily(familySpec{
		ID: "javascript-wasm",
		SourceLanguages: []string{
			"ENTRY",
			"WASM",
			"ASSEMBLYSCRIPT",
			"COFFEESCRIPT",
			"RESCRIPT",
			"PURESCRIPT",
			"JAVASCRIPT",
			"TYPESCRIPT",
			"ELM",
			"DENO",
			"JAVASCRIPT_DENO",
			"TYPESCRIPT_DENO",
			"JAVASCRIPT_QUICKJS",
			"APL",
			"GNU_APL",
			"JAVASCRIPT_BUN",
			"TYPESCRIPT_BUN",
		},
		RunLanguages: []string{"apl", "assemblyscript", "bun", "deno", "entry", "javascript", "quickjs", "wasm"},
	})
}
