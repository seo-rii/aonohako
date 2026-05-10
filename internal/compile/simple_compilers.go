package compile

import (
	"context"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
)

type passThroughCompiler struct {
	exts           []string
	noSourceReason string
}

func (c passThroughCompiler) Compile(_ context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	if len(sourcePathsByExt(job.WorkDir, job.Request.Sources, c.exts...)) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: c.noSourceReason}
	}
	return passThroughArtifacts(job.WorkDir, job.Request.Sources)
}

type checkedSourcesCompiler struct {
	exts           []string
	noSourceReason string
	bin            string
	prefix         []string
	env            []string
}

func (c checkedSourcesCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	paths := sourcePathsByExt(job.WorkDir, job.Request.Sources, c.exts...)
	if len(paths) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: c.noSourceReason}
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for _, path := range paths {
		args := append(append([]string{}, c.prefix...), path)
		result := runner.Run(ctx, job.WorkDir, c.bin, args, c.env)
		fullOut.Append(result.Stdout)
		fullErr.Append(result.Stderr)
		if result.Status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(result.Status, nil, result.Reason, fullOut, fullErr)
		}
	}

	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool {
		ext := strings.ToLower(filepath.Ext(name))
		for _, allowed := range c.exts {
			if ext == strings.ToLower(allowed) {
				return true
			}
		}
		return false
	}, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}
