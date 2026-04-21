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
	"sort"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/security"
	"aonohako/internal/util"
)

const buildTimeout = 60 * time.Second

const (
	maxDecodedSourceBytes      = 16 << 20
	maxDecodedSourceTotalBytes = 48 << 20
	maxArtifactBytes           = 16 << 20
	maxArtifactTotalBytes      = 48 << 20
	elixirERLAFlags            = "+MIscs 128 +S 1:1 +A 1"
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
	for _, dir := range security.WorkspaceScopedDirs(workDir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "workspace prep failed: " + err.Error()}
		}
	}

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
	case "aheui":
		l = "AHEUI"
	case "python", "python3":
		l = "PYTHON3"
	case "pypy", "pypy3":
		l = "PYPY3"
	case "r":
		l = "R"
	case "go", "golang":
		l = "GO"
	case "zig":
		l = "ZIG"
	case "pascal", "freepascal", "fpc":
		l = "PASCAL"
	case "nim":
		l = "NIM"
	case "clojure":
		l = "CLOJURE"
	case "racket", "scheme":
		l = "RACKET"
	case "ada":
		l = "ADA"
	case "dart":
		l = "DART"
	case "fortran", "fortan":
		l = "FORTRAN"
	case "d":
		l = "D"
	case "coq":
		l = "COQ"
	case "lisp":
		l = "LISP"
	case "c", "c11":
		l = "C11"
	case "c99":
		l = "C99"
	case "cpp", "c++":
		l = "CPP17"
	case "java":
		l = "JAVA11"
	case "groovy":
		l = "GROOVY"
	case "erlang":
		l = "ERLANG"
	case "prolog":
		l = "PROLOG"
	case "scala":
		l = "SCALA"
	case "f#", "fsharp":
		l = "FSHARP"
	case "whitespace":
		l = "WHITESPACE"
	case "bf", "brainfuck":
		l = "BF"
	case "wasm", "webassembly":
		l = "WASM"
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
	case "pascal":
		var rootSource string
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".pas") {
				continue
			}
			clean := filepath.Clean(src.Name)
			if rootSource == "" || strings.EqualFold(filepath.Base(clean), "Main.pas") {
				rootSource = filepath.Join(workDir, clean)
			}
			if strings.EqualFold(filepath.Base(clean), "Main.pas") {
				break
			}
		}
		if rootSource == "" {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no pascal sources"}
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "fpc", []string{"-O2", "-Xs", "-o" + filepath.Join(workDir, target), rootSource}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	case "nim":
		var rootSource string
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".nim") {
				continue
			}
			clean := filepath.Clean(src.Name)
			if rootSource == "" || strings.EqualFold(filepath.Base(clean), "Main.nim") {
				rootSource = filepath.Join(workDir, clean)
			}
			if strings.EqualFold(filepath.Base(clean), "Main.nim") {
				break
			}
		}
		if rootSource == "" {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no nim sources"}
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "nim", []string{"c", "-d:release", "--opt:speed", "--out:" + filepath.Join(workDir, target), rootSource}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	case "rust":
		return compileRust(ctx, workDir, target, req.Sources, profile.RustEdition)
	case "go":
		return compileGo(ctx, workDir, target, req.Sources)
	case "zig":
		var rootSource string
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".zig") {
				continue
			}
			clean := filepath.Clean(src.Name)
			if rootSource == "" || strings.EqualFold(filepath.Base(clean), "Main.zig") {
				rootSource = filepath.Join(workDir, clean)
			}
			if strings.EqualFold(filepath.Base(clean), "Main.zig") {
				break
			}
		}
		if rootSource == "" {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no zig sources"}
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "zig", []string{"build-exe", rootSource, "-O", "ReleaseSafe", "-femit-bin=" + filepath.Join(workDir, target)}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	case "java":
		return compileJava(ctx, workDir, req.Sources, profile.JavaRelease)
	case "groovy":
		var groovyFiles []string
		for _, src := range req.Sources {
			if strings.HasSuffix(strings.ToLower(src.Name), ".groovy") {
				groovyFiles = append(groovyFiles, filepath.Join(workDir, filepath.Clean(src.Name)))
			}
		}
		if len(groovyFiles) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no groovy sources"}
		}
		args := []string{"-d", workDir}
		args = append(args, groovyFiles...)
		stdout, stderr, status, reason := runCommand(ctx, workDir, "groovyc", args, nil)
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
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "groovyc produced no artifacts", Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	case "clojure":
		var checked int
		var fullOut bytes.Buffer
		var fullErr bytes.Buffer
		parseExpr := `(require '[clojure.java.io :as io]) (with-open [r (java.io.PushbackReader. (io/reader (first *command-line-args*)))] (loop [] (let [form (read {:eof ::eof} r)] (when-not (= form ::eof) (recur)))))`
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".clj") {
				continue
			}
			checked++
			stdout, stderr, status, reason := runCommand(ctx, workDir, "clojure", []string{"-e", parseExpr, filepath.Join(workDir, filepath.Clean(src.Name))}, nil)
			fullOut.WriteString(stdout)
			fullErr.WriteString(stderr)
			if status != model.CompileStatusOK {
				return model.CompileResponse{Status: status, Stdout: fullOut.String(), Stderr: fullErr.String(), Reason: reason}
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no clojure sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: fullOut.String(), Stderr: fullErr.String()}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: fullOut.String(), Stderr: fullErr.String()}
	case "racket":
		return compileScriptCheck(ctx, workDir, req.Sources, "raco", []string{"make"})
	case "python":
		return compilePythonLike(ctx, workDir, req.Sources, "python3")
	case "pypy":
		return compilePythonLike(ctx, workDir, req.Sources, "pypy3")
	case "r":
		var checked int
		var fullOut bytes.Buffer
		var fullErr bytes.Buffer
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".r") {
				continue
			}
			checked++
			stdout, stderr, status, reason := runCommand(ctx, workDir, "Rscript", []string{"--vanilla", "-e", "parse(file=commandArgs(TRUE)[1])", filepath.Join(workDir, filepath.Clean(src.Name))}, nil)
			fullOut.WriteString(stdout)
			fullErr.WriteString(stderr)
			if status != model.CompileStatusOK {
				return model.CompileResponse{Status: status, Stdout: fullOut.String(), Stderr: fullErr.String(), Reason: reason}
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no r sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: fullOut.String(), Stderr: fullErr.String()}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: fullOut.String(), Stderr: fullErr.String()}
	case "prolog":
		var checked int
		var fullOut bytes.Buffer
		var fullErr bytes.Buffer
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".pl") {
				continue
			}
			checked++
			stdout, stderr, status, reason := runCommand(ctx, workDir, "swipl", []string{"-q", "-f", "none", "-g", "halt", "-t", "halt", filepath.Join(workDir, filepath.Clean(src.Name))}, nil)
			fullOut.WriteString(stdout)
			fullErr.WriteString(stderr)
			if status != model.CompileStatusOK {
				return model.CompileResponse{Status: status, Stdout: fullOut.String(), Stderr: fullErr.String(), Reason: reason}
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no prolog sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: fullOut.String(), Stderr: fullErr.String()}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: fullOut.String(), Stderr: fullErr.String()}
	case "lisp":
		var checked int
		var fullOut bytes.Buffer
		var fullErr bytes.Buffer
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".lisp") && !strings.HasSuffix(strings.ToLower(src.Name), ".lsp") {
				continue
			}
			checked++
			stdout, stderr, status, reason := runCommand(ctx, workDir, "sbcl", []string{"--noinform", "--non-interactive", "--load", filepath.Join(workDir, filepath.Clean(src.Name)), "--eval", "(quit)"}, nil)
			fullOut.WriteString(stdout)
			fullErr.WriteString(stderr)
			if status != model.CompileStatusOK {
				return model.CompileResponse{Status: status, Stdout: fullOut.String(), Stderr: fullErr.String(), Reason: reason}
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no lisp sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: fullOut.String(), Stderr: fullErr.String()}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: fullOut.String(), Stderr: fullErr.String()}
	case "coq":
		var checked int
		var fullOut bytes.Buffer
		var fullErr bytes.Buffer
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".v") {
				continue
			}
			checked++
			stdout, stderr, status, reason := runCommand(ctx, workDir, "coqc", []string{"-q", filepath.Join(workDir, filepath.Clean(src.Name))}, nil)
			fullOut.WriteString(stdout)
			fullErr.WriteString(stderr)
			if status != model.CompileStatusOK {
				return model.CompileResponse{Status: status, Stdout: fullOut.String(), Stderr: fullErr.String(), Reason: reason}
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no coq sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".v") }, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: fullOut.String(), Stderr: fullErr.String()}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: fullOut.String(), Stderr: fullErr.String()}
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
	case "fortran":
		return compileNative(ctx, workDir, target, gatherByExt(req.Sources, ".f", ".for", ".f90", ".f95", ".f03", ".f08"), "gfortran", []string{"-O2", "-pipe"})
	case "ada":
		var rootSource string
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".adb") {
				continue
			}
			clean := filepath.Clean(src.Name)
			if rootSource == "" || strings.EqualFold(filepath.Base(clean), "Main.adb") {
				rootSource = filepath.Join(workDir, clean)
			}
			if strings.EqualFold(filepath.Base(clean), "Main.adb") {
				break
			}
		}
		if rootSource == "" {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no ada sources"}
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "gnatmake", []string{"-O2", "-o", filepath.Join(workDir, target), rootSource}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	case "d":
		var rootSource string
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".d") {
				continue
			}
			clean := filepath.Clean(src.Name)
			if rootSource == "" || strings.EqualFold(filepath.Base(clean), "Main.d") {
				rootSource = filepath.Join(workDir, clean)
			}
			if strings.EqualFold(filepath.Base(clean), "Main.d") {
				break
			}
		}
		if rootSource == "" {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no d sources"}
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "ldc2", []string{rootSource, "-O3", "-release", "-of=" + filepath.Join(workDir, target)}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	case "haskell":
		return compileHaskell(ctx, workDir, target, req.Sources)
	case "swift":
		return compileSwift(ctx, workDir, target, req.Sources)
	case "sqlite":
		return compileSQLite(workDir, req.Sources)
	case "julia":
		return compileJulia(workDir, req.Sources)
	case "erlang":
		var erlangFiles []string
		for _, src := range req.Sources {
			if strings.HasSuffix(strings.ToLower(src.Name), ".erl") {
				erlangFiles = append(erlangFiles, filepath.Join(workDir, filepath.Clean(src.Name)))
			}
		}
		if len(erlangFiles) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no erlang sources"}
		}
		args := []string{"-o", workDir}
		args = append(args, erlangFiles...)
		stdout, stderr, status, reason := runCommand(ctx, workDir, "erlc", args, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool {
			return strings.HasSuffix(strings.ToLower(name), ".beam")
		}, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		if len(artifacts) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "erlc produced no artifacts", Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	case "scala":
		return compileScala(ctx, workDir, req.Sources)
	case "fsharp":
		return compileFSharp(ctx, workDir, req.Sources)
	case "whitespace":
		return compileWhitespace(workDir, req.Sources)
	case "brainfuck":
		return compileBrainfuck(workDir, req.Sources)
	case "wasm":
		return compileWasm(ctx, workDir, target, req.Sources)
	case "ocaml":
		return compileOCaml(ctx, workDir, target, req.Sources)
	case "elixir":
		return compileElixir(ctx, workDir, req.Sources)
	case "csharp":
		return compileCSharp(ctx, workDir, req.Sources)
	case "dart":
		var rootSource string
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".dart") {
				continue
			}
			clean := filepath.Clean(src.Name)
			if rootSource == "" || strings.EqualFold(filepath.Base(clean), "Main.dart") {
				rootSource = filepath.Join(workDir, clean)
			}
			if strings.EqualFold(filepath.Base(clean), "Main.dart") {
				break
			}
		}
		if rootSource == "" {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no dart sources"}
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "dart", []string{"compile", "exe", rootSource, "-o", filepath.Join(workDir, target)}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
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
	artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
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
	moduleCacheDir := filepath.Join(workDir, ".swift-module-cache")
	if err := os.MkdirAll(moduleCacheDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	args := []string{"-O", "-module-cache-path", moduleCacheDir, "-o", target}
	args = append(args, swiftFiles...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "swiftc", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileSQLite(workDir string, sources []model.Source) model.CompileResponse {
	var hasSQL bool
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".sql") {
			hasSQL = true
			break
		}
	}
	if !hasSQL {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no sqlite sources"}
	}
	artifacts, err := collectArtifacts(workDir, func(string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
}

func compileJulia(workDir string, sources []model.Source) model.CompileResponse {
	var hasJulia bool
	for _, src := range sources {
		if strings.HasSuffix(strings.ToLower(src.Name), ".jl") {
			hasJulia = true
			break
		}
	}
	if !hasJulia {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no julia sources"}
	}
	artifacts, err := collectArtifacts(workDir, func(string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
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
	stdout, stderr, status, reason := runCommand(ctx, workDir, "scalac", args, nil)
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

func compileFSharp(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	projectDir := filepath.Join(workDir, "fsproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	var projectPath string
	var fsFiles []string
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, ".fsproj") && projectPath == "" {
			projectPath = filepath.Join(projectDir, clean)
		}
		if strings.HasSuffix(lower, ".fs") {
			fsFiles = append(fsFiles, clean)
		}
	}
	if err := materializeSources(projectDir, sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	if projectPath == "" {
		if len(fsFiles) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no fsharp sources"}
		}
		var builder strings.Builder
		builder.WriteString("<Project Sdk=\"Microsoft.NET.Sdk\">\n  <PropertyGroup>\n    <OutputType>Exe</OutputType>\n    <TargetFramework>net8.0</TargetFramework>\n    <LangVersion>latest</LangVersion>\n  </PropertyGroup>\n  <ItemGroup>\n")
		for _, file := range fsFiles {
			builder.WriteString("    <Compile Include=\"")
			builder.WriteString(file)
			builder.WriteString("\" />\n")
		}
		builder.WriteString("  </ItemGroup>\n</Project>\n")
		projectPath = filepath.Join(projectDir, "App.fsproj")
		if err := os.WriteFile(projectPath, []byte(builder.String()), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	outDir := filepath.Join(workDir, "publish")
	args := []string{"publish", projectPath, "--configuration", "Release", "-o", outDir, "-p:UseAppHost=false"}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, dotnetBuildEnv())
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectDotnetPublishArtifacts(outDir, strings.TrimSuffix(filepath.Base(projectPath), filepath.Ext(projectPath)))
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileWhitespace(workDir string, sources []model.Source) model.CompileResponse {
	var hasSource bool
	for _, src := range sources {
		if !strings.HasSuffix(strings.ToLower(src.Name), ".ws") {
			continue
		}
		hasSource = true
		data, err := util.DecodeB64(src.DataB64)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		for _, b := range data {
			if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
				return model.CompileResponse{Status: model.CompileStatusCompileError, Reason: "whitespace source contains non-whitespace characters"}
			}
		}
	}
	if !hasSource {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no whitespace sources"}
	}
	return passThroughArtifacts(workDir, sources)
}

func compileBrainfuck(workDir string, sources []model.Source) model.CompileResponse {
	var hasSource bool
	for _, src := range sources {
		if !strings.HasSuffix(strings.ToLower(src.Name), ".bf") {
			continue
		}
		hasSource = true
		data, err := util.DecodeB64(src.DataB64)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		depth := 0
		for _, b := range data {
			switch b {
			case '[':
				depth++
			case ']':
				depth--
				if depth < 0 {
					return model.CompileResponse{Status: model.CompileStatusCompileError, Reason: "brainfuck source has unmatched brackets"}
				}
			}
		}
		if depth != 0 {
			return model.CompileResponse{Status: model.CompileStatusCompileError, Reason: "brainfuck source has unmatched brackets"}
		}
	}
	if !hasSource {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no brainfuck sources"}
	}
	return passThroughArtifacts(workDir, sources)
}

func compileWasm(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	var watPath string
	var wasmPath string
	for _, src := range sources {
		clean := strings.ToLower(src.Name)
		switch {
		case strings.HasSuffix(clean, ".wat") && watPath == "":
			watPath = filepath.Join(workDir, filepath.Clean(src.Name))
		case strings.HasSuffix(clean, ".wasm") && wasmPath == "":
			wasmPath = filepath.Join(workDir, filepath.Clean(src.Name))
		}
	}
	if watPath == "" && wasmPath == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no wasm sources"}
	}
	if !strings.HasSuffix(strings.ToLower(target), ".wasm") {
		target += ".wasm"
	}
	targetPath := filepath.Join(workDir, target)
	if watPath != "" {
		stdout, stderr, status, reason := runCommand(ctx, workDir, "wat2wasm", []string{watPath, "-o", targetPath}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
	} else {
		stdout, stderr, status, reason := runCommand(ctx, workDir, "wasm-validate", []string{wasmPath}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		data, err := os.ReadFile(wasmPath)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		if err := os.WriteFile(targetPath, data, 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	artifacts, err := readSingleArtifact(targetPath, target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
}

func compileOCaml(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	ordered := make([]string, 0, len(sources))
	hasML := false
	for _, src := range sources {
		name := strings.ToLower(src.Name)
		if strings.HasSuffix(name, ".ml") || strings.HasSuffix(name, ".mli") {
			ordered = append(ordered, filepath.Clean(src.Name))
		}
		if strings.HasSuffix(name, ".ml") {
			hasML = true
		}
	}
	if !hasML {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no ocaml sources"}
	}
	sort.Slice(ordered, func(i, j int) bool {
		left := filepath.Base(ordered[i])
		right := filepath.Base(ordered[j])
		leftIsMain := strings.EqualFold(left, "Main.ml")
		rightIsMain := strings.EqualFold(right, "Main.ml")
		if leftIsMain != rightIsMain {
			return !leftIsMain
		}
		leftIsInterface := strings.HasSuffix(strings.ToLower(left), ".mli")
		rightIsInterface := strings.HasSuffix(strings.ToLower(right), ".mli")
		if leftIsInterface != rightIsInterface {
			return leftIsInterface
		}
		return left < right
	})
	args := []string{"-o", target}
	for _, rel := range ordered {
		args = append(args, filepath.Join(workDir, rel))
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "ocamlopt", args, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(filepath.Join(workDir, target), target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileElixir(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	var fullOut bytes.Buffer
	var fullErr bytes.Buffer
	var checked int
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
			[]string{"ERL_AFLAGS=" + elixirERLAFlags},
		)
		fullOut.WriteString(stdout)
		fullErr.WriteString(stderr)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: fullOut.String(), Stderr: fullErr.String(), Reason: reason}
		}
	}
	if checked == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no elixir sources"}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: fullOut.String(), Stderr: fullErr.String()}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: fullOut.String(), Stderr: fullErr.String()}
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
		if _, _, status, reason := runCommand(ctx, workDir, "dotnet", []string{"new", "console", "--force", "-o", projectDir}, dotnetBuildEnv()); status != model.CompileStatusOK {
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
	args := []string{"publish", publishTarget, "--configuration", "Release", "-o", outDir, "-p:UseAppHost=false"}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, dotnetBuildEnv())
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	assemblyName := filepath.Base(projectDir)
	if hasProject {
		assemblyName = strings.TrimSuffix(filepath.Base(projectPath), filepath.Ext(projectPath))
	}
	artifacts, err := collectDotnetPublishArtifacts(outDir, assemblyName)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func dotnetBuildEnv() []string {
	return []string{
		"HOME=/tmp/csharp-home",
		"DOTNET_CLI_HOME=/tmp/csharp-home",
		"NUGET_PACKAGES=/tmp/csharp-packages",
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1",
		"DOTNET_GENERATE_ASPNET_CERTIFICATE=false",
		"DOTNET_NOLOGO=1",
		"MSBuildEnableWorkloadResolver=false",
	}
}

func collectDotnetPublishArtifacts(root, assemblyName string) ([]model.Artifact, error) {
	artifacts, err := collectArtifacts(root, func(name string) bool {
		l := strings.ToLower(name)
		return !strings.HasSuffix(l, ".pdb") && !strings.HasSuffix(l, ".xml")
	}, "publish")
	if err != nil {
		return nil, err
	}
	if assemblyName == "" {
		return artifacts, nil
	}
	mainDLL := "publish/" + assemblyName + ".dll"
	for i, artifact := range artifacts {
		if artifact.Name == mainDLL {
			if i != 0 {
				artifacts[0], artifacts[i] = artifacts[i], artifacts[0]
			}
			break
		}
	}
	return artifacts, nil
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
	finalEnv := make(map[string]string, len(util.BaseEnv())+len(security.WorkspaceScopedEnv(workDir))+len(env))
	for _, item := range util.BaseEnv() {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			finalEnv[parts[0]] = parts[1]
		}
	}
	for _, item := range security.WorkspaceScopedEnv(workDir) {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			finalEnv[parts[0]] = parts[1]
		}
	}
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			finalEnv[parts[0]] = parts[1]
		}
	}
	cmd.Env = make([]string, 0, len(finalEnv))
	for key, value := range finalEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	sort.Strings(cmd.Env)
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
