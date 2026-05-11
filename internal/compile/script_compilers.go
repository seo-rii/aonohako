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
