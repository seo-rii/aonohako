package durante

func init() {
	registerFamily(familySpec{
		ID: "cpython",
		SourceLanguages: []string{
			"PYTHON3",
		},
		RunLanguages: []string{"python"},
	})
}
