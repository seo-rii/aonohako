package compile

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/util"
)

type pythonLikeCompiler struct {
	interpreter string
}

func (c pythonLikeCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	bytecodeOnly := job.Request != nil && job.Request.PythonArtifactMode == pythonpolicy.ArtifactModeBytecodeV1
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	// Source mtimes change with each workspace; record a content hash instead
	// so identical sources produce identical bytecode artifacts.
	args := []string{"-I", "-S", "-m", "compileall", "-q", "-b", "--invalidation-mode", "checked-hash"}
	if bytecodeOnly {
		// Always rebuild; uploaded or stale sibling caches are not compiler output.
		args = append(args, "-f")
	}
	var sources []string
	expectedBytecode := map[string]bool{}
	if job.Request != nil {
		for _, source := range job.Request.Sources {
			clean, err := util.ValidateRelativePath(source.Name)
			if err != nil {
				return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
			}
			if strings.HasSuffix(strings.ToLower(clean), ".py") {
				if bytecodeOnly && !strings.HasSuffix(clean, ".py") {
					return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "python_artifact_mode bytecode-v1 requires lowercase .py source extensions"}
				}
				sources = append(sources, clean)
				expectedBytecode[clean+"c"] = true
			} else if bytecodeOnly && strings.HasSuffix(strings.ToLower(clean), ".pyc") {
				return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "python_artifact_mode bytecode-v1 does not accept submitted .pyc files"}
			}
		}
	}
	if len(sources) == 0 {
		if bytecodeOnly {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "python_artifact_mode bytecode-v1 requires a .py source"}
		}
		sources = append(sources, ".")
	}
	args = append(args, sources...)
	result := runner.Run(ctx, job.WorkDir, c.interpreter, args, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool {
		lower := strings.ToLower(name)
		return (!bytecodeOnly && strings.HasSuffix(lower, ".py")) || strings.HasSuffix(lower, ".pyc")
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	if bytecodeOnly {
		// Canonicalize the bytes that execution receives, not just a cache key.
		// Keep interpreter magic and the entire marshalled code object intact.
		for i := range artifacts {
			artifact := &artifacts[i]
			if !expectedBytecode[artifact.Name] {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "unexpected Python bytecode artifact: " + artifact.Name}
			}
			delete(expectedBytecode, artifact.Name)
			data, err := util.DecodeB64(artifact.DataB64)
			if err != nil || len(data) <= 16 || data[2] != '\r' || data[3] != '\n' || binary.LittleEndian.Uint32(data[4:8]) != 3 {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "invalid checked-hash Python bytecode artifact: " + artifact.Name}
			}
			// Timestamp mode with a fixed zero mtime and size is valid for
			// sourceless loading and contains no source-content fingerprint.
			clear(data[4:16])
			artifact.DataB64 = util.EncodeB64(data)
		}
		if len(expectedBytecode) != 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: fmt.Sprintf("missing %d Python bytecode artifacts", len(expectedBytecode))}
		}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}
