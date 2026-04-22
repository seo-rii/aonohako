package execute

import (
	"fmt"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
)

func buildCommand(primaryPath, lang string, req *model.RunRequest) []string {
	switch lang {
	case "binary":
		return []string{primaryPath}
	case "aheui":
		return []string{
			"sh",
			"-c",
			"err_file=.aonohako-aheui.stderr.$$; " +
				"if aheui \"$1\" 2>\"${err_file}\"; then " +
				"cat \"${err_file}\" >&2; rm -f \"${err_file}\"; exit 0; " +
				"fi; " +
				"status=$?; " +
				"cat \"${err_file}\" >&2; " +
				"stderr_body=\"$(tr -d '\\r' < \"${err_file}\")\"; " +
				"rm -f \"${err_file}\"; " +
				"if [ -z \"${stderr_body}\" ] || [ \"${stderr_body}\" = \"[Warning:VirtualMachine] Running without rlib/jit.\" ]; then " +
				"exit 0; " +
				"fi; " +
				"case \"${stderr_body}\" in *\"Traceback (most recent call last):\"*) exit \"${status}\" ;; esac; " +
				"exit \"${status}\"",
			"sh",
			primaryPath,
		}
	case "clojure":
		return []string{"clojure", primaryPath}
	case "python":
		return []string{"python3", primaryPath}
	case "pypy":
		return []string{"pypy3", primaryPath}
	case "racket":
		return []string{"racket", primaryPath}
	case "erlang":
		module := "main"
		function := "main"
		entryPoint := strings.TrimSpace(req.EntryPoint)
		if entryPoint != "" {
			if left, right, ok := strings.Cut(entryPoint, ":"); ok {
				module = left
				function = right
			} else {
				module = entryPoint
			}
		}
		module = strings.TrimSuffix(filepath.Base(strings.TrimSpace(module)), filepath.Ext(strings.TrimSpace(module)))
		function = strings.TrimSpace(function)
		if module == "" {
			module = "main"
		}
		if function == "" {
			function = "main"
		}
		return []string{"erl", "+S", "1:1", "+A", "1", "-noshell", "-pa", primaryPath, "-s", module, function, "-s", "init", "stop"}
	case "prolog":
		return []string{"swipl", "-q", "-f", primaryPath, "-g", "main", "-t", "halt"}
	case "lisp":
		return []string{"sbcl", "--noinform", "--script", primaryPath}
	case "coq":
		return []string{"coqc", "-q", primaryPath}
	case "groovy":
		mainClass := strings.TrimSpace(req.EntryPoint)
		if mainClass == "" {
			mainClass = "Main"
		}
		mainClass = strings.ReplaceAll(mainClass, "/", ".")
		return []string{"groovy", "-cp", primaryPath, mainClass}
	case "scala":
		mainClass := strings.TrimSpace(req.EntryPoint)
		if mainClass == "" {
			mainClass = "Main"
		}
		mainClass = strings.ReplaceAll(mainClass, "/", ".")
		return []string{"scala", "-nocompdaemon", "-classpath", primaryPath, mainClass}
	case "java":
		xmx := max(32, req.Limits.MemoryMB)
		return []string{"java", "-XX:ReservedCodeCacheSize=64m", "-XX:-UseCompressedClassPointers", fmt.Sprintf("-Xmx%dm", xmx), "-Xss16m", "-Dfile.encoding=UTF-8", "-XX:+UseSerialGC", "-DONLINE_JUDGE=1", "-jar", primaryPath}
	case "javascript":
		return []string{"node", "--stack-size=65536", primaryPath}
	case "julia":
		return []string{"julia", "--startup-file=no", "--history-file=no", "--color=no", primaryPath}
	case "r":
		return []string{"Rscript", "--vanilla", primaryPath}
	case "ruby":
		return []string{"ruby", primaryPath}
	case "php":
		return []string{"php", "-d", "display_errors=stderr", primaryPath}
	case "lua":
		return []string{"lua5.4", primaryPath}
	case "perl":
		return []string{"perl", primaryPath}
	case "ocaml":
		return []string{"env", "OCAMLRUNPARAM=" + ocamlRunParam, primaryPath}
	case "elixir":
		return []string{"env", "ERL_AFLAGS=" + elixirERLAFlags, "elixir", primaryPath}
	case "sqlite":
		dbPath := filepath.Join(filepath.Dir(primaryPath), ".aonohako.sqlite3")
		return []string{"sh", "-c", "exec sqlite3 \"$0\" < \"$1\"", dbPath, primaryPath}
	case "uhmlang":
		return []string{"/usr/bin/umjunsik-lang-go", primaryPath}
	case "csharp", "fsharp":
		if strings.HasSuffix(strings.ToLower(primaryPath), ".dll") {
			return []string{"dotnet", primaryPath}
		}
		return []string{primaryPath}
	case "whitespace":
		return []string{"python3", "/usr/local/lib/aonohako/whitespace.py", primaryPath}
	case "brainfuck":
		return []string{"python3", "/usr/local/lib/aonohako/brainfuck.py", primaryPath}
	case "wasm":
		return []string{"wasmtime", "run", "--dir=.", primaryPath}
	case "text":
		return []string{"cat", primaryPath}
	default:
		return []string{primaryPath}
	}
}
