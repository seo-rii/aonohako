package compile

import (
	"context"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

type pythonLikeCompiler struct {
	interpreter string
}

func (c pythonLikeCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	args := []string{"-I", "-S", "-m", "compileall", "-q", "-b"}
	if job.Request != nil {
		for _, source := range job.Request.Sources {
			clean, err := util.ValidateRelativePath(source.Name)
			if err != nil {
				return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
			}
			if strings.HasSuffix(strings.ToLower(clean), ".py") {
				args = append(args, clean)
			}
		}
	}
	if len(args) == 6 {
		args = append(args, ".")
	}
	result := runner.Run(ctx, job.WorkDir, c.interpreter, args, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool {
		lower := strings.ToLower(name)
		return strings.HasSuffix(lower, ".py") || strings.HasSuffix(lower, ".pyc")
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}
