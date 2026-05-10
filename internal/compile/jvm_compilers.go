package compile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
)

type groovyCompiler struct{}

func (groovyCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	var groovyFiles []string
	for _, src := range job.Request.Sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".groovy") {
			groovyFiles = append(groovyFiles, filepath.Join(job.WorkDir, filepath.Clean(src.Name)))
		}
	}
	if len(groovyFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no groovy sources"}
	}
	args := []string{"-d", job.WorkDir}
	args = append(args, groovyFiles...)
	stdout, stderr, status, reason := runCommand(ctx, job.WorkDir, "groovyc", args, javaCompileEnv(job.WorkDir, 768))
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool {
		return strings.HasSuffix(strings.ToLower(name), ".class")
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	if len(artifacts) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "groovyc produced no artifacts", Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type clojureCompiler struct{}

func (clojureCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	var checked int
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for _, src := range job.Request.Sources {
		if !strings.HasSuffix(strings.ToLower(src.Name), ".clj") {
			continue
		}
		checked++
		sourcePath := filepath.Join(job.WorkDir, filepath.Clean(src.Name))
		parseExpr := fmt.Sprintf(`(require '[clojure.java.io :as io]) (with-open [r (java.io.PushbackReader. (io/reader %q))] (loop [] (let [form (read {:eof ::eof} r)] (when-not (= form ::eof) (recur)))))`, sourcePath)
		stdout, stderr, status, reason := runCommand(ctx, job.WorkDir, "clojure", []string{"-e", parseExpr}, javaCompileEnv(job.WorkDir, 768))
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
	}
	if checked == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no clojure sources"}
	}
	artifacts, err := collectArtifacts(job.WorkDir, func(name string) bool { return true }, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}
