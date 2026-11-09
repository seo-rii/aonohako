package workmeter

func init() {
	registerFamily(familySpec{
		ID: "specialized",
		SourceLanguages: []string{
			"AHEUI",
			"VHDL",
			"VERILOG",
			"SYSTEMVERILOG",
			"CUDA_OCELOT",
			"GRAPHQL",
			"TLA",
			"TLAPLUS",
			"WHITESPACE",
			"BEFUNGE",
			"BF",
			"MALBOLGE",
			"LOLCODE",
			"SHAKESPEARE",
			"PIET",
			"UNLAMBDA",
			"LAZYK",
			"VB6",
			"GOLFSCRIPT",
			"UHMLANG",
			"TEXT",
		},
		RunLanguages: []string{"aheui", "befunge", "brainfuck", "cuda-ocelot", "golfscript", "graphql", "lazyk", "lolcode", "malbolge", "piet", "shakespeare", "text", "tla", "uhmlang", "unlambda", "vb6", "verilog", "vhdl", "whitespace"},
	})
}
