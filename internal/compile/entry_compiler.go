package compile

import (
	"context"
	"os"
	"path/filepath"

	"aonohako/internal/entrypolicy"
	"aonohako/internal/model"
)

// Entry submissions are immutable JSON, never generated or submitted JavaScript.
// The image-owned checker parses every block without executing the project.
type entryCompiler struct{}

func (entryCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil || len(job.Request.Sources) != 1 || job.Request.Sources[0].Name != entrypolicy.SourceName ||
		(job.Request.EntryPoint != "" && job.Request.EntryPoint != entrypolicy.SourceName) {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "Entry requires only Main.entry.json"}
	}
	path := filepath.Join(job.WorkDir, entrypolicy.SourceName)
	info, err := os.Stat(path)
	if err != nil || info.Size() > entrypolicy.MaxSubmissionBytes {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "invalid or oversized Entry submission"}
	}
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, "node", []string{
		"--disable-wasm-trap-handler", "--max-old-space-size=128", "--stack-size=2048",
		"--disallow-code-generation-from-strings", "--no-addons", entrypolicy.RunnerPath, "--check", path,
	}, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Reason: "Entry submission validation failed", Stderr: result.Stderr}
	}
	return passThroughArtifacts(job.WorkDir, job.Request.Sources)
}
