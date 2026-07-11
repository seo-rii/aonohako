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

type rustCompiler struct{}

func (rustCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileRust(ctx, job.WorkDir, job.Target, job.Request.Sources, job.Profile.RustEdition)
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

type goCompiler struct{}

func (goCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	return compileGo(ctx, job.WorkDir, job.Target, job.Request.Sources, runner)
}

func compileGo(ctx context.Context, workDir, target string, sources []model.Source, runner CommandRunner) model.CompileResponse {
	var goFiles []string
	var goModFiles []string
	for _, src := range sources {
		clean := filepath.Clean(src.Name)
		name := filepath.Base(clean)
		if name == "go.mod" {
			goModFiles = append(goModFiles, clean)
		}
		if strings.HasSuffix(strings.ToLower(name), ".go") {
			goFiles = append(goFiles, filepath.Join(workDir, clean))
		}
	}
	if len(goFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no go sources"}
	}
	if len(goModFiles) > 1 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "multiple go.mod files are not supported"}
	}
	goCache := filepath.Join(workDir, ".gocache")
	goModCache := filepath.Join(workDir, ".gomodcache")
	goPath := filepath.Join(workDir, ".gopath")
	for _, d := range []string{goCache, goModCache, goPath} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "mkdir failed: " + err.Error()}
		}
	}
	args := []string{"build", "-buildvcs=false", "-tags=online_judge,ONLINE_JUDGE", "-o", target}
	if len(goModFiles) == 1 {
		moduleDir := filepath.Dir(filepath.Join(workDir, goModFiles[0]))
		if moduleDir != workDir {
			args = []string{"-C", moduleDir, "build", "-buildvcs=false", "-tags=online_judge,ONLINE_JUDGE", "-o", filepath.Join(workDir, target)}
		}
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
	result := runner.Run(ctx, workDir, "go", args, env)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}

type vhdlCompiler struct{}

func (vhdlCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileVHDL(ctx, job.WorkDir, job.Request.Sources, job.Request.EntryPoint)
}

func compileVHDL(ctx context.Context, workDir string, sources []model.Source, entryPoint string) model.CompileResponse {
	vhdlFiles := sourcePathsByExt(workDir, sources, ".vhd", ".vhdl")
	if len(vhdlFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no vhdl sources"}
	}
	analyzeArgs := []string{"-a", "--std=08"}
	analyzeArgs = append(analyzeArgs, vhdlFiles...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "ghdl", analyzeArgs, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	top := strings.TrimSpace(entryPoint)
	if top == "" {
		top = "main_tb"
	}
	elabOut, elabErr, elabStatus, elabReason := runCommand(ctx, workDir, "ghdl", []string{"-e", "--std=08", top}, nil)
	stdout += elabOut
	stderr += elabErr
	if elabStatus != model.CompileStatusOK {
		return model.CompileResponse{Status: elabStatus, Stdout: stdout, Stderr: stderr, Reason: elabReason}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool {
		ext := strings.ToLower(filepath.Ext(name))
		return ext == ".vhd" || ext == ".vhdl"
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type verilogCompiler struct{}

func (verilogCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileVerilog(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileVerilog(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	verilogFiles := sourcePathsByExt(workDir, sources, ".v", ".sv")
	if len(verilogFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no verilog sources"}
	}
	if !strings.HasSuffix(strings.ToLower(target), ".vvp") {
		target += ".vvp"
	}
	args := []string{"-g2012", "-DONLINE_JUDGE=1", "-o", filepath.Join(workDir, target)}
	args = append(args, verilogFiles...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "iverilog", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type crystalCompiler struct{}

func (crystalCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileCrystal(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileCrystal(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".cr"}, "Main.cr")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no crystal sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "crystal", []string{"build", rootSource, "--release", "--no-debug", "--define", "ONLINE_JUDGE", "-o", filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type vlangCompiler struct{}

func (vlangCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileVLang(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileVLang(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".v"}, "Main.v")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no vlang sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "v", []string{"-d", "ONLINE_JUDGE", "-o", filepath.Join(workDir, target), rootSource}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type odinCompiler struct{}

func (odinCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileOdin(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileOdin(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	if len(sourcePathsByExt(workDir, sources, ".odin")) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no odin sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "odin", []string{"build", ".", "-define:ONLINE_JUDGE=true", "-out:" + filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type c3Compiler struct{}

func (c3Compiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileC3(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileC3(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".c3"}, "Main.c3")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no c3 sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "c3c", []string{"compile", "-D", "ONLINE_JUDGE", rootSource, "-o", filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type hareCompiler struct{}

func (hareCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileHare(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileHare(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".ha"}, "Main.ha")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no hare sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "hare", []string{"build", "-o", filepath.Join(workDir, target), rootSource}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}
