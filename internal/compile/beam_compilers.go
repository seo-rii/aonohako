package compile

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/util"
)

func compileGleam(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	gleamFiles := sourcePathsByExt(workDir, sources, ".gleam")
	if len(gleamFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no gleam sources"}
	}
	if _, err := os.Stat(filepath.Join(workDir, "gleam.toml")); err != nil {
		project := "name = \"aonohako_submission\"\nversion = \"1.0.0\"\n\n[dependencies]\ngleam_stdlib = \"~> 0.44\"\n"
		if err := os.WriteFile(filepath.Join(workDir, "gleam.toml"), []byte(project), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	hasSrcMain := false
	for _, path := range gleamFiles {
		rel, err := filepath.Rel(workDir, path)
		if err == nil && strings.HasPrefix(filepath.ToSlash(rel), "src/") {
			hasSrcMain = true
			break
		}
	}
	if !hasSrcMain {
		rootSource := selectPrimarySource(workDir, sources, []string{".gleam"}, "Main.gleam", "main.gleam")
		if rootSource != "" {
			if err := os.MkdirAll(filepath.Join(workDir, "src"), 0o755); err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
			data, err := os.ReadFile(rootSource)
			if err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
			if err := os.WriteFile(filepath.Join(workDir, "src", "main.gleam"), data, 0o644); err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, "manifest.toml")); err != nil {
		if data, readErr := os.ReadFile("/usr/local/lib/aonohako/gleam-manifest.toml"); readErr == nil {
			if writeErr := os.WriteFile(filepath.Join(workDir, "manifest.toml"), data, 0o644); writeErr != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: writeErr.Error()}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, "build", "packages", "gleam_stdlib")); err != nil {
		packageRoot := "/usr/local/lib/aonohako/gleam-packages"
		if info, statErr := os.Stat(packageRoot); statErr == nil && info.IsDir() {
			targetRoot := filepath.Join(workDir, "build", "packages")
			if walkErr := filepath.WalkDir(packageRoot, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(packageRoot, path)
				if err != nil {
					return err
				}
				target := filepath.Join(targetRoot, rel)
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if entry.IsDir() {
					if err := os.MkdirAll(target, 0o777); err != nil {
						return err
					}
					return os.Chmod(target, 0o777)
				}
				if info.Mode().Type() != 0 {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if err := os.WriteFile(target, data, 0o666); err != nil {
					return err
				}
				return os.Chmod(target, 0o666)
			}); walkErr != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: walkErr.Error()}
			}
		}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "gleam", []string{"build"}, []string{
		"ERL_AFLAGS=" + erlangAFlags(config.DefaultRuntimeTuningConfig()),
		"HOME=/usr/local/lib/aonohako/gleam-home",
	})
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileElixir(ctx context.Context, workDir string, sources []model.Source, tuning config.RuntimeTuningConfig) model.CompileResponse {
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	var checked int
	tuning = tuning.WithSafeDefaults()
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		lower := strings.ToLower(clean)
		if !strings.HasSuffix(lower, ".ex") && !strings.HasSuffix(lower, ".exs") {
			continue
		}
		checked++
		stdout, stderr, status, reason := runCommand(
			ctx,
			workDir,
			"elixir",
			[]string{"-e", "Code.string_to_quoted!(File.read!(hd(System.argv())), file: hd(System.argv()))", filepath.Join(workDir, clean)},
			[]string{"ERL_AFLAGS=" + erlangAFlags(tuning)},
		)
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
	}
	if checked == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no elixir sources"}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
}

func erlangAFlags(tuning config.RuntimeTuningConfig) string {
	tuning = tuning.WithSafeDefaults()
	return fmt.Sprintf("+MIscs 128 +S %d:%d +A %d +MMscs 0", tuning.ErlangSchedulers, tuning.ErlangSchedulers, tuning.ErlangAsyncThreads)
}
