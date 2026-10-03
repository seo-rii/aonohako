package compile

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

// intercalCC keeps C-INTERCAL's K&R-style runtime declarations compiling
// under compilers that default to C23.
var intercalCC = []string{"CC=gcc -std=gnu17"}

type intercalCompiler struct{}

// Compile runs C-INTERCAL, which always names the executable after the source
// file, so a root source with a different basename is compiled from a
// temporary copy named after the requested target.
func (intercalCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".i"}, "Main.i")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no intercal sources"}
	}
	normalized := outputPath(job) + ".i"
	if rootSource != normalized {
		source, err := os.ReadFile(rootSource)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "read INTERCAL root source: " + err.Error()}
		}
		file, err := os.OpenFile(normalized, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "create normalized INTERCAL root source: " + err.Error()}
		}
		_, writeErr := file.Write(source)
		closeErr := file.Close()
		defer os.Remove(normalized)
		if writeErr != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "write normalized INTERCAL root source: " + writeErr.Error()}
		}
		if closeErr != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "close normalized INTERCAL root source: " + closeErr.Error()}
		}
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, "ick", []string{"-b", normalized}, intercalCC)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}

type unlambdaCompiler struct{}

func (unlambdaCompiler) Compile(_ context.Context, job CompileJob) model.CompileResponse {
	return compileUnlambda(job.WorkDir, job.Request.Sources)
}

// compileUnlambda checks that every source holds exactly one well-formed
// Unlambda expression, mirroring the bundled interpreter's parser.
func compileUnlambda(workDir string, sources []model.Source) model.CompileResponse {
	var hasSource bool
	for _, src := range sources {
		if !strings.EqualFold(filepath.Ext(src.Name), ".unl") {
			continue
		}
		hasSource = true
		data, err := util.DecodeB64(src.DataB64)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		if reason := validateUnlambda(data); reason != "" {
			return model.CompileResponse{Status: model.CompileStatusCompileError, Reason: fmt.Sprintf("unlambda source %q %s", src.Name, reason)}
		}
	}
	if !hasSource {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no unlambda sources"}
	}
	return passThroughArtifacts(workDir, sources)
}

func validateUnlambda(data []byte) string {
	// pending counts the operands still required to complete the expression.
	pending := 1
	for offset := 0; offset < len(data); offset++ {
		b := data[offset]
		switch b {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			continue
		case '#':
			for offset < len(data) && data[offset] != '\n' {
				offset++
			}
			continue
		}
		if pending == 0 {
			return fmt.Sprintf("has trailing input at byte %d", offset)
		}
		switch b {
		case '`':
			pending++
		case '.', '?':
			if offset+1 >= len(data) {
				return fmt.Sprintf("ends after %q at byte %d", b, offset)
			}
			offset++
			pending--
		case 'k', 's', 'i', 'v', 'd', 'c', 'e', 'r', 'K', 'S', 'I', 'V', 'D', 'C', 'E', 'R', '@', '|':
			pending--
		default:
			return fmt.Sprintf("has unknown character %q at byte %d", b, offset)
		}
	}
	if pending != 0 {
		return "ends before the expression is complete"
	}
	return ""
}

type pietCompiler struct{}

func (pietCompiler) Compile(_ context.Context, job CompileJob) model.CompileResponse {
	return compilePiet(job.WorkDir, job.Request.Sources)
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// compilePiet accepts the image formats npiet decodes without GD: PNG and
// netpbm PPM (P3/P6).
func compilePiet(workDir string, sources []model.Source) model.CompileResponse {
	var hasSource bool
	for _, src := range sources {
		ext := strings.ToLower(filepath.Ext(src.Name))
		if ext != ".png" && ext != ".ppm" {
			continue
		}
		hasSource = true
		data, err := util.DecodeB64(src.DataB64)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		valid := bytes.HasPrefix(data, pngSignature)
		if ext == ".ppm" {
			valid = bytes.HasPrefix(data, []byte("P3")) || bytes.HasPrefix(data, []byte("P6"))
		}
		if !valid {
			return model.CompileResponse{Status: model.CompileStatusCompileError, Reason: fmt.Sprintf("piet source %q is not a %s image", src.Name, strings.ToUpper(ext[1:]))}
		}
	}
	if !hasSource {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no piet sources"}
	}
	return passThroughArtifacts(workDir, sources)
}
