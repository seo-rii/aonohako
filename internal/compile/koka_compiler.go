package compile

import (
	"context"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/util"
)

const kokaBuildDir = ".aonohako-koka-build"

type kokaCompiler struct{}

func (kokaCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	if job.Request == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}

	rootName, normalizeRoot, err := validateKokaSources(job.Request.Sources)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	rootPath := filepath.Join(job.WorkDir, rootName)
	if normalizeRoot {
		source, readErr := os.ReadFile(rootPath)
		if readErr != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "read Koka root source: " + readErr.Error()}
		}
		rootPath = filepath.Join(job.WorkDir, "main.kk")
		file, openErr := os.OpenFile(rootPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if openErr != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "create normalized Koka root source: " + openErr.Error()}
		}
		_, writeErr := file.Write(source)
		closeErr := file.Close()
		if writeErr != nil {
			_ = os.Remove(rootPath)
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "write normalized Koka root source: " + writeErr.Error()}
		}
		if closeErr != nil {
			_ = os.Remove(rootPath)
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "close normalized Koka root source: " + closeErr.Error()}
		}
		defer os.Remove(rootPath)
	}

	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunner{}
	}
	args := []string{
		"--compile",
		"-O2",
		"--no-debug",
		"-j1",
		"-v0",
		"--console=raw",
		"--no-autoinstall",
		"--cc=/usr/bin/gcc-16",
		"--ccopts=-march=x86-64 -mtune=generic",
		"--cclinkopts=-march=x86-64 -mtune=generic",
		"--builddir=" + kokaBuildDir,
		"--output=" + outputPath(job),
		rootPath,
	}
	result := runner.Run(ctx, job.WorkDir, "koka", args, nil)
	if result.Status != model.CompileStatusOK {
		return model.CompileResponse{Status: result.Status, Stdout: result.Stdout, Stderr: result.Stderr, Reason: result.Reason}
	}
	if err := validateKokaExecutable(job.WorkDir, job.Target); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Stdout: result.Stdout, Stderr: result.Stderr, Reason: err.Error()}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, job.Target, job.Target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: result.Stdout, Stderr: result.Stderr}
}

func validateKokaSources(sources []model.Source) (rootName string, normalizeRoot bool, err error) {
	seen := make(map[string]string, len(sources))
	hasLowerRoot := false
	hasUpperRoot := false
	for _, source := range sources {
		clean, cleanErr := util.ValidateRelativePath(source.Name)
		if cleanErr != nil {
			return "", false, cleanErr
		}
		folded := strings.ToLower(filepath.ToSlash(clean))
		if previous, ok := seen[folded]; ok {
			return "", false, fmt.Errorf("case-fold source path collision: %s and %s", previous, clean)
		}
		seen[folded] = clean
		if clean == kokaBuildDir || strings.HasPrefix(clean, kokaBuildDir+string(filepath.Separator)) {
			return "", false, fmt.Errorf("Koka source path uses reserved build directory: %s", clean)
		}
		if !strings.EqualFold(filepath.Ext(clean), ".kk") {
			continue
		}
		switch clean {
		case "main.kk":
			hasLowerRoot = true
		case "Main.kk":
			hasUpperRoot = true
		default:
			if clean != strings.ToLower(clean) {
				return "", false, fmt.Errorf("Koka source paths must be lowercase: %s", clean)
			}
		}
	}
	if hasLowerRoot {
		return "main.kk", false, nil
	}
	if hasUpperRoot {
		return "Main.kk", true, nil
	}
	return "", false, fmt.Errorf("no root main.kk source")
}

func validateKokaExecutable(root, rel string) error {
	artifact, err := openArtifact(root, rel)
	if err != nil {
		return fmt.Errorf("validate Koka artifact: %w", err)
	}
	defer artifact.cleanup()
	file, err := elf.NewFile(artifact.file)
	if err != nil {
		return fmt.Errorf("validate Koka artifact ELF: %w", err)
	}
	defer file.Close()
	rpaths, err := file.DynString(elf.DT_RPATH)
	if err != nil {
		return fmt.Errorf("read Koka artifact RPATH: %w", err)
	}
	runpaths, err := file.DynString(elf.DT_RUNPATH)
	if err != nil {
		return fmt.Errorf("read Koka artifact RUNPATH: %w", err)
	}
	needed, err := file.DynString(elf.DT_NEEDED)
	if err != nil {
		return fmt.Errorf("read Koka artifact dependencies: %w", err)
	}
	return validateKokaDynamicEntries(rpaths, runpaths, needed)
}

func validateKokaDynamicEntries(rpaths, runpaths, needed []string) error {
	if len(rpaths) != 0 || len(runpaths) != 0 {
		return fmt.Errorf("Koka artifact must not contain RPATH or RUNPATH")
	}
	for _, dependency := range needed {
		if strings.ContainsAny(dependency, `/\\`) {
			return fmt.Errorf("Koka artifact dependency must be a library basename: %s", dependency)
		}
	}
	return nil
}
