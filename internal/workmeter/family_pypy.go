package workmeter

func init() {
	registerFamily(familySpec{
		ID: "pypy",
		SourceLanguages: []string{
			"PYPY3",
		},
		RunLanguages: []string{"pypy"},
	})
}
