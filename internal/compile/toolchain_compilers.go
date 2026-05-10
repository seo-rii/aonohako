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
