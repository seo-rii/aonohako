package compile

import (
	"context"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

type scala3Compiler struct{}

func (scala3Compiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	sources := sourcePathsByExt(job.WorkDir, job.Request.Sources, ".scala")
	if len(sources) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no scala3 sources"}
	}
	// Invoke the compiler main directly: no shell launcher, dependency resolution,
	// or submitted JARs can become part of the trusted compiler's class path.
	args := []string{"-cp", filepath.Join(profiles.Scala3Home, "lib", "*"), "dotty.tools.dotc.Main", "-classpath", profiles.Scala3RuntimeClasspath, "-release", "17", "-encoding", "UTF-8", "-color:never", "-d", job.WorkDir}
	args = append(args, sources...)
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, "java", args, javaCompileEnv(job.WorkDir, 768))
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".class") }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Stdout: result.Stdout, Stderr: result.Stderr, Reason: err.Error()}
	}
	if len(artifacts) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInternal, Stdout: result.Stdout, Stderr: result.Stderr, Reason: "scala3 compiler produced no artifacts"}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}
