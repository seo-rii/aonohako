package compile

var compileRegistry = map[string]Compiler{
	"racket":     scriptCheckCompiler{bin: "raco", prefix: []string{"make"}},
	"scheme":     passThroughCompiler{exts: []string{".scm"}, noSourceReason: "no scheme sources"},
	"awk":        checkedSourcesCompiler{exts: []string{".awk"}, noSourceReason: "no awk sources", bin: "gawk", prefix: []string{"--sandbox", "--lint", "-f"}},
	"tcl":        passThroughCompiler{exts: []string{".tcl"}, noSourceReason: "no tcl sources"},
	"gdl":        passThroughCompiler{exts: []string{".pro"}, noSourceReason: "no gdl sources"},
	"octave":     passThroughCompiler{exts: []string{".m"}, noSourceReason: "no octave sources"},
	"carbon":     checkedSourcesCompiler{exts: []string{".carbon"}, noSourceReason: "no carbon sources", bin: "carbon", prefix: []string{"compile", "--phase=check"}},
	"graphql":    passThroughCompiler{exts: []string{".graphql"}, noSourceReason: "no graphql sources"},
	"lean4":      checkedSourcesCompiler{exts: []string{".lean"}, noSourceReason: "no lean sources", bin: "lean"},
	"agda":       checkedSourcesCompiler{exts: []string{".agda"}, noSourceReason: "no agda sources", bin: "agda"},
	"dafny":      checkedSourcesCompiler{exts: []string{".dfy"}, noSourceReason: "no dafny sources", bin: "dafny", prefix: []string{"verify", "--cores", "1"}, env: []string{"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1", "DOTNET_PROCESSOR_COUNT=1", "COMPlus_ThreadPool_ForceMinWorkerThreads=1"}},
	"tla":        passThroughCompiler{exts: []string{".tla", ".cfg"}, noSourceReason: "no tla sources"},
	"why3":       checkedSourcesCompiler{exts: []string{".mlw"}, noSourceReason: "no why3 sources", bin: "aonohako-why3-prove"},
	"javascript": scriptCheckCompiler{bin: "node", prefix: []string{"--check"}},
	"ruby":       scriptCheckCompiler{bin: "ruby", prefix: []string{"-c"}},
	"php":        scriptCheckCompiler{bin: "php", prefix: []string{"-l"}},
	"lua":        scriptCheckCompiler{bin: "luac5.4", prefix: []string{"-p"}},
	"perl":       scriptCheckCompiler{bin: "perl", prefix: []string{"-c"}},
	"raku":       checkedSourcesCompiler{exts: []string{".raku", ".rakumod", ".p6", ".pl6"}, noSourceReason: "no raku sources", bin: "raku", prefix: []string{"-c"}},
	"vb6":        passThroughCompiler{exts: []string{".bas", ".frm", ".cls"}, noSourceReason: "no vb6 sources"},
	"smalltalk":  passThroughCompiler{exts: []string{".st"}, noSourceReason: "no smalltalk sources"},
	"golfscript": passThroughCompiler{exts: []string{".gs"}, noSourceReason: "no golfscript sources"},
	"duckdb":     passThroughCompiler{exts: []string{".sql"}, noSourceReason: "no duckdb sources"},
	"bqn":        passThroughCompiler{exts: []string{".bqn"}, noSourceReason: "no bqn sources"},
	"apl":        passThroughCompiler{exts: []string{".apl"}, noSourceReason: "no apl sources"},
	"uiua":       passThroughCompiler{exts: []string{".ua"}, noSourceReason: "no uiua sources"},
	"janet":      passThroughCompiler{exts: []string{".janet"}, noSourceReason: "no janet sources"},
	"sed":        checkedSourcesCompiler{exts: []string{".sed"}, noSourceReason: "no sed sources", bin: "sed", prefix: []string{"-n", "-f"}},
	"bc":         passThroughCompiler{exts: []string{".bc"}, noSourceReason: "no bc sources"},
	"forth":      passThroughCompiler{exts: []string{".fs", ".fth", ".4th"}, noSourceReason: "no forth sources"},
}

func lookupCompiler(kind string) (Compiler, bool) {
	compiler, ok := compileRegistry[kind]
	return compiler, ok
}
