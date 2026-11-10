package durante

func init() {
	registerFamily(familySpec{
		ID: "dotnet",
		SourceLanguages: []string{
			"POWERSHELL",
			"VBNET",
			"VB",
			"FSHARP",
			"CSHARP",
		},
		RunLanguages: []string{"csharp", "fsharp", "powershell", "vbnet"},
	})
}
