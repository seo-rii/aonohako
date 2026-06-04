package compile

var compileRegistry = map[string]Compiler{
	"c": nativeCompiler{exts: []string{".c", ".h"}, bin: "gcc", flags: func(job CompileJob) []string {
		return []string{"-O2", "-Wall", "-lm", "--static", "-DONLINE_JUDGE=1", "-std=" + job.Profile.CompileStd}
	}},
	"cpp": nativeCompiler{exts: []string{".cpp", ".cc", ".cxx", ".h", ".hpp"}, bin: "g++", flags: func(job CompileJob) []string {
		return []string{"-O2", "-Wall", "-lm", "--static", "-pipe", "-DONLINE_JUDGE=1", "-std=" + job.Profile.CompileStd}
	}},
	"asm": nativeCompiler{exts: []string{".s"}, bin: "gcc", flags: func(CompileJob) []string { return []string{"-nostdlib", "-static", "-no-pie"} }},
	"pascal": singleSourceExecutableCompiler{exts: []string{".pas"}, preferredBases: []string{"Main.pas"}, noSourceReason: "no pascal sources", bin: "fpc", args: func(job CompileJob, sourcePath string) []string {
		return []string{"-O2", "-Xs", "-dONLINE_JUDGE", "-o" + outputPath(job), sourcePath}
	}},
	"nim": singleSourceExecutableCompiler{exts: []string{".nim"}, preferredBases: []string{"Main.nim"}, noSourceReason: "no nim sources", bin: "nim", args: func(job CompileJob, sourcePath string) []string {
		return []string{"c", "-d:release", "-d:ONLINE_JUDGE", "--opt:speed", "--out:" + outputPath(job), sourcePath}
	}},
	"zig": singleSourceExecutableCompiler{exts: []string{".zig"}, preferredBases: []string{"Main.zig"}, noSourceReason: "no zig sources", bin: "zig", args: func(job CompileJob, sourcePath string) []string {
		return []string{"build-exe", sourcePath, "-O", "ReleaseSafe", "-femit-bin=" + outputPath(job)}
	}},
	"sml": singleSourceExecutableCompiler{exts: []string{".sml"}, preferredBases: []string{"Main.sml"}, noSourceReason: "no sml sources", bin: "mlton", args: func(job CompileJob, sourcePath string) []string {
		return []string{"-output", outputPath(job), sourcePath}
	}},
	"idris2":  idris2Compiler{},
	"rust":    rustCompiler{},
	"go":      goCompiler{},
	"java":    javaCompiler{},
	"groovy":  groovyCompiler{},
	"clojure": clojureCompiler{},
	"racket":  scriptCheckCompiler{bin: "raco", prefix: []string{"make"}},
	"scheme":  passThroughCompiler{exts: []string{".scm"}, noSourceReason: "no scheme sources"},
	"awk":     checkedSourcesCompiler{exts: []string{".awk"}, noSourceReason: "no awk sources", bin: "gawk", prefix: []string{"--sandbox", "--lint", "-f"}},
	"tcl":     passThroughCompiler{exts: []string{".tcl"}, noSourceReason: "no tcl sources"},
	"gdl":     passThroughCompiler{exts: []string{".pro"}, noSourceReason: "no gdl sources"},
	"octave":  passThroughCompiler{exts: []string{".m"}, noSourceReason: "no octave sources"},
	"carbon":  checkedSourcesCompiler{exts: []string{".carbon"}, noSourceReason: "no carbon sources", bin: "carbon", prefix: []string{"compile", "--phase=check"}},
	"graphql": passThroughCompiler{exts: []string{".graphql"}, noSourceReason: "no graphql sources"},
	"lean4":   checkedSourcesCompiler{exts: []string{".lean"}, noSourceReason: "no lean sources", bin: "lean"},
	"agda":    checkedSourcesCompiler{exts: []string{".agda"}, noSourceReason: "no agda sources", bin: "agda"},
	"dafny":   checkedSourcesCompiler{exts: []string{".dfy"}, noSourceReason: "no dafny sources", bin: "dafny", prefix: []string{"verify", "--cores", "1"}, env: []string{"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1", "DOTNET_PROCESSOR_COUNT=1", "COMPlus_ThreadPool_ForceMinWorkerThreads=1"}},
	"tla":     passThroughCompiler{exts: []string{".tla", ".cfg"}, noSourceReason: "no tla sources"},
	"why3":    checkedSourcesCompiler{exts: []string{".mlw"}, noSourceReason: "no why3 sources", bin: "aonohako-why3-prove"},
	"vhdl":    vhdlCompiler{},
	"verilog": verilogCompiler{},
	"crystal": crystalCompiler{},
	"vala": singleSourceExecutableCompiler{exts: []string{".vala"}, preferredBases: []string{"Main.vala"}, noSourceReason: "no vala sources", bin: "valac", args: func(job CompileJob, sourcePath string) []string {
		return []string{"-O", "--define=ONLINE_JUDGE", "-o", outputPath(job), sourcePath}
	}},
	"vlang":       vlangCompiler{},
	"odin":        odinCompiler{},
	"c3":          c3Compiler{},
	"hare":        hareCompiler{},
	"vbnet":       vbnetCompiler{},
	"gleam":       gleamCompiler{},
	"cuda-ocelot": cudaOcelotCompiler{},
	"rocq":        rocqCompiler{},
	"isabelle":    isabelleCompiler{},
	"python":      pythonLikeCompiler{interpreter: "python3"},
	"pypy":        pythonLikeCompiler{interpreter: "pypy3"},
	"javascript":  scriptCheckCompiler{exts: []string{".js"}, noSourceReason: "no javascript sources", bin: "node", prefix: []string{"--check"}},
	"ruby":        scriptCheckCompiler{exts: []string{".rb"}, noSourceReason: "no ruby sources", bin: "ruby", prefix: []string{"-c"}},
	"php":         scriptCheckCompiler{exts: []string{".php"}, noSourceReason: "no php sources", bin: "php", prefix: []string{"-l"}},
	"lua":         scriptCheckCompiler{exts: []string{".lua"}, noSourceReason: "no lua sources", bin: "luac5.4", prefix: []string{"-p"}},
	"perl":        scriptCheckCompiler{exts: []string{".pl"}, noSourceReason: "no perl sources", bin: "perl", prefix: []string{"-c"}},
	"fortran":     nativeCompiler{exts: []string{".f", ".for", ".f90", ".f95", ".f03", ".f08"}, bin: "gfortran", flags: func(CompileJob) []string { return []string{"-O2", "-pipe"} }},
	"ada": singleSourceExecutableCompiler{exts: []string{".adb"}, preferredBases: []string{"Main.adb"}, noSourceReason: "no ada sources", bin: "gnatmake", args: func(job CompileJob, sourcePath string) []string {
		return []string{"-O2", "-o", outputPath(job), sourcePath}
	}},
	"d": singleSourceExecutableCompiler{exts: []string{".d"}, preferredBases: []string{"Main.d"}, noSourceReason: "no d sources", bin: "ldc2", args: func(job CompileJob, sourcePath string) []string {
		return []string{sourcePath, "-O3", "-release", "--d-version=ONLINE_JUDGE", "-of=" + outputPath(job)}
	}},
	"objective-c": nativeCompiler{exts: []string{".m"}, bin: "clang", flags: func(CompileJob) []string {
		return []string{"-O2", "-pipe", "-DONLINE_JUDGE=1", "-L/usr/lib/gcc/x86_64-linux-gnu/16", "-lobjc"}
	}},
	"objective-cpp": nativeCompiler{exts: []string{".mm"}, bin: "clang++", flags: func(CompileJob) []string {
		return []string{"-O2", "-pipe", "-DONLINE_JUDGE=1", "-L/usr/lib/gcc/x86_64-linux-gnu/16", "-lobjc"}
	}},
	"raku": checkedSourcesCompiler{exts: []string{".raku", ".rakumod", ".p6", ".pl6"}, noSourceReason: "no raku sources", bin: "raku", prefix: []string{"-c"}},
	"r":    rCompiler{},
	"mercury": singleSourceExecutableCompiler{exts: []string{".m"}, preferredBases: []string{"main.m", "Main.m"}, noSourceReason: "no mercury sources", bin: "mmc", args: func(job CompileJob, sourcePath string) []string {
		return []string{"--make", "--grade", "hlc.gc", "-o", outputPath(job), sourcePath}
	}},
	"prolog":        prologCompiler{},
	"lisp":          lispCompiler{},
	"nasm":          nasmCompiler{},
	"erlang":        erlangCompiler{},
	"vb6":           passThroughCompiler{exts: []string{".bas", ".frm", ".cls"}, noSourceReason: "no vb6 sources"},
	"smalltalk":     passThroughCompiler{exts: []string{".st"}, noSourceReason: "no smalltalk sources"},
	"golfscript":    passThroughCompiler{exts: []string{".gs"}, noSourceReason: "no golfscript sources"},
	"duckdb":        passThroughCompiler{exts: []string{".sql"}, noSourceReason: "no duckdb sources"},
	"bqn":           passThroughCompiler{exts: []string{".bqn"}, noSourceReason: "no bqn sources"},
	"apl":           passThroughCompiler{exts: []string{".apl"}, noSourceReason: "no apl sources"},
	"uiua":          passThroughCompiler{exts: []string{".ua"}, noSourceReason: "no uiua sources"},
	"janet":         passThroughCompiler{exts: []string{".janet"}, noSourceReason: "no janet sources"},
	"sed":           checkedSourcesCompiler{exts: []string{".sed"}, noSourceReason: "no sed sources", bin: "sed", prefix: []string{"-n", "-f"}},
	"bc":            passThroughCompiler{exts: []string{".bc"}, noSourceReason: "no bc sources"},
	"forth":         passThroughCompiler{exts: []string{".fs", ".fth", ".4th"}, noSourceReason: "no forth sources"},
	"typescript":    typeScriptCompiler{},
	"kotlin":        kotlinNativeCompiler{},
	"cobol":         cobolCompiler{},
	"cython":        cythonCompiler{},
	"haskell":       haskellCompiler{},
	"elm":           elmCompiler{},
	"haxe":          haxeCompiler{},
	"swift":         swiftCompiler{},
	"sqlite":        sqliteCompiler{},
	"julia":         juliaCompiler{},
	"scala":         scalaCompiler{},
	"fsharp":        fsharpCompiler{},
	"freebasic":     freeBasicCompiler{noSourceReason: "no freebasic sources"},
	"classic-basic": freeBasicCompiler{dialectArgs: []string{"-lang", "qb"}, noSourceReason: "no classic-basic sources"},
	"mojo":          mojoCompiler{},
	"deno":          denoCompiler{},
	"kotlin-jvm":    kotlinJVMCompiler{},
	"coffeescript":  coffeeScriptCompiler{},
	"rescript":      reScriptCompiler{},
	"purescript":    pureScriptCompiler{},
	"whitespace":    whitespaceCompiler{},
	"brainfuck":     brainfuckCompiler{},
	"wasm":          wasmCompiler{},
	"ocaml":         ocamlCompiler{},
	"elixir":        elixirCompiler{},
	"csharp":        csharpCompiler{},
	"dart": singleSourceExecutableCompiler{exts: []string{".dart"}, preferredBases: []string{"Main.dart"}, noSourceReason: "no dart sources", bin: "dart", env: []string{"DART_SUPPRESS_ANALYTICS=true"}, args: func(job CompileJob, sourcePath string) []string {
		return []string{"compile", "exe", "-D", "ONLINE_JUDGE=true", sourcePath, "-o", outputPath(job)}
	}},
	"none": noneCompiler{},
}

func lookupCompiler(kind string) (Compiler, bool) {
	compiler, ok := compileRegistry[kind]
	return compiler, ok
}
