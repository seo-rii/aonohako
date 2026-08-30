package profiles

import "strings"

const asProfileKey = "ASSEMBLYSCRIPT"

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
	"C":             {SourceLang: "C", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c11", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C89":           {SourceLang: "C89", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c90", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C99":           {SourceLang: "C99", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c99", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C11":           {SourceLang: "C11", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c11", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C17":           {SourceLang: "C17", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c17", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C18":           {SourceLang: "C18", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c17", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"C23":           {SourceLang: "C23", Extension: "c", DefaultTarget: "Main", CompileKind: "c", CompileStd: "c23", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP":           {SourceLang: "CPP", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++17", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP98":         {SourceLang: "CPP98", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++03", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP03":         {SourceLang: "CPP03", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++03", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP11":         {SourceLang: "CPP11", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++11", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP14":         {SourceLang: "CPP14", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++14", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP17":         {SourceLang: "CPP17", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++17", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP20":         {SourceLang: "CPP20", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++20", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP23":         {SourceLang: "CPP23", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++23", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"CPP26":         {SourceLang: "CPP26", Extension: "cpp", DefaultTarget: "Main", CompileKind: "cpp", CompileStd: "c++26", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1},
	"RUST":          {SourceLang: "RUST", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2018", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"RUST2015":      {SourceLang: "RUST2015", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2015", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"RUST2018":      {SourceLang: "RUST2018", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2018", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"RUST2021":      {SourceLang: "RUST2021", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2021", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"RUST2024":      {SourceLang: "RUST2024", Extension: "rs", DefaultTarget: "Main", CompileKind: "rust", RustEdition: "2024", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"MOONBIT":       {SourceLang: "MOONBIT", Extension: "mbt", DefaultTarget: "Main", CompileKind: "moonbit", RunLang: "binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"FENNEL":        {SourceLang: "FENNEL", Extension: "fnl", DefaultTarget: "Main.lua", CompileKind: "fennel", RunLang: "lua", TimeMultiplier: 1, TimeOffsetMs: 500, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"CHAPEL":        {SourceLang: "CHAPEL", Extension: "chpl", DefaultTarget: "Main", CompileKind: "chapel", RunLang: "chapel-binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"ALGOL68":       {SourceLang: "ALGOL68", Extension: "a68", CompileKind: "algol68", RunLang: "algol68", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"KOKA":          {SourceLang: "KOKA", Extension: "kk", DefaultTarget: "Main", CompileKind: "koka", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"PONY":          {SourceLang: "PONY", Extension: "pony", DefaultTarget: "Main", CompileKind: "pony", RunLang: "pony-binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"SHELL":         {SourceLang: "SHELL", Extension: "sh", DefaultTarget: "Main.sh", CompileKind: "shell", RunLang: "bash", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"BASH":          {SourceLang: "BASH", Extension: "sh", DefaultTarget: "Main.sh", CompileKind: "shell", RunLang: "bash", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"POSIX_SH":      {SourceLang: "POSIX_SH", Extension: "sh", DefaultTarget: "Main.sh", CompileKind: "shell", RunLang: "posix-sh", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"POWERSHELL":    {SourceLang: "POWERSHELL", Extension: "ps1", DefaultTarget: "Main.ps1", CompileKind: "powershell", RunLang: "powershell", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"ZEROLANG":      {SourceLang: "ZEROLANG", Extension: "0", DefaultTarget: "Main", CompileKind: "zerolang", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"GO":            {SourceLang: "GO", Extension: "go", DefaultTarget: "Main", CompileKind: "go", RunLang: "go-binary", TimeMultiplier: 1, TimeOffsetMs: 2000, MemoryMultiplier: 1, MemoryOffsetMB: 1088},
	"ZIG":           {SourceLang: "ZIG", Extension: "zig", DefaultTarget: "Main", CompileKind: "zig", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"JAVA":          {SourceLang: "JAVA", Extension: "java", CompileKind: "java", JavaRelease: "11", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"JAVA8":         {SourceLang: "JAVA8", Extension: "java", CompileKind: "java", JavaRelease: "8", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"JAVA11":        {SourceLang: "JAVA11", Extension: "java", CompileKind: "java", JavaRelease: "11", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"JAVA15":        {SourceLang: "JAVA15", Extension: "java", CompileKind: "java", JavaRelease: "15", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"JAVA17":        {SourceLang: "JAVA17", Extension: "java", CompileKind: "java", JavaRelease: "17", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"JAVA21":        {SourceLang: "JAVA21", Extension: "java", CompileKind: "java", JavaRelease: "21", RunLang: "java", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1280},
	"GROOVY":        {SourceLang: "GROOVY", Extension: "groovy", CompileKind: "groovy", RunLang: "groovy", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN":        {SourceLang: "KOTLIN", Extension: "kt", DefaultTarget: "Main", CompileKind: "kotlin", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 16},
	"ASM":           {SourceLang: "ASM", Extension: "s", DefaultTarget: "Main", CompileKind: "asm", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"AHEUI":         {SourceLang: "AHEUI", Extension: "aheui", CompileKind: "none", RunLang: "aheui", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"NASM":          {SourceLang: "NASM", Extension: "asm", DefaultTarget: "Main", CompileKind: "nasm", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"PASCAL":        {SourceLang: "PASCAL", Extension: "pas", DefaultTarget: "Main", CompileKind: "pascal", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"DELPHI":        {SourceLang: "DELPHI", Extension: "dpr", DefaultTarget: "Main", CompileKind: "delphi", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"OBJECTPASCAL":  {SourceLang: "OBJECTPASCAL", Extension: "pas", DefaultTarget: "Main", CompileKind: "objectpascal", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"NIM":           {SourceLang: "NIM", Extension: "nim", DefaultTarget: "Main", CompileKind: "nim", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"CLOJURE":       {SourceLang: "CLOJURE", Extension: "clj", CompileKind: "clojure", RunLang: "clojure", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"RACKET":        {SourceLang: "RACKET", Extension: "rkt", CompileKind: "racket", RunLang: "racket", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"SCHEME":        {SourceLang: "SCHEME", Extension: "scm", CompileKind: "scheme", RunLang: "scheme", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"AWK":           {SourceLang: "AWK", Extension: "awk", CompileKind: "awk", RunLang: "awk", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"TCL":           {SourceLang: "TCL", Extension: "tcl", CompileKind: "tcl", RunLang: "tcl", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"GDL":           {SourceLang: "GDL", Extension: "pro", CompileKind: "gdl", RunLang: "gdl", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"OCTAVE":        {SourceLang: "OCTAVE", Extension: "m", CompileKind: "octave", RunLang: "octave", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"ADA":           {SourceLang: "ADA", Extension: "adb", DefaultTarget: "Main", CompileKind: "ada", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"ADA2012":       {SourceLang: "ADA2012", Extension: "adb", DefaultTarget: "Main", CompileKind: "ada", CompileStd: "-gnat2012", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"ADA2022":       {SourceLang: "ADA2022", Extension: "adb", DefaultTarget: "Main", CompileKind: "ada", CompileStd: "-gnat2022", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"COBOL":         {SourceLang: "COBOL", Extension: "cob", DefaultTarget: "Main", CompileKind: "cobol", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"GNUCOBOL":      {SourceLang: "GNUCOBOL", Extension: "cob", DefaultTarget: "Main", CompileKind: "cobol", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"CYTHON":        {SourceLang: "CYTHON", Extension: "pyx", DefaultTarget: "Main", CompileKind: "cython", RunLang: "binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"DART":          {SourceLang: "DART", Extension: "dart", DefaultTarget: "Main", CompileKind: "dart", RunLang: "binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"FORTRAN":       {SourceLang: "FORTRAN", Extension: "f90", DefaultTarget: "Main", CompileKind: "fortran", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"FORTRAN95":     {SourceLang: "FORTRAN95", Extension: "f90", DefaultTarget: "Main", CompileKind: "fortran", CompileStd: "f95", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"FORTRAN2003":   {SourceLang: "FORTRAN2003", Extension: "f90", DefaultTarget: "Main", CompileKind: "fortran", CompileStd: "f2003", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"FORTRAN2008":   {SourceLang: "FORTRAN2008", Extension: "f90", DefaultTarget: "Main", CompileKind: "fortran", CompileStd: "f2008", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"FORTRAN2018":   {SourceLang: "FORTRAN2018", Extension: "f90", DefaultTarget: "Main", CompileKind: "fortran", CompileStd: "f2018", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"D":             {SourceLang: "D", Extension: "d", DefaultTarget: "Main", CompileKind: "d", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"OBJC":          {SourceLang: "OBJC", Extension: "m", DefaultTarget: "Main", CompileKind: "objective-c", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"OBJECTIVE_C":   {SourceLang: "OBJECTIVE_C", Extension: "m", DefaultTarget: "Main", CompileKind: "objective-c", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"OBJCPP":        {SourceLang: "OBJCPP", Extension: "mm", DefaultTarget: "Main", CompileKind: "objective-cpp", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"OBJECTIVE_CPP": {SourceLang: "OBJECTIVE_CPP", Extension: "mm", DefaultTarget: "Main", CompileKind: "objective-cpp", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"VHDL":          {SourceLang: "VHDL", Extension: "vhd", DefaultTarget: "main_tb", CompileKind: "vhdl", RunLang: "vhdl", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"VERILOG":       {SourceLang: "VERILOG", Extension: "v", DefaultTarget: "Main.vvp", CompileKind: "verilog", RunLang: "verilog", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"SYSTEMVERILOG": {SourceLang: "SYSTEMVERILOG", Extension: "sv", DefaultTarget: "Main.vvp", CompileKind: "verilog", RunLang: "verilog", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"CRYSTAL":       {SourceLang: "CRYSTAL", Extension: "cr", DefaultTarget: "Main", CompileKind: "crystal", RunLang: "binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"VALA":          {SourceLang: "VALA", Extension: "vala", DefaultTarget: "Main", CompileKind: "vala", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"VLANG":         {SourceLang: "VLANG", Extension: "v", DefaultTarget: "Main", CompileKind: "vlang", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"ODIN":          {SourceLang: "ODIN", Extension: "odin", DefaultTarget: "Main", CompileKind: "odin", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"C3":            {SourceLang: "C3", Extension: "c3", DefaultTarget: "Main", CompileKind: "c3", RunLang: "c3", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"HARE":          {SourceLang: "HARE", Extension: "ha", DefaultTarget: "Main", CompileKind: "hare", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"VBNET":         {SourceLang: "VBNET", Extension: "vb", CompileKind: "vbnet", RunLang: "vbnet", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 16},
	"VB":            {SourceLang: "VB", Extension: "vb", CompileKind: "vbnet", RunLang: "vbnet", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 16},
	"GLEAM":         {SourceLang: "GLEAM", Extension: "gleam", CompileKind: "gleam", RunLang: "gleam", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"CUDA_OCELOT":   {SourceLang: "CUDA_OCELOT", Extension: "cu", DefaultTarget: "Main", CompileKind: "cuda-ocelot", RunLang: "cuda-ocelot", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"CARBON":        {SourceLang: "CARBON", Extension: "carbon", DefaultTarget: "Main", CompileKind: "carbon", RunLang: "binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"GRAPHQL":       {SourceLang: "GRAPHQL", Extension: "graphql", CompileKind: "graphql", RunLang: "graphql", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"ROCQ":          {SourceLang: "ROCQ", Extension: "v", CompileKind: "rocq", RunLang: "rocq", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"COQ":           {SourceLang: "COQ", Extension: "v", CompileKind: "rocq", RunLang: "rocq", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"LEAN4":         {SourceLang: "LEAN4", Extension: "lean", CompileKind: "lean4", RunLang: "lean4", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"LEAN":          {SourceLang: "LEAN", Extension: "lean", CompileKind: "lean4", RunLang: "lean4", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"AGDA":          {SourceLang: "AGDA", Extension: "agda", CompileKind: "agda", RunLang: "agda", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1536},
	"DAFNY":         {SourceLang: "DAFNY", Extension: "dfy", CompileKind: "dafny", RunLang: "dafny", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"TLA":           {SourceLang: "TLA", Extension: "tla", CompileKind: "tla", RunLang: "tla", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"TLAPLUS":       {SourceLang: "TLAPLUS", Extension: "tla", CompileKind: "tla", RunLang: "tla", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"WHY3":          {SourceLang: "WHY3", Extension: "mlw", CompileKind: "why3", RunLang: "why3", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"WHYML":         {SourceLang: "WHYML", Extension: "mlw", CompileKind: "why3", RunLang: "why3", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"ISABELLE":      {SourceLang: "ISABELLE", Extension: "thy", CompileKind: "isabelle", RunLang: "isabelle", TimeMultiplier: 4, TimeOffsetMs: 3000, MemoryMultiplier: 3, MemoryOffsetMB: 2048},
	"FSTAR":         {SourceLang: "FSTAR", Extension: "fst", CompileKind: "fstar", RunLang: "fstar", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1536},
	"ALLOY":         {SourceLang: "ALLOY", Extension: "als", CompileKind: "alloy", RunLang: "alloy", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"ACL2":          {SourceLang: "ACL2", Extension: "lisp", CompileKind: "acl2", RunLang: "acl2", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1536},
	"KFRAMEWORK":    {SourceLang: "KFRAMEWORK", Extension: "k", CompileKind: "kframework", RunLang: "kframework", TimeMultiplier: 4, TimeOffsetMs: 3000, MemoryMultiplier: 3, MemoryOffsetMB: 2048},
	"HASKELL":       {SourceLang: "HASKELL", Extension: "hs", DefaultTarget: "Main", CompileKind: "haskell", RunLang: "binary", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"IDRIS2":        {SourceLang: "IDRIS2", Extension: "idr", DefaultTarget: "Main", CompileKind: "idris2", RunLang: "binary", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"SML":           {SourceLang: "SML", Extension: "sml", DefaultTarget: "Main", CompileKind: "sml", RunLang: "binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"HAXE":          {SourceLang: "HAXE", Extension: "hx", DefaultTarget: "Main.n", CompileKind: "haxe", RunLang: "haxe", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"LISP":          {SourceLang: "LISP", Extension: "lisp", CompileKind: "lisp", RunLang: "lisp", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"PICOLISP":      {SourceLang: "PICOLISP", Extension: "l", CompileKind: "picolisp", RunLang: "picolisp", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"SWIFT":         {SourceLang: "SWIFT", Extension: "swift", DefaultTarget: "Main", CompileKind: "swift", RunLang: "binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"SQLITE":        {SourceLang: "SQLITE", Extension: "sql", CompileKind: "sqlite", RunLang: "sqlite", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"JULIA":         {SourceLang: "JULIA", Extension: "jl", CompileKind: "julia", RunLang: "julia", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"RAKU":          {SourceLang: "RAKU", Extension: "raku", CompileKind: "raku", RunLang: "raku", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"ERLANG":        {SourceLang: "ERLANG", Extension: "erl", CompileKind: "erlang", RunLang: "erlang", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"MERCURY":       {SourceLang: "MERCURY", Extension: "m", DefaultTarget: "Main", CompileKind: "mercury", RunLang: "binary", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"PROLOG":        {SourceLang: "PROLOG", Extension: "pl", CompileKind: "prolog", RunLang: "prolog", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"SCALA":         {SourceLang: "SCALA", Extension: "scala", CompileKind: "scala", RunLang: "scala", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"FSHARP":        {SourceLang: "FSHARP", Extension: "fs", CompileKind: "fsharp", RunLang: "fsharp", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"WHITESPACE":    {SourceLang: "WHITESPACE", Extension: "ws", CompileKind: "whitespace", RunLang: "whitespace", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"BEFUNGE":       {SourceLang: "BEFUNGE", Extension: "bef", CompileKind: "befunge", RunLang: "befunge", TimeMultiplier: 4, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"BF":            {SourceLang: "BF", Extension: "bf", CompileKind: "brainfuck", RunLang: "brainfuck", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"MALBOLGE":      {SourceLang: "MALBOLGE", Extension: "mal", CompileKind: "malbolge", RunLang: "malbolge", TimeMultiplier: 5, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"LOLCODE":       {SourceLang: "LOLCODE", Extension: "lol", CompileKind: "lolcode", RunLang: "lolcode", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"APECODE":       {SourceLang: "APECODE", Extension: "ape", DefaultTarget: "Main", CompileKind: "apecode", RunLang: "binary", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"WASM":          {SourceLang: "WASM", Extension: "wat", DefaultTarget: "Main.wasm", CompileKind: "wasm", RunLang: "wasm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	asProfileKey:    {SourceLang: asProfileKey, Extension: "ts", DefaultTarget: "Main.wasm", CompileKind: "assemblyscript", RunLang: "assemblyscript", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"OCAML":         {SourceLang: "OCAML", Extension: "ml", DefaultTarget: "Main", CompileKind: "ocaml", RunLang: "ocaml", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"ELIXIR":        {SourceLang: "ELIXIR", Extension: "exs", CompileKind: "elixir", RunLang: "elixir", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1536},
	"COFFEESCRIPT":  {SourceLang: "COFFEESCRIPT", Extension: "coffee", DefaultTarget: "Main.js", CompileKind: "coffeescript", RunLang: "javascript", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"RESCRIPT":      {SourceLang: "RESCRIPT", Extension: "res", DefaultTarget: "Main.js", CompileKind: "rescript", RunLang: "javascript", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"PURESCRIPT":    {SourceLang: "PURESCRIPT", Extension: "purs", DefaultTarget: "Main.js", CompileKind: "purescript", RunLang: "javascript", TimeMultiplier: 4, TimeOffsetMs: 3000, MemoryMultiplier: 3, MemoryOffsetMB: 1536},
	"CSHARP":        {SourceLang: "CSHARP", Extension: "cs", CompileKind: "csharp", RunLang: "csharp", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 16},
	"R":             {SourceLang: "R", Extension: "R", CompileKind: "r", RunLang: "r", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"PYTHON3":       {SourceLang: "PYTHON3", Extension: "py", CompileKind: "python", RunLang: "python", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 32},
	"PYPY3":         {SourceLang: "PYPY3", Extension: "py", CompileKind: "pypy", RunLang: "pypy", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 128},
	"JAVASCRIPT":    {SourceLang: "JAVASCRIPT", Extension: "js", CompileKind: "javascript", RunLang: "javascript", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"TYPESCRIPT":    {SourceLang: "TYPESCRIPT", Extension: "ts", CompileKind: "typescript", RunLang: "javascript", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"ELM":           {SourceLang: "ELM", Extension: "elm", DefaultTarget: "Main.js", CompileKind: "elm", RunLang: "javascript", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"RUBY":          {SourceLang: "RUBY", Extension: "rb", CompileKind: "ruby", RunLang: "ruby", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"PHP":           {SourceLang: "PHP", Extension: "php", CompileKind: "php", RunLang: "php", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"PHP7":          {SourceLang: "PHP7", Extension: "php", CompileKind: "php", RunLang: "php", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"PHP8":          {SourceLang: "PHP8", Extension: "php", CompileKind: "php", RunLang: "php", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"LUA":           {SourceLang: "LUA", Extension: "lua", CompileKind: "lua", RunLang: "lua", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"PERL":          {SourceLang: "PERL", Extension: "pl", CompileKind: "perl", RunLang: "perl", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"VB6":           {SourceLang: "VB6", Extension: "bas", CompileKind: "vb6", RunLang: "vb6", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"FREEBASIC":     {SourceLang: "FREEBASIC", Extension: "bas", DefaultTarget: "Main", CompileKind: "freebasic", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"CLASSIC_BASIC": {SourceLang: "CLASSIC_BASIC", Extension: "bas", DefaultTarget: "Main", CompileKind: "classic-basic", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"QBASIC":        {SourceLang: "QBASIC", Extension: "bas", DefaultTarget: "Main", CompileKind: "classic-basic", RunLang: "binary", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 128},
	"SMALLTALK":     {SourceLang: "SMALLTALK", Extension: "st", CompileKind: "smalltalk", RunLang: "smalltalk", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"GST":           {SourceLang: "GST", Extension: "st", CompileKind: "smalltalk", RunLang: "smalltalk", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"GOLFSCRIPT":    {SourceLang: "GOLFSCRIPT", Extension: "gs", CompileKind: "golfscript", RunLang: "golfscript", TimeMultiplier: 3, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"MOJO":          {SourceLang: "MOJO", Extension: "mojo", DefaultTarget: "Main", CompileKind: "mojo", RunLang: "mojo-binary", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"DENO":          {SourceLang: "DENO", Extension: "ts", CompileKind: "deno", RunLang: "deno", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JVM":    {SourceLang: "KOTLIN_JVM", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "8", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JVM8":   {SourceLang: "KOTLIN_JVM8", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "8", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JVM11":  {SourceLang: "KOTLIN_JVM11", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "11", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JVM17":  {SourceLang: "KOTLIN_JVM17", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "17", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JVM21":  {SourceLang: "KOTLIN_JVM21", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "21", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JAVA":   {SourceLang: "KOTLIN_JAVA", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "8", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JAVA8":  {SourceLang: "KOTLIN_JAVA8", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "8", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JAVA11": {SourceLang: "KOTLIN_JAVA11", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "11", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JAVA17": {SourceLang: "KOTLIN_JAVA17", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "17", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"KOTLIN_JAVA21": {SourceLang: "KOTLIN_JAVA21", Extension: "kt", DefaultTarget: "Main.jar", CompileKind: "kotlin-jvm", JavaRelease: "21", RunLang: "kotlin-jvm", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"DUCKDB":        {SourceLang: "DUCKDB", Extension: "sql", CompileKind: "duckdb", RunLang: "duckdb", TimeMultiplier: 1, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"BQN":           {SourceLang: "BQN", Extension: "bqn", CompileKind: "bqn", RunLang: "bqn", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"APL":           {SourceLang: "APL", Extension: "apl", CompileKind: "apl", RunLang: "apl", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"GNU_APL":       {SourceLang: "GNU_APL", Extension: "apl", CompileKind: "apl", RunLang: "apl", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"J":             {SourceLang: "J", Extension: "ijs", CompileKind: "j", RunLang: "j", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"UIUA":          {SourceLang: "UIUA", Extension: "ua", CompileKind: "uiua", RunLang: "uiua", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 512},
	"JANET":         {SourceLang: "JANET", Extension: "janet", CompileKind: "janet", RunLang: "janet", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"SED":           {SourceLang: "SED", Extension: "sed", CompileKind: "sed", RunLang: "sed", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"BC":            {SourceLang: "BC", Extension: "bc", CompileKind: "bc", RunLang: "bc", TimeMultiplier: 1, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 64},
	"FORTH":         {SourceLang: "FORTH", Extension: "fs", CompileKind: "forth", RunLang: "forth", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"GFORTH":        {SourceLang: "GFORTH", Extension: "fs", CompileKind: "forth", RunLang: "forth", TimeMultiplier: 2, TimeOffsetMs: 1000, MemoryMultiplier: 1, MemoryOffsetMB: 256},
	"UHMLANG":       {SourceLang: "UHMLANG", Extension: "uhm", CompileKind: "none", RunLang: "uhmlang", TimeMultiplier: 3, TimeOffsetMs: 2000, MemoryMultiplier: 2, MemoryOffsetMB: 1024},
	"TEXT":          {SourceLang: "TEXT", Extension: "txt", CompileKind: "none", RunLang: "text", TimeMultiplier: 1, MemoryMultiplier: 1},
}

func Resolve(language string) (Profile, bool) {
	key := strings.ToUpper(strings.TrimSpace(language))
	p, ok := profiles[key]
	return p, ok
}

func All() map[string]Profile {
	out := make(map[string]Profile, len(profiles))
	for key, profile := range profiles {
		out[key] = profile
	}
	return out
}

func NormalizeRunLang(language string) string {
	raw := strings.TrimSpace(language)
	if raw == strings.ToUpper(raw) {
		if profile, ok := profiles[raw]; ok {
			return profile.RunLang
		}
	}
	key := strings.ToLower(raw)
	switch key {
	case "binary", "go-binary", "mojo-binary", "pony-binary", "python", "pypy", "java", "javascript", "ruby", "php", "lua", "perl", "uhmlang", "text", "csharp", "ocaml", "elixir", "sqlite", "julia", "erlang", "prolog", "r", "groovy", "scala", "fsharp", "whitespace", "befunge", "brainfuck", "malbolge", "lolcode", "apecode", "wasm", "assemblyscript", "lisp", "picolisp", "rocq", "clojure", "racket", "scheme", "awk", "tcl", "gdl", "octave", "vhdl", "verilog", "c3", "vbnet", "vb6", "gleam", "cuda-ocelot", "graphql", "lean4", "agda", "dafny", "tla", "why3", "isabelle", "fstar", "alloy", "acl2", "kframework", "smalltalk", "golfscript", "deno", "kotlin-jvm", "duckdb", "bqn", "apl", "j", "uiua", "janet", "aheui", "haxe", "raku", "sed", "bc", "forth", "algol68", "bash", "posix-sh", "powershell":
		return key
	case "shell":
		return "bash"
	case "posix_sh", "posixsh", "sh":
		return "posix-sh"
	case "pwsh":
		return "powershell"
	case "coffeescript":
		// The CoffeeScript compiler emits JavaScript artifacts, so execute uses
		// the JavaScript runtime regardless of the compile-language casing.
		return "javascript"
	case "coq":
		return "rocq"
	case "kotlin-java", "kotlin/java", "kotlinjava", "kotlin-jvm8", "kotlin-jvm11", "kotlin-jvm17", "kotlin-jvm21", "kotlin-java8", "kotlin-java11", "kotlin-java17", "kotlin-java21", "kotlin/java8", "kotlin/java11", "kotlin/java17", "kotlin/java21":
		return "kotlin-jvm"
	case "bf":
		return "brainfuck"
	case "zero", "zerolang":
		return "binary"
	case "lol":
		return "lolcode"
	case "befunge93", "befunge-93", "bf93":
		return "befunge"
	case "vb":
		return "vbnet"
	case "gawk":
		return "awk"
	case "gnudatalanguage":
		return "gdl"
	case "systemverilog":
		return "verilog"
	case "lean":
		return "lean4"
	case "tlaplus":
		return "tla"
	case "whyml":
		return "why3"
	case "f*", "f-star":
		return "fstar"
	case "k", "k-framework":
		return "kframework"
	case "gst":
		return "smalltalk"
	case "gnu-apl":
		return "apl"
	case "jsoftware":
		return "j"
	case "gnucobol":
		return "binary"
	case "ada2012", "ada12", "ada2022", "ada22":
		return "binary"
	case "fortran95", "f95", "fortran2003", "fortran03", "f2003", "f03":
		return "binary"
	case "fortran2008", "fortran08", "f2008", "f08", "fortran2018", "fortran18", "f2018", "f18":
		return "binary"
	case "objective-c", "objective-cpp", "objc", "objcpp", "freebasic", "classic-basic", "qbasic", "vala", "mercury", "idris2", "delphi", "object-pascal", "object_pascal", "objpascal", "objfpc", "moonbit", "koka":
		return "binary"
	case "mojo":
		return "mojo-binary"
	case "gforth":
		return "forth"
	}
	if p, ok := Resolve(language); ok {
		return p.RunLang
	}
	return key
}
