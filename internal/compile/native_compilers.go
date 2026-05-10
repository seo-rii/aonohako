package compile

import (
	"context"
	"path/filepath"

	"aonohako/internal/model"
)

type nativeFlags func(CompileJob) []string

type nativeCompiler struct {
	exts  []string
	bin   string
	flags nativeFlags
}

func (c nativeCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	srcRel := gatherByExt(job.Request.Sources, c.exts...)
	if len(srcRel) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no compilable sources"}
	}

	args := make([]string, 0, len(srcRel)+8)
	for _, rel := range srcRel {
		args = append(args, filepath.Join(job.WorkDir, rel))
	}
	if c.flags != nil {
		args = append(args, c.flags(job)...)
	}
	args = append(args, "-o", job.Target)

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, c.bin, args, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}
