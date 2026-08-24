package compile

import (
	"context"

	"aonohako/internal/model"
)

type ponyCompiler struct{}

func (ponyCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	if len(gatherByExt(job.Request.Sources, ".pony")) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no Pony sources"}
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	args := []string{"--cpu=generic", "--output=.", "--bin-name=" + job.Target, "."}
	result := runner.Run(ctx, job.WorkDir, "ponyc", args, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}
