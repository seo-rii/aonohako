package compile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
)

type kotlinNativeCompiler struct{}

func (kotlinNativeCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileKotlinNative(ctx, job.WorkDir, job.Target, job.Request.Sources, job.Tuning)
}

func compileKotlinNative(ctx context.Context, workDir, target string, sources []model.Source, tuning config.RuntimeTuningConfig) model.CompileResponse {
	var kt []string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".kt") {
			kt = append(kt, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(kt) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no kotlin sources"}
	}
	tuning = tuning.WithSafeDefaults()
	args := []string{
		"-J-Xms64m",
		fmt.Sprintf("-J-Xmx%dm", tuning.KotlinNativeCompilerHeapMB),
		"-J-Xss1m",
		"-J-XX:+UseSerialGC",
		"-J-XX:ReservedCodeCacheSize=32m",
		"-J-XX:MaxMetaspaceSize=192m",
		"-J-XX:CompressedClassSpaceSize=64m",
		"-o",
		target,
		"-opt",
	}
	args = append(args, kt...)
	env := append(javaCompileEnv(workDir, tuning.KotlinNativeCompilerHeapMB), "KONAN_DATA_DIR=/usr/local/lib/aonohako/konan")
	stdout, stderr, status, reason := runCommand(ctx, workDir, "kotlinc-native", args, env)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	binaryPath := filepath.Join(workDir, target+".kexe")
	if _, err := os.Stat(binaryPath); err != nil {
		binaryPath = filepath.Join(workDir, target)
	}
	binaryRel, err := filepath.Rel(workDir, binaryPath)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	artifacts, err := readSingleArtifact(workDir, binaryRel, filepath.Base(binaryPath), "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type haskellCompiler struct{}

func (haskellCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileHaskell(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileHaskell(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	var hs []string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".hs") {
			hs = append(hs, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(hs) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no haskell sources"}
	}
	args := []string{"-O2", "-o", target}
	args = append(args, hs...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "ghc", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type swiftCompiler struct{}

func (swiftCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileSwift(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileSwift(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	var swiftFiles []string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".swift") {
			swiftFiles = append(swiftFiles, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(swiftFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no swift sources"}
	}
	moduleCacheDir := filepath.Join(workDir, ".cache", "swift-module-cache")
	args := []string{"-O", "-D", "ONLINE_JUDGE", "-module-cache-path", moduleCacheDir, "-o", target}
	args = append(args, swiftFiles...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "swiftc", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type cudaOcelotCompiler struct{}

func (cudaOcelotCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileCUDAOcelot(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileCUDAOcelot(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".cu"}, "Main.cu")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no cuda-ocelot sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "aonohako-cuda-ocelot-build", []string{rootSource, filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type cobolCompiler struct{}

func (cobolCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileCobol(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileCobol(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".cob", ".cbl", ".cobol"}, "Main.cob")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no cobol sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "cobc", []string{"-x", "-free", "-O2", "-o", filepath.Join(workDir, target), rootSource}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type cythonCompiler struct{}

func (cythonCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileCython(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileCython(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".pyx"}, "Main.pyx")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no cython sources"}
	}
	cPath := filepath.Join(workDir, ".aonohako-cython.c")
	outPath := filepath.Join(workDir, target)
	script := `cython3 --embed -3 -o "$2" "$1" && gcc -O2 -pipe -DONLINE_JUDGE=1 "$2" -o "$3" $(python3-config --includes --ldflags --embed)`
	stdout, stderr, status, reason := runCommand(ctx, workDir, "sh", []string{"-c", script, "aonohako-cython", rootSource, cPath, outPath}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type haxeCompiler struct{}

func (haxeCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileHaxe(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileHaxe(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	if len(sourcePathsByExt(workDir, sources, ".hx")) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no haxe sources"}
	}
	if !strings.HasSuffix(strings.ToLower(target), ".n") {
		target += ".n"
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "haxe", []string{"-D", "ONLINE_JUDGE", "-main", "Main", "-neko", filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type freeBasicCompiler struct {
	dialectArgs    []string
	noSourceReason string
}

func (c freeBasicCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileFreeBasic(ctx, job.WorkDir, job.Target, job.Request.Sources, c.dialectArgs, c.noSourceReason)
}

func compileFreeBasic(ctx context.Context, workDir, target string, sources []model.Source, dialectArgs []string, noSourceReason string) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".bas"}, "Main.bas")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: noSourceReason}
	}
	args := append([]string{}, dialectArgs...)
	args = append(args, "-d", "ONLINE_JUDGE", "-x", filepath.Join(workDir, target), rootSource)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "fbc", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

type mojoCompiler struct{}

func (mojoCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	return compileMojo(ctx, job.WorkDir, job.Target, job.Request.Sources)
}

func compileMojo(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".mojo"}, "Main.mojo")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no mojo sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "mojo", []string{"build", rootSource, "-o", filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

const defaultCarbonCoreObjectDir = "/opt/carbon/lib/carbon/core"

type carbonCompiler struct {
	coreObjectDir string
}

func (c carbonCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".carbon"}, "Main.carbon", "main.carbon")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no carbon sources"}
	}

	coreObjectDir := c.coreObjectDir
	if coreObjectDir == "" {
		coreObjectDir = defaultCarbonCoreObjectDir
	}
	coreObjects, err := carbonCoreObjectPaths(coreObjectDir)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "locate Carbon Core objects: " + err.Error()}
	}
	if len(coreObjects) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "Carbon Core objects are not installed"}
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	objectPath := outputPath(job) + ".o"
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()

	result := runner.Run(ctx, job.WorkDir, "carbon", []string{
		"compile",
		"--optimize=speed",
		"--no-debug-info",
		"--output-last-input-only",
		"--output=" + objectPath,
		rootSource,
	}, nil)
	fullOut.Append(result.Stdout)
	fullErr.Append(result.Stderr)
	if result.Status != model.CompileStatusOK {
		return compileResponseWithCapturedOutput(result.Status, nil, result.Reason, fullOut, fullErr)
	}

	linkArgs := []string{"link", "--output=" + outputPath(job), objectPath}
	linkArgs = append(linkArgs, coreObjects...)
	result = runner.Run(ctx, job.WorkDir, "carbon", linkArgs, nil)
	fullOut.Append(result.Stdout)
	fullErr.Append(result.Stderr)
	if result.Status != model.CompileStatusOK {
		return compileResponseWithCapturedOutput(result.Status, nil, result.Reason, fullOut, fullErr)
	}

	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}

func carbonCoreObjectPaths(root string) ([]string, error) {
	objects := make([]string, 0, 32)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".o") {
			objects = append(objects, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(objects)
	return objects, nil
}

type zerolangCompiler struct{}

func (zerolangCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	rootSource := selectPrimarySource(job.WorkDir, job.Request.Sources, []string{".0"}, "Main.0", "main.0")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no zerolang sources"}
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	graphPath := filepath.Join(job.WorkDir, ".cache", "zerolang-"+job.Target+".graph")
	cacheEnv := []string{"ZERO_CACHE_DIR=" + filepath.Join(job.WorkDir, ".cache", "zerolang-native")}
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()

	result := runner.Run(ctx, job.WorkDir, "zero", []string{"import", "--out", graphPath, rootSource}, cacheEnv)
	fullOut.Append(result.Stdout)
	fullErr.Append(result.Stderr)
	if result.Status != model.CompileStatusOK {
		return compileResponseWithCapturedOutput(result.Status, nil, result.Reason, fullOut, fullErr)
	}

	result = runner.Run(ctx, job.WorkDir, "zero", []string{"build", "--release", "release-fast", "--out", outputPath(job), graphPath}, cacheEnv)
	fullOut.Append(result.Stdout)
	fullErr.Append(result.Stderr)
	if result.Status != model.CompileStatusOK {
		return compileResponseWithCapturedOutput(result.Status, nil, result.Reason, fullOut, fullErr)
	}

	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}
