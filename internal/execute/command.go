package execute

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
)

func buildCommand(primaryPath, lang string, req *model.RunRequest) []string {
	return buildCommandWithRuntimeTuning(primaryPath, lang, req, config.DefaultRuntimeTuningConfig())
}

func buildCommandWithRuntimeTuning(primaryPath, lang string, req *model.RunRequest, tuning config.RuntimeTuningConfig) []string {
	tuning = tuning.WithSafeDefaults()
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
		xmx := jvmHeapMB(req.Limits.MemoryMB, tuning)
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
		schedulerArg := fmt.Sprintf("%d:%d", tuning.ErlangSchedulers, tuning.ErlangSchedulers)
		asyncThreadsArg := fmt.Sprintf("%d", tuning.ErlangAsyncThreads)
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
				"ERL_AFLAGS=" + erlangAFlags(tuning),
				erlangRuntime,
				"+S",
				schedulerArg,
				"+A",
				asyncThreadsArg,
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
		return []string{erlangRuntime, "+S", schedulerArg, "+A", asyncThreadsArg, "-noshell", "-pa", primaryPath, "-s", module, function, "-s", "init", "stop"}
	case "prolog":
		return []string{"swipl", "-q", "-f", primaryPath, "-g", "main", "-t", "halt"}
	case "lisp":
		dynamicSpaceMB := max(64, req.Limits.MemoryMB/2)
		if dynamicSpaceMB > 512 {
			dynamicSpaceMB = 512
		}
		return []string{"sbcl", "--noinform", "--dynamic-space-size", fmt.Sprintf("%d", dynamicSpaceMB), "--script", primaryPath}
	case "coq":
		return []string{"true"}
	case "groovy":
		mainClass, err := normalizeJVMMainClass(req.EntryPoint, "Main")
		if err != nil {
			return nil
		}
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
		xmx := jvmHeapMB(req.Limits.MemoryMB, tuning)
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
		mainClass, err := normalizeJVMMainClass(req.EntryPoint, "Main")
		if err != nil {
			return nil
		}
		scalaClasspath := []string{primaryPath}
		if matches, err := filepath.Glob("/usr/share/java/scala*.jar"); err == nil {
			scalaClasspath = append(scalaClasspath, matches...)
		}
		if len(scalaClasspath) == 1 {
			scalaClasspath = append(scalaClasspath, "/usr/share/java/scala-library.jar")
		}
		xmx := jvmHeapMB(req.Limits.MemoryMB, tuning)
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
			strings.Join(scalaClasspath, string(os.PathListSeparator)),
			mainClass,
		}
	case "java":
		xmx := jvmHeapMB(req.Limits.MemoryMB, tuning)
		return []string{"java", "-XX:ReservedCodeCacheSize=64m", "-XX:-UseCompressedClassPointers", fmt.Sprintf("-Xmx%dm", xmx), "-Xss1m", "-Dfile.encoding=UTF-8", "-XX:+UseSerialGC", "-DONLINE_JUDGE=1", "-jar", primaryPath}
	case "javascript":
		limitMB := max(64, req.Limits.MemoryMB)
		oldSpaceMB := max(32, (limitMB*tuning.NodeOldSpacePercent)/100)
		semiSpaceMB := limitMB / 64
		if semiSpaceMB < 1 {
			semiSpaceMB = 1
		}
		if semiSpaceMB > tuning.NodeMaxSemiSpaceMB {
			semiSpaceMB = tuning.NodeMaxSemiSpaceMB
		}
		return []string{
			"node",
			"--disable-wasm-trap-handler",
			fmt.Sprintf("--max-old-space-size=%d", oldSpaceMB),
			fmt.Sprintf("--max-semi-space-size=%d", semiSpaceMB),
			fmt.Sprintf("--stack-size=%d", tuning.NodeStackSizeKB),
			primaryPath,
		}
	case "julia":
		return []string{"julia", "--startup-file=no", "--history-file=no", "--compiled-modules=no", "--color=no", primaryPath}
	case "r":
		return []string{"/usr/lib/R/bin/exec/R", "--vanilla", "--slave", "-f", primaryPath}
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
			return []string{"env", "ERL_AFLAGS=" + erlangAFlags(tuning), "elixir", primaryPath}
		}
		return []string{
			"env",
			"EMU=beam",
			"ROOTDIR=/usr/lib/erlang",
			"BINDIR=" + ertsBin,
			"PROGNAME=erl",
			"ERL_AFLAGS=" + erlangAFlags(tuning),
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
		return []string{
			"env",
			fmt.Sprintf("GOMEMLIMIT=%dMiB", goMemoryLimitMB(req.Limits.MemoryMB, tuning)),
			fmt.Sprintf("GOGC=%d", tuning.GoGOGC),
			"/usr/bin/umjunsik-lang-go",
			primaryPath,
		}
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
		limitMB := max(16, req.Limits.MemoryMB)
		guestMemoryMB := max(1, limitMB-64)
		if limitMB <= 128 {
			guestMemoryMB = max(1, limitMB/2)
		}
		guestMemoryBytes := int64(guestMemoryMB) * 1024 * 1024
		return []string{
			"wasmtime",
			"run",
			"--dir=.",
			"-O", fmt.Sprintf("memory-reservation=%d", guestMemoryBytes),
			"-O", "memory-reservation-for-growth=0",
			"-O", fmt.Sprintf("memory-guard-size=%d", tuning.WasmtimeMemoryGuardBytes),
			"-W", fmt.Sprintf("max-memory-size=%d", guestMemoryBytes),
			"-W", "max-memories=1",
			"-W", "max-instances=1",
			"-W", "max-tables=1",
			"-W", "max-table-elements=65536",
			"-W", fmt.Sprintf("max-wasm-stack=%d", tuning.WasmtimeMaxWasmStackBytes),
			"-W", "trap-on-grow-failure=y",
			primaryPath,
		}
	case "text":
		return []string{"cat", primaryPath}
	default:
		return []string{primaryPath}
	}
}

func jvmHeapMB(memoryMB int, tuning config.RuntimeTuningConfig) int {
	tuning = tuning.WithSafeDefaults()
	return max(32, (max(0, memoryMB)*tuning.JVMHeapPercent)/100)
}

func goMemoryLimitMB(memoryMB int, tuning config.RuntimeTuningConfig) int {
	tuning = tuning.WithSafeDefaults()
	return max(16, max(0, memoryMB)-tuning.GoMemoryReserveMB)
}

func dotnetGCHeapHardLimitHex(memoryMB int, tuning config.RuntimeTuningConfig) string {
	if memoryMB <= 0 {
		return ""
	}
	tuning = tuning.WithSafeDefaults()
	heapMB := max(16, (memoryMB*tuning.DotnetGCHeapPercent)/100)
	return fmt.Sprintf("%X", uint64(heapMB)*1024*1024)
}

func erlangAFlags(tuning config.RuntimeTuningConfig) string {
	tuning = tuning.WithSafeDefaults()
	return fmt.Sprintf("+MIscs 128 +S %d:%d +A %d +MMscs 0", tuning.ErlangSchedulers, tuning.ErlangSchedulers, tuning.ErlangAsyncThreads)
}
