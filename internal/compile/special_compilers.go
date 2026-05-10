package compile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
)

type rCompiler struct{}

func (rCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	var checked int
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for _, src := range job.Request.Sources {
		if !strings.HasSuffix(strings.ToLower(src.Name), ".r") {
			continue
		}
		checked++
		stdout, stderr, status, reason := runCommand(ctx, job.WorkDir, "/usr/lib/R/bin/exec/R", []string{"--vanilla", "--slave", "-e", "parse(file=commandArgs(TRUE)[1])", "--args", filepath.Join(job.WorkDir, filepath.Clean(src.Name))}, nil)
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
	}
	if checked == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no r sources"}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool { return true }, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}

type prologCompiler struct{}

func (prologCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	var checked int
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for _, src := range job.Request.Sources {
		if !strings.HasSuffix(strings.ToLower(src.Name), ".pl") {
			continue
		}
		checked++
		stdout, stderr, status, reason := runCommand(ctx, job.WorkDir, "swipl", []string{"-q", "-f", "none", "-g", "halt", "-t", "halt", filepath.Join(job.WorkDir, filepath.Clean(src.Name))}, nil)
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
	}
	if checked == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no prolog sources"}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool { return true }, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}

type lispCompiler struct{}

func (lispCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	var checked int
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for _, src := range job.Request.Sources {
		if !strings.HasSuffix(strings.ToLower(src.Name), ".lisp") && !strings.HasSuffix(strings.ToLower(src.Name), ".lsp") {
			continue
		}
		checked++
		clean := filepath.Clean(src.Name)
		sourcePath := filepath.Join(job.WorkDir, clean)
		outputPath := filepath.Join(job.WorkDir, ".cache", strings.TrimSuffix(filepath.Base(clean), filepath.Ext(clean))+".fasl")
		eval := fmt.Sprintf(`(handler-case (progn (compile-file %q :output-file %q) (sb-ext:exit :code 0)) (error (e) (format *error-output* "~A~%%" e) (sb-ext:exit :code 1)))`, sourcePath, outputPath)
		stdout, stderr, status, reason := runCommand(ctx, job.WorkDir, "sbcl", []string{"--noinform", "--non-interactive", "--eval", eval}, nil)
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
	}
	if checked == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no lisp sources"}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool {
		lower := strings.ToLower(name)
		return strings.HasSuffix(lower, ".lisp") || strings.HasSuffix(lower, ".lsp")
	}, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}

type nasmCompiler struct{}

func (nasmCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".asm"}, "Main.asm")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no nasm sources"}
	}
	objectPath := filepath.Join(job.WorkDir, job.Target+".o")
	stdout, stderr, status, reason := runCommand(ctx, job.WorkDir, "nasm", []string{"-felf64", "-dONLINE_JUDGE=1", rootSource, "-o", objectPath}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	linkOut, linkErr, linkStatus, linkReason := runCommand(ctx, job.WorkDir, "gcc", []string{"-nostdlib", "-static", "-no-pie", objectPath, "-o", job.Target}, nil)
	stdout += linkOut
	stderr += linkErr
	if linkStatus != model.CompileStatusOK {
		return model.CompileResponse{Status: linkStatus, Stdout: stdout, Stderr: stderr, Reason: linkReason}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type erlangCompiler struct{}

func (erlangCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	var erlangFiles []string
	for _, src := range job.Request.Sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".erl") {
			erlangFiles = append(erlangFiles, filepath.Join(job.WorkDir, filepath.Clean(src.Name)))
		}
	}
	if len(erlangFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no erlang sources"}
	}
	args := []string{"-o", job.WorkDir}
	args = append(args, erlangFiles...)
	stdout, stderr, status, reason := runCommand(ctx, job.WorkDir, "erlc", args, []string{"ERL_AFLAGS=" + erlangAFlags(job.Tuning)})
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool {
		return strings.HasSuffix(strings.ToLower(name), ".beam")
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	if len(artifacts) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "erlc produced no artifacts", Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}
