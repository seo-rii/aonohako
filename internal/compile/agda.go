package compile

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"aonohako/internal/model"
)

type agdaCompiler struct{}

func (agdaCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	if len(sourcePathsByExt(job.WorkDir, job.Request.Sources, ".agda")) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no agda sources"}
	}

	entries, err := os.ReadDir(job.WorkDir)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "agda library root preparation failed: " + err.Error()}
	}
	hasLibrary := false
	for _, entry := range entries {
		if entry.Type().IsRegular() && filepath.Ext(entry.Name()) == ".agda-lib" {
			hasLibrary = true
			break
		}
	}
	if !hasLibrary {
		// Agda searches ancestors for .agda-lib files. Stop at the submission
		// instead of trying to list its root-owned, traverse-only work root.
		// Keep submitted library definitions and their options unchanged.
		library, err := os.CreateTemp(job.WorkDir, "aonohako-*.agda-lib")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "agda library root preparation failed: " + err.Error()}
		}
		defer os.Remove(library.Name())
		_, writeErr := library.WriteString("include: .\n")
		chmodErr := library.Chmod(0o444)
		closeErr := library.Close()
		if err := errors.Join(writeErr, chmodErr, closeErr); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "agda library root preparation failed: " + err.Error()}
		}
	}

	return (checkedSourcesCompiler{exts: []string{".agda"}, noSourceReason: "no agda sources", bin: "agda"}).Compile(ctx, job)
}
