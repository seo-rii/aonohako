package profiles

import "strings"

type Profile struct {
	SourceLang       string
	Extension        string
	DefaultTarget    string
	CompileKind      string
	CompileStd       string
	JavaRelease      string
	RustEdition      string
	RunLang          string
	TimeMultiplier   int
	TimeOffsetMs     int
	MemoryMultiplier int
	MemoryOffsetMB   int
}

var profiles = map[string]Profile{
	"C":          {SourceLang: "C", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c11", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C99":        {SourceLang: "C99", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c99", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C11":        {SourceLang: "C11", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c11", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C18":        {SourceLang: "C18", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c11", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP":        {SourceLang: "CPP", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++17", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP03":      {SourceLang: "CPP03", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++03", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP11":      {SourceLang: "CPP11", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++11", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP14":      {SourceLang: "CPP14", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++14", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP17":      {SourceLang: "CPP17", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++17", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP20":      {SourceLang: "CPP20", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++20", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP23":      {SourceLang: "CPP23", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++23", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP26":      {SourceLang: "CPP26", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++23", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"RUST":       {SourceLang: "RUST", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2018", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"RUST2018":   {SourceLang: "RUST2018", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2018", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"RUST2021":   {SourceLang: "RUST2021", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2021", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"RUST2024":   {SourceLang: "RUST2024", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2024", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"GO":         {SourceLang: "GO", Extension: "go", DefaultTarget: "Main", CompileKind: "go", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 2000, MemoryMultiplier: 1, MemoryOffsetMB: 1024},
	"JAVA":       {SourceLang: "JAVA", Extension: "java", CompileKind: "java", JavaRelease: "11", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"JAVA8":      {SourceLang: "JAVA8", Extension: "java", CompileKind: "java", JavaRelease: "8", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"JAVA11":     {SourceLang: "JAVA11", Extension: "java", CompileKind: "java", JavaRelease: "11", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"JAVA15":     {SourceLang: "JAVA15", Extension: "java", CompileKind: "java", JavaRelease: "15", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"KOTLIN":     {SourceLang: "KOTLIN", Extension: "kt", DefaultTarget: "Main", CompileKind: "kotlin", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"HASKELL":    {SourceLang: "HASKELL", Extension: "hs", DefaultTarget: "Main", CompileKind: "haskell", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"SWIFT":      {SourceLang: "SWIFT", Extension: "swift", DefaultTarget: "Main", CompileKind: "swift", RunLang: "binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"SQLITE":     {SourceLang: "SQLITE", Extension: "sql", CompileKind: "sqlite", RunLang: "sqlite", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"JULIA":      {SourceLang: "JULIA", Extension: "jl", CompileKind: "julia", RunLang: "julia", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"ERLANG":     {SourceLang: "ERLANG", Extension: "erl", CompileKind: "erlang", RunLang: "erlang", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"SCALA":      {SourceLang: "SCALA", Extension: "scala", CompileKind: "scala", RunLang: "scala", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"FSHARP":     {SourceLang: "FSHARP", Extension: "fs", CompileKind: "fsharp", RunLang: "fsharp", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"WHITESPACE": {SourceLang: "WHITESPACE", Extension: "ws", CompileKind: "whitespace", RunLang: "whitespace", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"BF":         {SourceLang: "BF", Extension: "bf", CompileKind: "brainfuck", RunLang: "brainfuck", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"WASM":       {SourceLang: "WASM", Extension: "wat", DefaultTarget: "Main.wasm", CompileKind: "wasm", RunLang: "wasm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"OCAML":      {SourceLang: "OCAML", Extension: "ml", DefaultTarget: "Main", CompileKind: "ocaml", RunLang: "ocaml", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"ELIXIR":     {SourceLang: "ELIXIR", Extension: "exs", CompileKind: "elixir", RunLang: "elixir", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1536},
	"CSHARP":     {SourceLang: "CSHARP", Extension: "cs", CompileKind: "csharp", RunLang: "csharp", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 16},
	"PYTHON3":    {SourceLang: "PYTHON3", Extension: "py", CompileKind: "python", RunLang: "python", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 32},
	"PYPY3":      {SourceLang: "PYPY3", Extension: "py", CompileKind: "pypy", RunLang: "pypy", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 128},
	"JAVASCRIPT": {SourceLang: "JAVASCRIPT", Extension: "js", CompileKind: "javascript", RunLang: "javascript", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"TYPESCRIPT": {SourceLang: "TYPESCRIPT", Extension: "ts", CompileKind: "typescript", RunLang: "javascript", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"RUBY":       {SourceLang: "RUBY", Extension: "rb", CompileKind: "ruby", RunLang: "ruby", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"PHP":        {SourceLang: "PHP", Extension: "php", CompileKind: "php", RunLang: "php", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"LUA":        {SourceLang: "LUA", Extension: "lua", CompileKind: "lua", RunLang: "lua", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"PERL":       {SourceLang: "PERL", Extension: "pl", CompileKind: "perl", RunLang: "perl", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"UHMLANG":    {SourceLang: "UHMLANG", Extension: "uhm", CompileKind: "none", RunLang: "uhmlang", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"TEXT":       {SourceLang: "TEXT", Extension: "txt", CompileKind: "none", RunLang: "text", TimeMultiplier: 1, MemoryMultiplier: 1},
}

func Resolve(language string) (Profile, bool) {
	key := strings.ToUpper(strings.TrimSpace(language))
	p, ok := profiles[key]
	return p, ok
}

func NormalizeRunLang(language string) string {
	key := strings.ToLower(strings.TrimSpace(language))
	switch key {
	case "binary", "python", "pypy", "java", "javascript", "ruby", "php", "lua", "perl", "uhmlang", "text", "csharp", "ocaml", "elixir", "sqlite", "julia", "erlang", "scala", "fsharp", "whitespace", "brainfuck", "wasm":
		return key
	case "bf":
		return "brainfuck"
	}
	if p, ok := Resolve(language); ok {
		return p.RunLang
	}
	return key
}
