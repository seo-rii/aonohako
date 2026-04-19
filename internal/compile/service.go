package compile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/util"
)

const buildTimeout = 60 * time.Second

const (
	maxDecodedSourceBytes      = 16 << 20
	maxDecodedSourceTotalBytes = 48 << 20
	maxArtifactBytes           = 16 << 20
	maxArtifactTotalBytes      = 48 << 20
)

type Service struct{}

func New() *Service {
	return &Service{}
}

func (s *Service) Run(parent context.Context, req *model.CompileRequest) model.CompileResponse {
	if req == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	if len(req.Sources) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no sources"}
	}
	profile, ok := resolveProfile(req.Lang)
	if !ok {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "unsupported lang: " + req.Lang}
	}

	workDir, err := util.CreateWorkDir("aonohako-compile-*")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "mkdtemp failed: " + err.Error()}
	}
	defer os.RemoveAll(workDir)

	if err := materializeSources(workDir, req.Sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}

	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = profile.DefaultTarget
		if target == "" {
			target = "Main"
		}
	}
	target, err = validateTargetName(target)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}

	ctx, cancel := context.WithTimeout(parent, buildTimeout)
	defer cancel()

	return executeBuild(ctx, workDir, profile, target, req)
}

func resolveProfile(lang string) (profiles.Profile, bool) {
	l := strings.TrimSpace(lang)
	switch strings.ToLower(l) {
	case "python", "python3":
		l = "PYTHON3"
	case "pypy", "pypy3":
		l = "PYPY3"
	case "go", "golang":
		l = "GO"
	case "c", "c11":
		l = "C11"
	case "c99":
		l = "C99"
	case "cpp", "c++":
		l = "CPP17"
	case "java":
		l = "JAVA11"
	}
	return profiles.Resolve(l)
}

func materializeSources(root string, sources []model.Source) error {
	totalBytes := 0
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return err
		}
		data, err := util.DecodeB64(src.DataB64)
		if err != nil {
			return fmt.Errorf("decode %s: %w", clean, err)
		}
		if len(data) > maxDecodedSourceBytes {
			return fmt.Errorf("source too large: %s", clean)
		}
		totalBytes += len(data)
		if totalBytes > maxDecodedSourceTotalBytes {
			return fmt.Errorf("sources total size exceeded")
		}
		dest := filepath.Join(root, clean)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", clean, err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", clean, err)
		}
	}
	return nil
}

func validateTargetName(raw string) (string, error) {
	clean, err := util.ValidateRelativePath(raw)
	if err != nil {
		return "", err
	}
	if filepath.Base(clean) != clean || strings.ContainsAny(clean, `/\`) {
		return "", fmt.Errorf("invalid target: %q", raw)
	}
	return clean, nil
}

func executeBuild(ctx context.Context, workDir string, profile profiles.Profile, target string, req *model.CompileRequest) model.CompileResponse {
	switch profile.CompileKind {
	case "c":
		return compileNative(ctx, workDir, target, gatherByExt(req.Sources, ".c", ".h"), "gcc", []string{"-O2", "-Wall", "-lm", "--static", "-DONLINE_JUDGE", "-std=" + profile.CompileStd})
	case "cpp":
		return compileNative(ctx, workDir, target, gatherByExt(req.Sources, ".cpp", ".cc", ".cxx", ".h", ".hpp"), "g++", []string{"-O2", "-Wall", "-lm", "--static", "-pipe", "-DONLINE_JUDGE", "-std=" + profile.CompileStd})
	case "rust":
		return compileRust(ctx, workDir, target, req.Sources, profile.RustEdition)
	case "go":
		return compileGo(ctx, workDir, target, req.Sources)
	case "java":
		return compileJava(ctx, workDir, req.Sources, profile.JavaRelease)
	case "python":
		return compilePythonLike(ctx, workDir, req.Sources, "python3")
	case "pypy":
		return compilePythonLike(ctx, workDir, req.Sources, "pypy3")
	case "javascript":
		return compileScriptCheck(ctx, workDir, req.Sources, "node", []string{"--check"})
	case "ruby":
		return compileScriptCheck(ctx, workDir, req.Sources, "ruby", []string{"-c"})
	case "php":
		return compileScriptCheck(ctx, workDir, req.Sources, "php", []string{"-l"})
	case "lua":
		return compileScriptCheck(ctx, workDir, req.Sources, "luac5.4", []string{"-p"})
	case "perl":
		return compileScriptCheck(ctx, workDir, req.Sources, "perl", []string{"-c"})
	case "typescript":
		return compileTypeScript(ctx, workDir, req.Sources)
	case "kotlin":
		return compileKotlinNative(ctx, workDir, target, req.Sources)
	case "csharp":
		return compileCSharp(ctx, workDir, req.Sources)
	case "none":
		return passThroughArtifacts(workDir, req.Sources)
	default:
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "unsupported compile kind: " + profile.CompileKind}
	}
}

func gatherByExt(sources []model.Source, exts ...string) []string {
	allowed := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		allowed[strings.ToLower(ext)] = struct{}{}
	}
	var out []string
	for _, src := range sources {
		name := strings.ToLower(src.Name)
		ext := strings.ToLower(filepath.Ext(name))
		if _, ok := allowed[ext]; ok {
			if ext == ".h" || ext == ".hpp" {
				continue
			}
			out = append(out, filepath.Clean(src.Name))
		}
	}
	return out
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
	artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
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
	args := []string{"--edition", edition, "-O", "-o", target, primary}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "rustc", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
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
	args := []string{"build", "-o", target}
	if hasMod {
		args = append(args, ".")
	} else {
		args = append(args, goFiles...)
	}
	env := append(util.BaseEnv(), "GOCACHE="+goCache, "GOMODCACHE="+goModCache, "GOPATH="+goPath, "GOENV=off")
	stdout, stderr, status, reason := runCommand(ctx, workDir, "go", args, env)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
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
	args := []string{"--release", release, "-J-Xms1024m", "-J-Xmx1920m", "-J-Xss512m", "-encoding", "UTF-8"}
	args = append(args, javaPaths...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "javac", args, nil)
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

func compilePythonLike(ctx context.Context, workDir string, sources []model.Source, interpreter string) model.CompileResponse {
	stdout, stderr, status, reason := runCommand(ctx, workDir, interpreter, []string{"-m", "compileall", "-b", "."}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool {
		l := strings.ToLower(name)
		return strings.HasSuffix(l, ".py") || strings.HasSuffix(l, ".pyc")
	}, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileScriptCheck(ctx context.Context, workDir string, sources []model.Source, bin string, prefix []string) model.CompileResponse {
	var fullOut bytes.Buffer
	var fullErr bytes.Buffer
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		abs := filepath.Join(workDir, clean)
		args := append(append([]string{}, prefix...), abs)
		stdout, stderr, status, reason := runCommand(ctx, workDir, bin, args, nil)
		fullOut.WriteString(stdout)
		fullErr.WriteString(stderr)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: fullOut.String(), Stderr: fullErr.String(), Reason: reason}
		}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: fullOut.String(), Stderr: fullErr.String()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: fullOut.String(), Stderr: fullErr.String()}
}

func compileTypeScript(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	var tsFiles []string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".ts") {
			tsFiles = append(tsFiles, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(tsFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no ts sources"}
	}
	args := []string{"--module", "commonjs", "--target", "es5", "--sourceMap", "--moduleResolution", "node", "--outDir", "dist"}
	args = append(args, tsFiles...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "tsc", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(filepath.Join(workDir, "dist"), func(name string) bool {
		return strings.HasSuffix(strings.ToLower(name), ".js") || strings.HasSuffix(strings.ToLower(name), ".js.map")
	}, "dist")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileKotlinNative(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	var kt []string
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".kt") {
			kt = append(kt, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	if len(kt) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no kotlin sources"}
	}
	args := []string{"-o", target, "-opt"}
	args = append(args, kt...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "kotlinc-native", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	binaryPath := filepath.Join(workDir, target+".kexe")
	if _, err := os.Stat(binaryPath); err != nil {
		binaryPath = filepath.Join(workDir, target)
	}
	artifacts, err := readSingleArtifact(binaryPath, filepath.Base(binaryPath), "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileCSharp(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	projectDir := filepath.Join(workDir, "csproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	var hasProject bool
	var projectPath string
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		if strings.HasSuffix(strings.ToLower(clean), ".csproj") {
			hasProject = true
			if projectPath == "" {
				projectPath = filepath.Join(projectDir, clean)
			}
			break
		}
	}
	if !hasProject {
		if _, _, status, reason := runCommand(ctx, workDir, "dotnet", []string{"new", "console", "--force", "-o", projectDir}, nil); status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Reason: reason}
		}
	}
	if err := materializeSources(projectDir, sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	outDir := filepath.Join(workDir, "publish")
	publishTarget := projectDir
	if hasProject {
		publishTarget = projectPath
	}
	args := []string{"publish", publishTarget, "--configuration", "Release", "-o", outDir}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(outDir, func(name string) bool {
		l := strings.ToLower(name)
		return !strings.HasSuffix(l, ".pdb") && !strings.HasSuffix(l, ".xml")
	}, "publish")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func passThroughArtifacts(workDir string, sources []model.Source) model.CompileResponse {
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
}

func runCommand(ctx context.Context, workDir, bin string, args, env []string) (stdout, stderr, status, reason string) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir
	if len(env) > 0 {
		cmd.Env = env
	} else {
		cmd.Env = util.BaseEnv()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", "", model.CompileStatusInternal, bin + " not found"
		}
		return "", "", model.CompileStatusInternal, err.Error()
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitCh
		return outBuf.String(), errBuf.String(), model.CompileStatusTimeout, ctx.Err().Error()
	case err := <-waitCh:
		if err != nil {
			return outBuf.String(), errBuf.String(), model.CompileStatusCompileError, ""
		}
	}
	return outBuf.String(), errBuf.String(), model.CompileStatusOK, ""
}

func readSingleArtifact(path, name, mode string) ([]model.Artifact, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact failed: %w", err)
	}
	if st.Size() > maxArtifactBytes {
		return nil, fmt.Errorf("artifact too large: %s", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact failed: %w", err)
	}
	return []model.Artifact{{Name: name, DataB64: util.EncodeB64(data), Mode: mode}}, nil
}

func collectArtifacts(root string, include func(name string) bool, prefix string) ([]model.Artifact, error) {
	var artifacts []model.Artifact
	var totalBytes int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if include != nil && !include(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxArtifactBytes {
			return fmt.Errorf("artifact too large: %s", d.Name())
		}
		totalBytes += info.Size()
		if totalBytes > maxArtifactTotalBytes {
			return fmt.Errorf("artifact total size exceeded")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if prefix != "" {
			name = filepath.ToSlash(filepath.Join(prefix, rel))
		}
		artifacts = append(artifacts, model.Artifact{Name: name, DataB64: util.EncodeB64(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return artifacts, nil
}
