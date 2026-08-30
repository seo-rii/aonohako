package compile

import (
	"context"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
)

type assemblyScriptCompiler struct{}

func (assemblyScriptCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".ts"}, "Main.ts", "main.ts")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no assemblyscript sources"}
	}
	target := job.Target
	if !strings.EqualFold(filepath.Ext(target), ".wasm") {
		target += ".wasm"
	}
	targetPath := filepath.Join(job.WorkDir, target)

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	compileResult := runner.Run(ctx, job.WorkDir, "aonohako-assemblyscript-compile", []string{rootSource, targetPath}, nil)
	if compileResult.Status != model.CompileStatusOK {
		return model.CompileResponse{
			Status: compileResult.Status,
			Stdout: compileResult.Stdout,
			Stderr: compileResult.Stderr,
			Reason: compileResult.Reason,
		}
	}
	validateResult := runner.Run(ctx, job.WorkDir, "wasm-validate", []string{targetPath}, nil)
	stdout := compileResult.Stdout + validateResult.Stdout
	stderr := compileResult.Stderr + validateResult.Stderr
	if validateResult.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: validateResult.Status, Stdout: stdout, Stderr: stderr, Reason: validateResult.Reason}
	}

	artifacts, err := readSingleArtifact(job.WorkDir, target, target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}
