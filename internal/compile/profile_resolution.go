package compile

import (
	"fmt"
	"strings"

	"aonohako/internal/profiles"
)

func resolveProfile(lang string) (profiles.Profile, bool) {
	l := strings.TrimSpace(lang)
	switch strings.ToLower(l) {
	case "asm", "asm64", "assembly", "gas":
		l = "ASM"
	case "aheui":
		l = "AHEUI"
	case "nasm", "nasm64":
		l = "NASM"
	case "python", "python3":
		l = "PYTHON3"
	case "pypy", "pypy3":
		l = "PYPY3"
	case "r":
		l = "R"
	case "go", "golang":
		l = "GO"
	case "zig":
		l = "ZIG"
	case "zero", "zerolang":
		l = "ZEROLANG"
	case "pascal", "freepascal", "fpc":
		l = "PASCAL"
	case "delphi":
		l = "DELPHI"
	case "objectpascal", "object-pascal", "object_pascal", "objpascal", "objfpc":
		l = "OBJECTPASCAL"
	case "nim":
		l = "NIM"
	case "clojure":
		l = "CLOJURE"
	case "racket":
		l = "RACKET"
	case "scheme":
		l = "SCHEME"
	case "chez", "chezscheme", "chez-scheme", "chez_scheme":
		l = "CHEZ_SCHEME"
	case "guile", "guile3", "guile-3.0":
		l = "GUILE"
	case "chicken", "chicken-scheme", "chicken_scheme", "csc":
		l = "CHICKEN_SCHEME"
	case "awk", "gawk":
		l = "AWK"
	case "tcl":
		l = "TCL"
	case "gdl", "gnudatalanguage":
		l = "GDL"
	case "octave":
		l = "OCTAVE"
	case "algol68", "algol-68", "algol", "a68":
		l = "ALGOL68"
	case "koka":
		l = "KOKA"
	case "pony":
		l = "PONY"
	case "shell", "bash":
		l = "BASH"
	case "posix-sh", "posix_sh", "posixsh", "sh":
		l = "POSIX_SH"
	case "zsh":
		l = "ZSH"
	case "fish":
		l = "FISH"
	case "powershell", "pwsh":
		l = "POWERSHELL"
	case "ada":
		l = "ADA"
	case "ada2012", "ada12":
		l = "ADA2012"
	case "ada2022", "ada22":
		l = "ADA2022"
	case "cobol":
		l = "COBOL"
	case "gnucobol":
		l = "GNUCOBOL"
	case "cython":
		l = "CYTHON"
	case "dart":
		l = "DART"
	case "fortran", "fortan":
		l = "FORTRAN"
	case "fortran95", "f95":
		l = "FORTRAN95"
	case "fortran2003", "fortran03", "f2003", "f03":
		l = "FORTRAN2003"
	case "fortran2008", "fortran08", "f2008", "f08":
		l = "FORTRAN2008"
	case "fortran2018", "fortran18", "f2018", "f18":
		l = "FORTRAN2018"
	case "d":
		l = "D"
	case "objective-c", "objc":
		l = "OBJC"
	case "objective-cpp", "objcpp":
		l = "OBJCPP"
	case "vhdl":
		l = "VHDL"
	case "verilog":
		l = "VERILOG"
	case "systemverilog":
		l = "SYSTEMVERILOG"
	case "crystal":
		l = "CRYSTAL"
	case "vala":
		l = "VALA"
	case "vlang":
		l = "VLANG"
	case "odin":
		l = "ODIN"
	case "c3":
		l = "C3"
	case "hare":
		l = "HARE"
	case "vb", "vbnet":
		l = "VBNET"
	case "gleam":
		l = "GLEAM"
	case "cuda-ocelot":
		l = "CUDA_OCELOT"
	case "carbon":
		l = "CARBON"
	case "graphql":
		l = "GRAPHQL"
	case "rocq":
		l = "ROCQ"
	case "coq":
		l = "COQ"
	case "lean", "lean4":
		l = "LEAN4"
	case "agda":
		l = "AGDA"
	case "dafny":
		l = "DAFNY"
	case "tla", "tlaplus":
		l = "TLA"
	case "why3", "whyml":
		l = "WHY3"
	case "isabelle":
		l = "ISABELLE"
	case "fstar", "f*", "f-star":
		l = "FSTAR"
	case "alloy":
		l = "ALLOY"
	case "acl2":
		l = "ACL2"
	case "k", "kframework", "k-framework":
		l = "KFRAMEWORK"
	case "lisp":
		l = "LISP"
	case "picolisp":
		l = "PICOLISP"
	case "idris2", "idris":
		l = "IDRIS2"
	case "sml", "standardml", "standard-ml":
		l = "SML"
	case "haxe":
		l = "HAXE"
	case "c", "c11":
		l = "C11"
	case "c89", "c90":
		l = "C89"
	case "c99":
		l = "C99"
	case "c17", "c18":
		l = "C17"
	case "c23":
		l = "C23"
	case "cpp", "c++":
		l = "CPP17"
	case "cpp98", "c++98":
		l = "CPP98"
	case "java":
		l = "JAVA11"
	case "groovy":
		l = "GROOVY"
	case "raku":
		l = "RAKU"
	case "erlang":
		l = "ERLANG"
	case "mercury":
		l = "MERCURY"
	case "prolog":
		l = "PROLOG"
	case "scala":
		l = "SCALA"
	case "f#", "fsharp":
		l = "FSHARP"
	case "vb6":
		l = "VB6"
	case "freebasic":
		l = "FREEBASIC"
	case "classic-basic":
		l = "CLASSIC_BASIC"
	case "qbasic":
		l = "QBASIC"
	case "smalltalk", "gst":
		l = "SMALLTALK"
	case "golfscript":
		l = "GOLFSCRIPT"
	case "mojo":
		l = "MOJO"
	case "deno":
		l = "DENO"
	case "elm":
		l = "ELM"
	case "kotlin-jvm", "kotlin_java", "kotlin-java", "kotlin/java", "kotlinjava":
		l = "KOTLIN_JVM"
	case "kotlin-jvm8", "kotlin-java8", "kotlin/java8":
		l = "KOTLIN_JVM8"
	case "kotlin-jvm11", "kotlin-java11", "kotlin/java11":
		l = "KOTLIN_JVM11"
	case "kotlin-jvm17", "kotlin-java17", "kotlin/java17":
		l = "KOTLIN_JVM17"
	case "kotlin-jvm21", "kotlin-java21", "kotlin/java21":
		l = "KOTLIN_JVM21"
	case "duckdb":
		l = "DUCKDB"
	case "bqn":
		l = "BQN"
	case "apl", "gnu-apl":
		l = "APL"
	case "j", "jsoftware":
		l = "J"
	case "uiua":
		l = "UIUA"
	case "janet":
		l = "JANET"
	case "coffeescript", "coffee":
		l = "COFFEESCRIPT"
	case "rescript", "res":
		l = "RESCRIPT"
	case "purescript", "purs":
		l = "PURESCRIPT"
	case "sed":
		l = "SED"
	case "bc":
		l = "BC"
	case "forth":
		l = "FORTH"
	case "gforth":
		l = "GFORTH"
	case "whitespace":
		l = "WHITESPACE"
	case "befunge", "befunge93", "befunge-93", "bf93":
		l = "BEFUNGE"
	case "bf", "brainfuck":
		l = "BF"
	case "malbolge":
		l = "MALBOLGE"
	case "lolcode", "lol":
		l = "LOLCODE"
	case "wasm", "webassembly":
		l = "WASM"
	case "assemblyscript":
		l = "ASSEMBLYSCRIPT"
	case "factor":
		l = "FACTOR"
	}
	return profiles.Resolve(l)
}

func applyRequestedVersion(profile profiles.Profile, raw string) (profiles.Profile, error) {
	version := normalizeRequestedVersion(raw)
	if version == "" {
		return profile, nil
	}

	switch profile.CompileKind {
	case "c":
		std, ok := cStandardVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported C version: %s", raw)
		}
		profile.CompileStd = std
	case "cpp":
		std, ok := cppStandardVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported C++ version: %s", raw)
		}
		profile.CompileStd = std
	case "java":
		release, ok := javaReleaseVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported Java version: %s", raw)
		}
		profile.JavaRelease = release
	case "kotlin-jvm":
		release, ok := javaReleaseVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported Kotlin/JVM target: %s", raw)
		}
		profile.JavaRelease = release
	case "rust":
		edition, ok := rustEditionVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported Rust edition: %s", raw)
		}
		profile.RustEdition = edition
	case "fortran":
		std, ok := fortranStandardVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported Fortran standard: %s", raw)
		}
		profile.CompileStd = std
	case "ada":
		std, ok := adaStandardVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported Ada standard: %s", raw)
		}
		profile.CompileStd = std
	default:
		return profile, fmt.Errorf("version is not supported for %s", profile.SourceLang)
	}
	return profile, nil
}

func normalizeRequestedVersion(raw string) string {
	version := strings.ToLower(strings.TrimSpace(raw))
	version = strings.TrimPrefix(version, "std=")
	version = strings.TrimPrefix(version, "--std=")
	version = strings.TrimPrefix(version, "release=")
	version = strings.TrimPrefix(version, "--release=")
	version = strings.TrimPrefix(version, "jvm=")
	version = strings.TrimPrefix(version, "jvmtarget=")
	version = strings.TrimPrefix(version, "jvm_target=")
	version = strings.TrimPrefix(version, "jvm-target=")
	version = strings.TrimPrefix(version, "--jvm-target=")
	version = strings.TrimPrefix(version, "--jvm_target=")
	version = strings.TrimPrefix(version, "edition=")
	version = strings.TrimPrefix(version, "--edition=")
	return strings.ReplaceAll(version, "_", "")
}

func cStandardVersion(version string) (string, bool) {
	switch version {
	case "89", "90", "ansi", "c89", "c90", "iso9899:1990":
		return "c90", true
	case "gnu89", "gnu90":
		return "gnu90", true
	case "99", "c99", "iso9899:1999":
		return "c99", true
	case "gnu99":
		return "gnu99", true
	case "11", "c11", "iso9899:2011":
		return "c11", true
	case "gnu11":
		return "gnu11", true
	case "17", "18", "c17", "c18", "iso9899:2017", "iso9899:2018":
		return "c17", true
	case "gnu17", "gnu18":
		return "gnu17", true
	case "23", "c23", "c2x", "iso9899:2024":
		return "c23", true
	case "gnu23", "gnu2x":
		return "gnu23", true
	default:
		return "", false
	}
}

func cppStandardVersion(version string) (string, bool) {
	switch version {
	case "03", "98", "cpp03", "cpp98", "c++03", "c++98":
		return "c++03", true
	case "gnu++03", "gnu++98":
		return "gnu++03", true
	case "11", "cpp11", "c++11":
		return "c++11", true
	case "gnu++11":
		return "gnu++11", true
	case "14", "cpp14", "c++14":
		return "c++14", true
	case "gnu++14":
		return "gnu++14", true
	case "17", "cpp17", "c++17":
		return "c++17", true
	case "gnu++17":
		return "gnu++17", true
	case "20", "cpp20", "c++20":
		return "c++20", true
	case "gnu++20":
		return "gnu++20", true
	case "23", "cpp23", "c++23":
		return "c++23", true
	case "gnu++23":
		return "gnu++23", true
	case "26", "cpp26", "c++26":
		return "c++26", true
	case "gnu++26":
		return "gnu++26", true
	default:
		return "", false
	}
}

func javaReleaseVersion(version string) (string, bool) {
	version = strings.TrimPrefix(version, "java")
	switch version {
	case "1.8":
		return "8", true
	case "8", "11", "15", "17", "21":
		return version, true
	default:
		return "", false
	}
}

func rustEditionVersion(version string) (string, bool) {
	version = strings.TrimPrefix(version, "rust")
	version = strings.TrimPrefix(version, "edition")
	switch version {
	case "2015", "2018", "2021", "2024":
		return version, true
	default:
		return "", false
	}
}

func fortranStandardVersion(version string) (string, bool) {
	version = strings.TrimPrefix(version, "fortran")
	switch version {
	case "95", "f95":
		return "f95", true
	case "2003", "03", "f2003", "f03":
		return "f2003", true
	case "2008", "08", "f2008", "f08":
		return "f2008", true
	case "2018", "18", "f2018", "f18":
		return "f2018", true
	default:
		return "", false
	}
}

func adaStandardVersion(version string) (string, bool) {
	version = strings.TrimPrefix(version, "ada")
	switch version {
	case "2012", "12":
		return "-gnat2012", true
	case "2022", "22":
		return "-gnat2022", true
	default:
		return "", false
	}
}
