package compile

import (
	"context"
	"path/filepath"

	"aonohako/internal/model"
)

type executableArgs func(job CompileJob, sourcePath string) []string

type singleSourceExecutableCompiler struct {
	exts           []string
	preferredBases []string
	noSourceReason string
	bin            string
	args           executableArgs
	env            []string
}

func (c singleSourceExecutableCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, c.exts, c.preferredBases...)
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: c.noSourceReason}
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, c.bin, c.args(job, rootSource), c.env)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}

func outputPath(job CompileJob) string {
	return filepath.Join(job.WorkDir, job.Target)
}

type idris2Compiler struct{}

func (idris2Compiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".idr"}, "Main.idr", "main.idr")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no idris2 sources"}
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, "idris2", []string{"--cg", "chez", "-o", job.Target, rootSource}, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := collectArtifacts(filepath.Join(job.WorkDir, "build", "exec"), func(string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}
