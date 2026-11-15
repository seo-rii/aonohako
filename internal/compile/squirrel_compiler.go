package compile

import (
	"context"
	"path/filepath"

	"aonohako/internal/model"
)

type squirrelCompiler struct{}

func (squirrelCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	root := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".nut"}, "Main.nut")
	if root == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no Squirrel sources"}
	}
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, "aonohako-squirrel", []string{"-c", "-o", filepath.Join(job.WorkDir, job.Target), root}, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Reason: result.Reason, Stdout: result.Stdout, Stderr: result.Stderr}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	// dofile() resolves immutable submitted helpers at runtime.
	for _, source := range job.Request.Sources {
		submitted, err := readSingleArtifact(job.WorkDir, source.Name, source.Name, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		artifacts = append(artifacts, submitted...)
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}
