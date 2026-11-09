package workmeter

func init() {
	registerFamily(familySpec{
		ID: "beam",
		SourceLanguages: []string{
			"GLEAM",
			"ERLANG",
			"ELIXIR",
		},
		RunLanguages: []string{"elixir", "erlang", "gleam"},
	})
}
