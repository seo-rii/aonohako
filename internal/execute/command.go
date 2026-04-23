package execute

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
)

func buildCommand(primaryPath, lang string, req *model.RunRequest) []string {
	ertsBin := ""
	erlangRuntime := "erl"
	if matches, err := filepath.Glob("/usr/lib/erlang/erts-*/bin/erlexec"); err == nil && len(matches) > 0 {
		erlangRuntime = matches[len(matches)-1]
		ertsBin = filepath.Dir(erlangRuntime)
	}

	switch lang {
	case "binary":
		return []string{primaryPath}
	case "aheui":
		return []string{
			"python3",
			"-c",
			"import io, sys\n" +
				"from contextlib import redirect_stderr\n" +
				"from aheui.aheui import entry_point\n" +
				"stderr = io.StringIO()\n" +
				"with redirect_stderr(stderr):\n" +
				"    code = entry_point(['aheui', sys.argv[1]])\n" +
				"body = stderr.getvalue().replace('\\r', '')\n" +
				"sys.stderr.write(body)\n" +
				"trimmed = '\\n'.join(line for line in body.splitlines() if line.strip())\n" +
				"if code == 0 or trimmed in ('', '[Warning:VirtualMachine] Running without rlib/jit.'):\n" +
				"    raise SystemExit(0)\n" +
				"raise SystemExit(code)\n",
			primaryPath,
		}
	case "clojure":
		xmx := max(32, req.Limits.MemoryMB/2)
		return []string{
			"java",
			fmt.Sprintf("-Xmx%dm", xmx),
			"-Xss1m",
			"-XX:+UseSerialGC",
			"-XX:ReservedCodeCacheSize=32m",
			"-XX:MaxMetaspaceSize=192m",
			"-XX:CompressedClassSpaceSize=64m",
			"-Dfile.encoding=UTF-8",
			"-cp",
			"/usr/share/java/clojure-1.12.jar",
			"clojure.main",
			primaryPath,
		}
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
		if ertsBin != "" {
			return []string{
				"env",
				"EMU=beam",
				"ROOTDIR=/usr/lib/erlang",
				"BINDIR=" + ertsBin,
				"PROGNAME=erl",
				"ERL_AFLAGS=" + elixirERLAFlags,
				erlangRuntime,
				"+S",
				"1:1",
				"+A",
				"1",
				"-noshell",
				"-pa",
				primaryPath,
				"-s",
				module,
				function,
				"-s",
				"init",
				"stop",
			}
		}
		return []string{erlangRuntime, "+S", "1:1", "+A", "1", "-noshell", "-pa", primaryPath, "-s", module, function, "-s", "init", "stop"}
	case "prolog":
		return []string{"swipl", "-q", "-f", primaryPath, "-g", "main", "-t", "halt"}
	case "lisp":
		return []string{"sbcl", "--noinform", "--script", primaryPath}
	case "coq":
		return []string{"true"}
	case "groovy":
		mainClass := strings.TrimSpace(req.EntryPoint)
		if mainClass == "" {
			mainClass = "Main"
		}
		mainClass = strings.ReplaceAll(mainClass, "/", ".")
		groovyClasspath := []string{primaryPath}
		for _, pattern := range []string{
			"/usr/share/groovy/embeddable/groovy-all*.jar",
			"/usr/share/java/groovy-all*.jar",
			"/usr/share/java/groovy*.jar",
		} {
			if matches, err := filepath.Glob(pattern); err == nil {
				groovyClasspath = append(groovyClasspath, matches...)
			}
		}
		if len(groovyClasspath) == 1 {
			groovyClasspath = append(groovyClasspath,
				"/usr/share/groovy/embeddable/groovy-all.jar",
				"/usr/share/java/groovy-all.jar",
				"/usr/share/java/groovy.jar",
			)
		}
		xmx := max(32, req.Limits.MemoryMB/2)
		return []string{
			"java",
			fmt.Sprintf("-Xmx%dm", xmx),
			"-Xss1m",
			"-XX:+UseSerialGC",
			"-XX:ReservedCodeCacheSize=32m",
			"-XX:MaxMetaspaceSize=192m",
			"-XX:CompressedClassSpaceSize=64m",
			"-Dfile.encoding=UTF-8",
			"-cp",
			strings.Join(groovyClasspath, string(os.PathListSeparator)),
			mainClass,
		}
	case "scala":
		mainClass := strings.TrimSpace(req.EntryPoint)
		if mainClass == "" {
			mainClass = "Main"
		}
		mainClass = strings.ReplaceAll(mainClass, "/", ".")
		return []string{"scala", "-nocompdaemon", "-classpath", primaryPath, mainClass}
	case "java":
		xmx := max(32, req.Limits.MemoryMB/2)
		return []string{"java", "-XX:ReservedCodeCacheSize=64m", "-XX:-UseCompressedClassPointers", fmt.Sprintf("-Xmx%dm", xmx), "-Xss1m", "-Dfile.encoding=UTF-8", "-XX:+UseSerialGC", "-DONLINE_JUDGE=1", "-jar", primaryPath}
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
		elixirRoot := "/usr/lib/elixir/lib"
		if info, err := os.Stat(filepath.Join(elixirRoot, "elixir", "ebin")); err != nil || !info.IsDir() || ertsBin == "" {
			return []string{"env", "ERL_AFLAGS=" + elixirERLAFlags, "elixir", primaryPath}
		}
		return []string{
			"env",
			"EMU=beam",
			"ROOTDIR=/usr/lib/erlang",
			"BINDIR=" + ertsBin,
			"PROGNAME=erl",
			"ERL_AFLAGS=" + elixirERLAFlags,
			erlangRuntime,
			"-noshell",
			"-elixir_root",
			elixirRoot,
			"-pa",
			filepath.Join(elixirRoot, "elixir", "ebin"),
			"-s",
			"elixir",
			"start_cli",
			"-extra",
			primaryPath,
		}
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
