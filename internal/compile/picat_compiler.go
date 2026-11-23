package compile

import (
	"context"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

const picatCompileHelper = "/usr/local/lib/aonohako/picat_compile.pi"

type picatCompiler struct{}

func (picatCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	root := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".pi"}, "Main.pi")
	if root == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no Picat sources"}
	}
	for _, source := range job.Request.Sources {
		if strings.EqualFold(filepath.Ext(source.Name), ".qi") {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "Picat bytecode must not be submitted"}
		}
	}
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	stdout, stderr := newCompileOutputBuffer(), newCompileOutputBuffer()
	for _, path := range sourcePathsByExt(job.WorkDir, job.Request.Sources, ".pi") {
		result := runner.Run(ctx, job.WorkDir, "picat", []string{picatCompileHelper, path}, []string{"PICATPATH="})
		stdout.Append(result.Stdout)
		stderr.Append(result.Stderr)
		if result.Status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(result.Status, nil, result.Reason, stdout, stderr)
		}
	}
	// Publish only submitted sources/data. Picat's generated .qi files can embed
	// compile-workspace paths; execution compiles immutable source in memory.
	artifacts := make([]model.Artifact, 0, len(job.Request.Sources))
	appendSource := func(source model.Source) error {
		clean, err := util.ValidateRelativePath(source.Name)
		if err != nil {
			return err
		}
		artifact, err := readSingleArtifact(job.WorkDir, clean, filepath.ToSlash(clean), "")
		artifacts = append(artifacts, artifact...)
		return err
	}
	for _, source := range job.Request.Sources {
		if filepath.Join(job.WorkDir, filepath.Clean(source.Name)) == root {
			if err := appendSource(source); err != nil {
				return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), stdout, stderr)
			}
		}
	}
	for _, source := range job.Request.Sources {
		if filepath.Join(job.WorkDir, filepath.Clean(source.Name)) != root {
			if err := appendSource(source); err != nil {
				return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), stdout, stderr)
			}
		}
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", stdout, stderr)
}
