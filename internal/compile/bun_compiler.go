package compile

import (
	"context"
	"fmt"
	"path/filepath"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

var bunSourceExtensions = []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts"}

type bunCompiler struct{}

func (bunCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	paths := sourcePathsByExt(job.WorkDir, job.Request.Sources, bunSourceExtensions...)
	if len(paths) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no Bun JavaScript or TypeScript sources"}
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for index, path := range paths {
		// Bun 1.3.14 returns ENOENT for --no-bundle combined with --outdir.
		// A source-independent filename in the reserved workspace cache preserves
		// syntax-only transpilation without publishing the generated output.
		validationOutput := filepath.Join(".cache", fmt.Sprintf("aonohako-bun-check-%03d.js", index))
		args := []string{
			"--no-install",
			"--no-env-file",
			"--no-macros",
			"--config=/dev/null",
			"build",
			"--target=bun",
			"--no-bundle",
			"--outfile=" + validationOutput,
			path,
		}
		result := runner.Run(ctx, job.WorkDir, "bun", args, []string{
			"BUN_RUNTIME_TRANSPILER_CACHE_PATH=0",
			"BUN_OPTIONS=",
		})
		fullOut.Append(result.Stdout)
		fullErr.Append(result.Stderr)
		if result.Status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(result.Status, nil, result.Reason, fullOut, fullErr)
		}
	}

	artifacts := make([]model.Artifact, 0, len(job.Request.Sources))
	for _, source := range job.Request.Sources {
		clean, err := util.ValidateRelativePath(source.Name)
		if err != nil {
			return compileResponseWithCapturedOutput(model.CompileStatusInvalid, nil, err.Error(), fullOut, fullErr)
		}
		artifact, err := readSingleArtifact(job.WorkDir, clean, filepath.ToSlash(clean), "")
		if err != nil {
			return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
		}
		artifacts = append(artifacts, artifact...)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}
