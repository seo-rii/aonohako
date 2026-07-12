package compile

import (
	"context"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

type noneCompiler struct{}

func (noneCompiler) Compile(_ context.Context, job CompileJob) model.CompileResponse {
	return passThroughArtifacts(job.WorkDir, job.Request.Sources)
}

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

func compileCheckedSources(ctx context.Context, workDir string, sources []model.Source, exts []string, noSourceReason, bin string, prefix, env []string) model.CompileResponse {
	return checkedSourcesCompiler{exts: exts, noSourceReason: noSourceReason, bin: bin, prefix: prefix, env: env}.Compile(ctx, CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: sources},
		Runner:  sandboxCommandRunner{},
	})
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

type scriptCheckCompiler struct {
	exts           []string
	noSourceReason string
	bin            string
	prefix         []string
}

func (c scriptCheckCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	paths := make([]string, 0, len(job.Request.Sources))
	if len(c.exts) == 0 {
		for _, src := range job.Request.Sources {
			clean, err := util.ValidateRelativePath(src.Name)
			if err != nil {
				return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
			}
			paths = append(paths, filepath.Join(job.WorkDir, clean))
		}
	} else {
		paths = sourcePathsByExt(job.WorkDir, job.Request.Sources, c.exts...)
		if len(paths) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: c.noSourceReason}
		}
	}
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for _, path := range paths {
		args := append(append([]string{}, c.prefix...), path)
		result := runner.Run(ctx, job.WorkDir, c.bin, args, nil)
		fullOut.Append(result.Stdout)
		fullErr.Append(result.Stderr)
		if result.Status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(result.Status, nil, result.Reason, fullOut, fullErr)
		}
	}

	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool { return true }, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}
