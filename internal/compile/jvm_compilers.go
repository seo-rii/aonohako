package compile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/config"
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

type javaCompiler struct{}

func (javaCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileJava(ctx, job.WorkDir, job.Request.Sources, job.Profile.JavaRelease)
}

func compileJava(ctx context.Context, workDir string, sources []model.Source, release string) model.CompileResponse {
	var javaPaths []string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".java") {
			javaPaths = append(javaPaths, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(javaPaths) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no java sources"}
	}
	args := []string{"--release", release, "-encoding", "UTF-8"}
	args = append(args, javaPaths...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "javac", args, javaCompileEnv(workDir, 768))
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".class") }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	if len(artifacts) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "javac produced no artifacts", Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type scalaCompiler struct{}

func (scalaCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileScala(ctx, job.WorkDir, job.Request.Sources)
}

func compileScala(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	var scalaFiles []string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".scala") {
			scalaFiles = append(scalaFiles, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(scalaFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no scala sources"}
	}
	args := []string{"-d", workDir}
	args = append(args, scalaFiles...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "scalac", args, javaCompileEnv(workDir, 768))
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool {
		return strings.HasSuffix(strings.ToLower(name), ".class")
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	if len(artifacts) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "scalac produced no artifacts", Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type kotlinJVMCompiler struct{}

func (kotlinJVMCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileKotlinJVM(ctx, job.WorkDir, job.Target, job.Request.Sources, job.Profile.JavaRelease, job.Tuning)
}

func compileKotlinJVM(ctx context.Context, workDir, target string, sources []model.Source, javaRelease string, tuning config.RuntimeTuningConfig) model.CompileResponse {
	kt := sourcePathsByExt(workDir, sources, ".kt")
	if len(kt) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no kotlin-jvm sources"}
	}
	javaPaths := sourcePathsByExt(workDir, sources, ".java")
	if !strings.HasSuffix(strings.ToLower(target), ".jar") {
		target += ".jar"
	}
	if javaRelease == "" {
		javaRelease = "8"
	}
	jvmTarget := javaRelease
	if javaRelease == "8" {
		jvmTarget = "1.8"
	}
	tuning = tuning.WithSafeDefaults()
	heapMB := max(512, tuning.KotlinNativeCompilerHeapMB)
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	args := []string{
		"-J-Xms64m",
		fmt.Sprintf("-J-Xmx%dm", heapMB),
		"-J-Xss1m",
		"-J-XX:+UseSerialGC",
		"-jvm-target",
		jvmTarget,
		"-include-runtime",
		"-d",
		filepath.Join(workDir, target),
	}
	args = append(args, kt...)
	args = append(args, javaPaths...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "kotlinc", args, javaCompileEnv(workDir, heapMB))
	fullOut.Append(stdout)
	fullErr.Append(stderr)
	if status != model.CompileStatusOK {
		return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
	}
	if len(javaPaths) > 0 {
		javaClassesDir := filepath.Join(workDir, ".aonohako-java-classes")
		if err := os.MkdirAll(javaClassesDir, 0o777|os.ModeSticky); err != nil {
			return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
		}
		javacArgs := []string{"--release", javaRelease, "-encoding", "UTF-8", "-cp", filepath.Join(workDir, target), "-d", javaClassesDir}
		javacArgs = append(javacArgs, javaPaths...)
		stdout, stderr, status, reason = runCommand(ctx, workDir, "javac", javacArgs, javaCompileEnv(workDir, heapMB))
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
		stdout, stderr, status, reason = runCommand(ctx, workDir, "jar", []string{"uf", filepath.Join(workDir, target), "-C", javaClassesDir, "."}, javaCompileEnv(workDir, heapMB))
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}

func javaCompileEnv(workDir string, xmxMB int) []string {
	if xmxMB < 256 {
		xmxMB = 256
	}
	tmp := filepath.Join(workDir, ".tmp")
	return []string{
		fmt.Sprintf("JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=%s -Xms64m -Xmx%dm -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m", tmp, xmxMB),
	}
}
