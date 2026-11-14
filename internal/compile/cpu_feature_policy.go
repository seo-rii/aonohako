package compile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/model"
)

// ValidateDisallowedCPUFeatures is shared by HTTP admission and local compilation.
// Worker architecture is checked separately so a remote frontend can forward to
// an x86-64 worker regardless of the frontend's own architecture.
func ValidateDisallowedCPUFeatures(lang string, features []cpupolicy.Feature) error {
	if err := cpupolicy.Validate(features); err != nil {
		return fmt.Errorf("invalid disallowed_cpu_features: %w", err)
	}
	if len(features) == 0 {
		return nil
	}
	profile, ok := resolveProfile(lang)
	if !ok || (profile.CompileKind != "c" && profile.CompileKind != "cpp") {
		return fmt.Errorf("disallowed_cpu_features requires a C or C++ compile request")
	}
	return nil
}

// Compile and inspect each translation unit before linking the exact inspected
// objects. Inspecting the final static executable would incorrectly include
// trusted libc/libstdc++ implementations, which themselves contain SIMD code.
func (c nativeCompiler) compileWithCPUFeaturePolicy(ctx context.Context, job CompileJob, sources []string) model.CompileResponse {
	features := job.Request.DisallowedCPUFeatures
	if err := ValidateDisallowedCPUFeatures(job.Request.Lang, features); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	if (c.bin != "gcc" && c.bin != "g++") || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "disallowed_cpu_features requires a Linux x86-64 C/C++ compiler"}
	}
	objectDir, err := os.MkdirTemp(job.WorkDir, ".cpu-policy-*")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "CPU policy workspace creation failed"}
	}
	defer os.RemoveAll(objectDir)

	var flags []string
	if c.flags != nil {
		flags = append(flags, c.flags(job)...)
	}
	flags = append(flags, cpupolicy.Flags(features)...)
	// Link-time code generation would invalidate the pre-link inspection.
	flags = append(flags, "-fno-lto")
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	response := model.CompileResponse{Status: model.CompileStatusOK}
	appendOutput := func(current, next string) string {
		remaining := compileOutputCaptureBytes + 1 - len(current)
		if remaining <= 0 {
			return current
		}
		if len(next) > remaining {
			next = next[:remaining]
		}
		return current + next
	}
	record := func(result CommandResult) {
		response.Stdout = appendOutput(response.Stdout, result.Stdout)
		response.Stderr = appendOutput(response.Stderr, result.Stderr)
		response.Status = result.Status
		response.Reason = result.Reason
	}
	objects := make([]string, 0, len(sources))
	for i, source := range sources {
		object := filepath.Join(objectDir, fmt.Sprintf("%d.o", i))
		args := append([]string{}, flags...)
		args = append(args, "-c", filepath.Join(job.WorkDir, source), "-o", object)
		result := runner.Run(ctx, job.WorkDir, c.bin, args, nil)
		record(result)
		if result.Status != model.CompileStatusOK {
			return response
		}
		inspection := runner.Run(ctx, job.WorkDir, "objdump", []string{"-d", "-w", "-z", "--no-show-raw-insn", "-M", "att", "--", object}, []string{"LC_ALL=C"})
		if inspection.Status != model.CompileStatusOK {
			// Inspector output is not compiler output and must not be exposed on
			// the successful path or allowed to replace compiler diagnostics.
			response.Stderr = appendOutput(response.Stderr, inspection.Stderr)
			response.Status = inspection.Status
			response.Reason = "CPU feature inspection failed: " + inspection.Reason
			response.ReasonCode = "cpu_feature_inspection_failed"
			return response
		}
		var inspectionErr error
		if len(inspection.Stdout) > compileOutputCaptureBytes || strings.TrimSpace(inspection.Stderr) != "" {
			inspectionErr = fmt.Errorf("incomplete or warning-producing disassembly")
		} else {
			inspectionErr = cpupolicy.CheckDisassembly(inspection.Stdout, features)
		}
		if inspectionErr != nil {
			response.Status = model.CompileStatusCompileError
			response.Reason = fmt.Sprintf("%s: %s", source, inspectionErr)
			response.ReasonCode = "cpu_feature_inspection_failed"
			var violation *cpupolicy.ViolationError
			if errors.As(inspectionErr, &violation) {
				response.ReasonCode = "disallowed_cpu_feature"
			}
			return response
		}
		objects = append(objects, object)
	}
	args := append([]string{}, objects...)
	args = append(args, flags...)
	args = append(args, "-o", job.Target)
	record(runner.Run(ctx, job.WorkDir, c.bin, args, nil))
	if response.Status != model.CompileStatusOK {
		return response
	}
	response.Artifacts, err = readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		response.Status = model.CompileStatusInternal
		response.Reason = err.Error()
	}
	return response
}
