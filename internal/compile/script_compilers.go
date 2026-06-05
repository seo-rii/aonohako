package compile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/util"
)

func compilePythonLike(ctx context.Context, workDir string, sources []model.Source, interpreter string) model.CompileResponse {
	return pythonLikeCompiler{interpreter: interpreter}.Compile(ctx, CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: sources},
		Runner:  sandboxCommandRunner{},
	})
}

func compileScriptCheck(ctx context.Context, workDir string, sources []model.Source, bin string, prefix []string) model.CompileResponse {
	return scriptCheckCompiler{bin: bin, prefix: prefix}.Compile(ctx, CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: sources},
		Runner:  sandboxCommandRunner{},
	})
}

type typeScriptCompiler struct{}

func (typeScriptCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileTypeScript(ctx, job.WorkDir, job.Request.Sources)
}

func compileTypeScript(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	var tsFiles []string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".ts") {
			tsFiles = append(tsFiles, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(tsFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no ts sources"}
	}
	args := []string{"--module", "commonjs", "--target", "es2019", "--sourceMap", "--outDir", "dist"}
	args = append(args, tsFiles...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "tsc", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(filepath.Join(workDir, "dist"), func(name string) bool {
		return strings.HasSuffix(strings.ToLower(name), ".js") || strings.HasSuffix(strings.ToLower(name), ".js.map")
	}, "dist")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type coffeeScriptCompiler struct{}

func (coffeeScriptCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileCoffeeScript(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileCoffeeScript(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".coffee"}, "Main.coffee")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no coffeescript sources"}
	}
	if !strings.HasSuffix(strings.ToLower(target), ".js") {
		target += ".js"
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "coffee", []string{"--compile", "--bare", "--output", workDir, rootSource}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	compiledName := strings.TrimSuffix(filepath.Base(rootSource), filepath.Ext(rootSource)) + ".js"
	if compiledName != target {
		if err := os.Rename(filepath.Join(workDir, compiledName), filepath.Join(workDir, target)); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type elmCompiler struct{}

func (elmCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".elm"}, "Main.elm", "main.elm")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no elm sources"}
	}
	if !strings.HasSuffix(strings.ToLower(job.Target), ".js") {
		job.Target += ".js"
	}
	elmJSONPath := filepath.Join(job.WorkDir, "elm.json")
	if _, err := os.Stat(elmJSONPath); err != nil {
		if !os.IsNotExist(err) {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		defaultProject := `{
  "type": "application",
  "source-directories": ["."],
  "elm-version": "0.19.1",
  "dependencies": {
    "direct": {
      "elm/core": "1.0.5",
      "elm/json": "1.1.3"
    },
    "indirect": {}
  },
  "test-dependencies": {
    "direct": {},
    "indirect": {}
  }
}
`
		if err := os.WriteFile(elmJSONPath, []byte(defaultProject), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	compiledPath := filepath.Join(job.WorkDir, "aonohako-elm-compiled.js")
	elmHome := filepath.Join(job.WorkDir, ".home")
	if err := copyPrewarmedToolHome("/usr/local/lib/aonohako/elm-home/.elm", filepath.Join(elmHome, ".elm")); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, "elm", []string{"make", rootSource, "--output", compiledPath}, []string{"HOME=" + elmHome, "GHCRTS=-N1"})
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	compiled, err := os.ReadFile(compiledPath)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	wrapper := append([]byte(`const fs = require("fs");
const __aonohakoInput = fs.readFileSync(0, "utf8");
`), compiled...)
	wrapper = append(wrapper, []byte(`
const __aonohakoElm = typeof Elm !== "undefined" ? Elm : (typeof module !== "undefined" && module.exports ? module.exports.Elm : undefined);
if (!__aonohakoElm || !__aonohakoElm.Main || typeof __aonohakoElm.Main.init !== "function") {
  console.error("Elm.Main.init is not available");
  process.exit(1);
}
const __aonohakoApp = __aonohakoElm.Main.init({ flags: null });
const __aonohakoPorts = __aonohakoApp && __aonohakoApp.ports ? __aonohakoApp.ports : {};
if (__aonohakoPorts.stdout && typeof __aonohakoPorts.stdout.subscribe === "function") {
  __aonohakoPorts.stdout.subscribe((value) => process.stdout.write(String(value)));
}
if (__aonohakoPorts.stderr && typeof __aonohakoPorts.stderr.subscribe === "function") {
  __aonohakoPorts.stderr.subscribe((value) => process.stderr.write(String(value)));
}
if (__aonohakoPorts.exit && typeof __aonohakoPorts.exit.subscribe === "function") {
  __aonohakoPorts.exit.subscribe((code) => {
    const numericCode = Number(code);
    process.exitCode = Number.isFinite(numericCode) ? numericCode : 0;
    setImmediate(() => process.exit(process.exitCode));
  });
}
if (__aonohakoPorts.stdin && typeof __aonohakoPorts.stdin.send === "function") {
  __aonohakoPorts.stdin.send(__aonohakoInput);
}
`)...)
	if err := os.WriteFile(filepath.Join(job.WorkDir, job.Target), wrapper, 0o644); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}

func copyPrewarmedToolHome(src, dest string) error {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if d.IsDir() {
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
			if os.Geteuid() == 0 {
				if err := os.Chown(target, 65532, 65532); err != nil {
					return err
				}
			}
			return os.Chmod(target, mode|0o700)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, mode|0o600); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(target, 65532, 65532); err != nil {
				return err
			}
		}
		return nil
	})
}

type reScriptCompiler struct{}

func (reScriptCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".res"}, "Main.res", "main.res")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no rescript sources"}
	}
	if !strings.HasSuffix(strings.ToLower(job.Target), ".js") {
		job.Target += ".js"
	}
	configPath := filepath.Join(job.WorkDir, "rescript.json")
	if _, err := os.Stat(configPath); err != nil {
		if !os.IsNotExist(err) {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		defaultConfig := `{
  "name": "aonohako-rescript-submission",
  "sources": [
    { "dir": ".", "subdirs": false }
  ],
  "package-specs": {
    "module": "commonjs",
    "in-source": false
  },
  "suffix": ".js"
}
`
		if err := os.WriteFile(configPath, []byte(defaultConfig), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, "rescript", []string{"build"}, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	compiledName := strings.TrimSuffix(filepath.Base(rootSource), filepath.Ext(rootSource)) + ".js"
	outputDir := filepath.Join(job.WorkDir, "lib", "js")
	artifacts, err := readSingleArtifact(outputDir, compiledName, job.Target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	extraArtifacts, err := collectArtifacts(outputDir, func(name string) bool {
		return strings.HasSuffix(strings.ToLower(name), ".js") && name != compiledName
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	artifacts = append(artifacts, extraArtifacts...)
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}

type pureScriptCompiler struct{}

func (pureScriptCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".purs"}, "Main.purs", "main.purs", filepath.Join("src", "Main.purs"))
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no purescript sources"}
	}
	if !strings.HasSuffix(strings.ToLower(job.Target), ".js") {
		job.Target += ".js"
	}
	configPath := filepath.Join(job.WorkDir, "spago.yaml")
	if _, err := os.Stat(configPath); err != nil {
		if !os.IsNotExist(err) {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		defaultConfig := `package:
  name: aonohako-purescript-submission
  dependencies:
    - console
    - effect
    - prelude
  test:
    main: Test.Main
    dependencies: []
workspace:
  packageSet:
    registry: 77.4.1
  extraPackages: {}
`
		if err := os.WriteFile(configPath, []byte(defaultConfig), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		for _, src := range job.Request.Sources {
			clean := filepath.Clean(src.Name)
			lower := strings.ToLower(clean)
			if !strings.HasSuffix(lower, ".purs") && !strings.HasSuffix(lower, ".js") {
				continue
			}
			if clean == "src" || strings.HasPrefix(clean, "src"+string(os.PathSeparator)) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(job.WorkDir, clean))
			if err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
			dest := filepath.Join(job.WorkDir, "src", clean)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
		}
	}
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	pureScriptHome := filepath.Join(job.WorkDir, ".purescript-home")
	if err := copyPrewarmedToolHome("/usr/local/lib/aonohako/purescript-home", pureScriptHome); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	result := runner.Run(ctx, job.WorkDir, "spago", []string{"build"}, []string{
		"HOME=" + pureScriptHome,
		"XDG_CACHE_HOME=" + filepath.Join(pureScriptHome, ".cache"),
	})
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	wrapper := []byte(`require("./output/Main/index.js").main();
`)
	if err := os.WriteFile(filepath.Join(job.WorkDir, job.Target), wrapper, 0o644); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	outputArtifacts, err := collectArtifacts(filepath.Join(job.WorkDir, "output"), func(name string) bool {
		return strings.HasSuffix(strings.ToLower(name), ".js")
	}, "output")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	artifacts = append(artifacts, outputArtifacts...)
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}

type sqliteCompiler struct{}

func (sqliteCompiler) Compile(_ context.Context, job CompileJob) model.CompileResponse {
	return compileSQLite(job.WorkDir, job.Request.Sources)
}

func compileSQLite(workDir string, sources []model.Source) model.CompileResponse {
	var hasSQL bool
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".sql") {
			hasSQL = true
			break
		}
	}
	if !hasSQL {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no sqlite sources"}
	}
	artifacts, err := collectArtifacts(workDir, func(string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
}

type juliaCompiler struct{}

func (juliaCompiler) Compile(_ context.Context, job CompileJob) model.CompileResponse {
	return compileJulia(job.WorkDir, job.Request.Sources)
}

func compileJulia(workDir string, sources []model.Source) model.CompileResponse {
	var hasJulia bool
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".jl") {
			hasJulia = true
			break
		}
	}
	if !hasJulia {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no julia sources"}
	}
	artifacts, err := collectArtifacts(workDir, func(string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
}

type whitespaceCompiler struct{}

func (whitespaceCompiler) Compile(_ context.Context, job CompileJob) model.CompileResponse {
	return compileWhitespace(job.WorkDir, job.Request.Sources)
}

func compileWhitespace(workDir string, sources []model.Source) model.CompileResponse {
	var hasSource bool
	for _, src := range sources {
		if !strings.HasSuffix(strings.ToLower(src.Name), ".ws") {
			continue
		}
		hasSource = true
		data, err := util.DecodeB64(src.DataB64)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		for _, b := range data {
			if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
				return model.CompileResponse{Status: model.CompileStatusCompileError, Reason: "whitespace source contains non-whitespace characters"}
			}
		}
	}
	if !hasSource {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no whitespace sources"}
	}
	return passThroughArtifacts(workDir, sources)
}

type brainfuckCompiler struct{}

func (brainfuckCompiler) Compile(_ context.Context, job CompileJob) model.CompileResponse {
	return compileBrainfuck(job.WorkDir, job.Request.Sources)
}

func compileBrainfuck(workDir string, sources []model.Source) model.CompileResponse {
	var hasSource bool
	for _, src := range sources {
		if !strings.HasSuffix(strings.ToLower(src.Name), ".bf") {
			continue
		}
		hasSource = true
		data, err := util.DecodeB64(src.DataB64)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		depth := 0
		for _, b := range data {
			switch b {
			case '[':
				depth++
			case ']':
				depth--
				if depth < 0 {
					return model.CompileResponse{Status: model.CompileStatusCompileError, Reason: "brainfuck source has unmatched brackets"}
				}
			}
		}
		if depth != 0 {
			return model.CompileResponse{Status: model.CompileStatusCompileError, Reason: "brainfuck source has unmatched brackets"}
		}
	}
	if !hasSource {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no brainfuck sources"}
	}
	return passThroughArtifacts(workDir, sources)
}

type wasmCompiler struct{}

func (wasmCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileWasm(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileWasm(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	var watPath string
	var wasmPath string
	for _, src := range sources {
		clean := strings.ToLower(src.Name)
		switch {
		case strings.HasSuffix(clean, ".wat") && watPath == "":
			watPath = filepath.Join(workDir, filepath.Clean(src.Name))
		case strings.HasSuffix(clean, ".wasm") && wasmPath == "":
			wasmPath = filepath.Join(workDir, filepath.Clean(src.Name))
		}
	}
	if watPath == "" && wasmPath == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no wasm sources"}
	}
	if !strings.HasSuffix(strings.ToLower(target), ".wasm") {
		target += ".wasm"
	}
	targetPath := filepath.Join(workDir, target)
	if watPath != "" {
		stdout, stderr, status, reason := runCommand(ctx, workDir, "wat2wasm", []string{watPath, "-o", targetPath}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
	} else {
		stdout, stderr, status, reason := runCommand(ctx, workDir, "wasm-validate", []string{wasmPath}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		data, err := os.ReadFile(wasmPath)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		if err := os.WriteFile(targetPath, data, 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
}

type denoCompiler struct{}

func (denoCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileCheckedSources(
		ctx,
		job.WorkDir,
		job.Request.Sources,
		[]string{".ts", ".js"},
		"no deno sources",
		"deno",
		[]string{"check", fmt.Sprintf("--v8-flags=--max-old-space-size=%d", config.DenoOldSpaceMB(compileSandboxMemoryMB, job.Tuning))},
		nil,
	)
}
