package workmeter

func init() {
	registerFamily(familySpec{
		ID: "scripting",
		SourceLanguages: []string{
			"FENNEL",
			"SHELL",
			"BASH",
			"POSIX_SH",
			"ZSH",
			"FISH",
			"AWK",
			"TCL",
			"RUBY",
			"PHP",
			"PHP7",
			"PHP8",
			"LUA",
			"LUA54",
			"LUAJIT",
			"PERL",
			"SED",
			"BC",
		},
		RunLanguages: []string{"awk", "bash", "bc", "fish", "lua", "luajit", "perl", "php", "posix-sh", "ruby", "sed", "tcl", "zsh"},
	})
}
