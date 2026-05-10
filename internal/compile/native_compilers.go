package compile

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

type nativeFlags func(CompileJob) []string

type nativeCompiler struct {
	exts  []string
	bin   string
	flags nativeFlags
}

func (c nativeCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	srcRel := gatherByExt(job.Request.Sources, c.exts...)
	if len(srcRel) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no compilable sources"}
	}

	args := make([]string, 0, len(srcRel)+8)
	for _, rel := range srcRel {
		args = append(args, filepath.Join(job.WorkDir, rel))
	}
	if c.flags != nil {
		args = append(args, c.flags(job)...)
	}
	args = append(args, "-o", job.Target)

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	result := runner.Run(ctx, job.WorkDir, c.bin, args, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}

func compileNative(ctx context.Context, workDir, target string, srcRel []string, compiler string, flags []string) model.CompileResponse {
	if len(srcRel) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no compilable sources"}
	}
	args := make([]string, 0, len(srcRel)+len(flags)+2)
	for _, rel := range srcRel {
		args = append(args, filepath.Join(workDir, rel))
	}
	args = append(args, "-o", target)
	args = append(args, flags...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, compiler, args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileRust(ctx context.Context, workDir, target string, sources []model.Source, edition string) model.CompileResponse {
	var primary string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".rs") {
			base := strings.ToLower(filepath.Base(src.Name))
			if base == "main.rs" || primary == "" {
				primary = filepath.Join(workDir, filepath.Clean(src.Name))
			}
		}
	}
	if primary == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no rust sources"}
	}
	args := []string{"--edition", edition, "-O", "--cfg", "ONLINE_JUDGE", "-o", target, primary}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "rustc", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileGo(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	var goFiles []string
	hasMod := false
	for _, src := range sources {
		name := strings.ToLower(filepath.Base(src.Name))
		if name == "go.mod" {
			hasMod = true
		}
		if strings.HasSuffix(name, ".go") {
			goFiles = append(goFiles, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(goFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no go sources"}
	}
	goCache := filepath.Join(workDir, ".gocache")
	goModCache := filepath.Join(workDir, ".gomodcache")
	goPath := filepath.Join(workDir, ".gopath")
	for _, d := range []string{goCache, goModCache, goPath} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "mkdir failed: " + err.Error()}
		}
	}
	args := []string{"build", "-tags=online_judge,ONLINE_JUDGE", "-o", target}
	if hasMod {
		args = append(args, ".")
	} else {
		args = append(args, goFiles...)
	}
	env := append(util.BaseEnv(),
		"GOCACHE="+goCache,
		"GOMODCACHE="+goModCache,
		"GOPATH="+goPath,
		"GOENV=off",
		"GOTELEMETRY=off",
		"GOTOOLCHAIN=local",
	)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "go", args, env)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}
