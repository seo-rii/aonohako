package compile

import (
	"context"
	"path/filepath"

	"aonohako/internal/model"
)

type pariGPCompiler struct{}

func (pariGPCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	checker := checkedSourcesCompiler{exts: []string{".gp"}, noSourceReason: "no PARI/GP sources", bin: "aonohako-pari-gp", prefix: []string{"--aonohako-check", "-f", "-q"}}
	response := checker.Compile(ctx, job)
	if response.Status != model.CompileStatusOK {
		return response
	}
	root := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".gp"}, "Main.gp")
	response.Artifacts = nil
	// The executable source comes first; immutable helpers and data follow.
	for _, primary := range []bool{true, false} {
		for _, source := range job.Request.Sources {
			if (filepath.Join(job.WorkDir, source.Name) == root) != primary {
				continue
			}
			artifacts, err := readSingleArtifact(job.WorkDir, source.Name, source.Name, "")
			if err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
			response.Artifacts = append(response.Artifacts, artifacts...)
		}
	}
	return response
}
