package compile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/isolation/cgroup"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/sandbox"
	"aonohako/internal/security"
	"aonohako/internal/util"
	"aonohako/internal/workspacequota"
)

const buildTimeout = 60 * time.Second

const (
	maxDecodedSourceBytes      = 16 << 20
	maxDecodedSourceTotalBytes = 48 << 20
	maxArtifactBytes           = 16 << 20
	maxArtifactTotalBytes      = 48 << 20
	maxSourceFiles             = 512
	ocamlCompileRunParam       = "s=32k"
	compileSandboxMemoryMB     = 2048
	compileSandboxThreadLimit  = 256
	compileWorkspaceBytes      = 512 << 20
	compileOutputCaptureBytes  = 1 << 20
)

type Service struct {
	runtimeTuning         config.RuntimeTuningConfig
	runtimeTuningProfiles map[string]config.RuntimeTuningConfig
	cgroupParentDir       string
}

func New() *Service {
	return NewWithRuntimeTuning(config.DefaultRuntimeTuningConfig())
}

func NewWithRuntimeTuning(tuning config.RuntimeTuningConfig) *Service {
	return &Service{runtimeTuning: tuning.WithSafeDefaults()}
}

func (s *Service) Run(parent context.Context, req *model.CompileRequest) model.CompileResponse {
	if req == nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "nil request"}
	}
	tuning := s.runtimeTuning.WithSafeDefaults()
	if req.RuntimeProfile != "" {
		if err := runtimepolicy.ValidateProfileName(req.RuntimeProfile); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "invalid runtime_profile: " + err.Error()}
		}
		profileTuning, ok := s.runtimeTuningProfiles[req.RuntimeProfile]
		if !ok {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "unknown runtime_profile: " + req.RuntimeProfile}
		}
		tuning = profileTuning.WithSafeDefaults()
	}
	if len(req.Sources) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no sources"}
	}
	if len(req.Sources) > maxSourceFiles {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: fmt.Sprintf("too many sources: max %d", maxSourceFiles)}
	}
	profile, ok := resolveProfile(req.Lang)
	if !ok {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "unsupported lang: " + req.Lang}
	}
	if entryPoint := strings.TrimSpace(req.EntryPoint); entryPoint != "" {
		cleanEntryPoint, err := util.ValidateRelativePath(entryPoint)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "invalid entry_point: " + err.Error()}
		}
		found := false
		for _, src := range req.Sources {
			cleanSource, err := util.ValidateRelativePath(src.Name)
			if err != nil {
				return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
			}
			if cleanSource == cleanEntryPoint {
				found = true
				break
			}
		}
		if !found && (strings.ContainsAny(cleanEntryPoint, `/\`) || filepath.Ext(cleanEntryPoint) != "") {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "entry_point not found in sources: " + cleanEntryPoint}
		}
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
	if err := hardenCompileWorkspace(workDir); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "workspace ownership failed: " + err.Error()}
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
	ctx = withCompileCgroupParent(ctx, s.cgroupParentDir)

	return capCompileResponseOutput(executeBuild(ctx, workDir, profile, target, req, tuning))
}

type compileCgroupParentContextKey struct{}

func withCompileCgroupParent(ctx context.Context, parentDir string) context.Context {
	if strings.TrimSpace(parentDir) == "" {
		return ctx
	}
	return context.WithValue(ctx, compileCgroupParentContextKey{}, parentDir)
}

func compileCgroupParentFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	parentDir, _ := ctx.Value(compileCgroupParentContextKey{}).(string)
	return strings.TrimSpace(parentDir)
}

func capCompileResponseOutput(resp model.CompileResponse) model.CompileResponse {
	var truncated bool
	resp.Stdout, truncated = capCompileOutputValue(resp.Stdout)
	resp.StdoutTruncated = resp.StdoutTruncated || truncated
	resp.Stderr, truncated = capCompileOutputValue(resp.Stderr)
	resp.StderrTruncated = resp.StderrTruncated || truncated
	if resp.ReasonCode == "" {
		resp.ReasonCode = compileReasonCode(resp.Status, resp.Reason)
	}
	return resp
}

func capCompileOutputValue(value string) (string, bool) {
	if len(value) > compileOutputCaptureBytes {
		return value[:compileOutputCaptureBytes], true
	}
	return value, false
}

func compileReasonCode(status, reason string) string {
	lowerReason := strings.ToLower(reason)
	switch {
	case status == model.CompileStatusTimeout || strings.Contains(lowerReason, "context deadline exceeded"):
		return "timeout"
	case strings.Contains(lowerReason, "memory limit exceeded"):
		return "memory_limit_exceeded"
	case strings.Contains(lowerReason, "workspace limit exceeded") || strings.Contains(lowerReason, "workspace scan failed"):
		return "workspace_limit_exceeded"
	case strings.Contains(lowerReason, "pids limit exceeded") || strings.Contains(lowerReason, "process limit exceeded"):
		return "process_limit_exceeded"
	case strings.Contains(lowerReason, "file size limit exceeded"):
		return "file_size_limit_exceeded"
	case strings.Contains(lowerReason, "cpu time limit exceeded"):
		return "cpu_time_limit_exceeded"
	default:
		return ""
	}
}

type cappedTextBuffer struct {
	limit     int
	buf       strings.Builder
	truncated bool
}

func newCompileOutputBuffer() *cappedTextBuffer {
	return &cappedTextBuffer{limit: compileOutputCaptureBytes}
}

func (b *cappedTextBuffer) Append(value string) {
	if b == nil || value == "" || b.limit <= 0 {
		return
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return
	}
	if len(value) > remaining {
		b.buf.WriteString(value[:remaining])
		b.truncated = true
		return
	}
	b.buf.WriteString(value)
}

func (b *cappedTextBuffer) String() string {
	if b == nil {
		return ""
	}
	return b.buf.String()
}

func (b *cappedTextBuffer) Truncated() bool {
	return b != nil && b.truncated
}

func compileResponseWithCapturedOutput(status string, artifacts []model.Artifact, reason string, stdout, stderr *cappedTextBuffer) model.CompileResponse {
	return model.CompileResponse{
		Status:          status,
		Artifacts:       artifacts,
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		Reason:          reason,
	}
}

func resolveProfile(lang string) (profiles.Profile, bool) {
	l := strings.TrimSpace(lang)
	switch strings.ToLower(l) {
	case "asm", "asm64", "assembly", "gas":
		l = "ASM"
	case "aheui":
		l = "AHEUI"
	case "nasm", "nasm64":
		l = "NASM"
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
	case "racket":
		l = "RACKET"
	case "scheme":
		l = "SCHEME"
	case "awk", "gawk":
		l = "AWK"
	case "gdl", "gnudatalanguage":
		l = "GDL"
	case "octave":
		l = "OCTAVE"
	case "ada":
		l = "ADA"
	case "dart":
		l = "DART"
	case "fortran", "fortan":
		l = "FORTRAN"
	case "d":
		l = "D"
	case "vhdl":
		l = "VHDL"
	case "verilog":
		l = "VERILOG"
	case "systemverilog":
		l = "SYSTEMVERILOG"
	case "crystal":
		l = "CRYSTAL"
	case "vlang":
		l = "VLANG"
	case "odin":
		l = "ODIN"
	case "c3":
		l = "C3"
	case "hare":
		l = "HARE"
	case "vb", "vbnet":
		l = "VBNET"
	case "gleam":
		l = "GLEAM"
	case "cuda-ocelot":
		l = "CUDA_OCELOT"
	case "carbon":
		l = "CARBON"
	case "graphql":
		l = "GRAPHQL"
	case "rocq":
		l = "ROCQ"
	case "coq":
		l = "COQ"
	case "lean", "lean4":
		l = "LEAN4"
	case "agda":
		l = "AGDA"
	case "dafny":
		l = "DAFNY"
	case "tla", "tlaplus":
		l = "TLA"
	case "why3", "whyml":
		l = "WHY3"
	case "isabelle":
		l = "ISABELLE"
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
	case "vb6":
		l = "VB6"
	case "classic-basic":
		l = "CLASSIC_BASIC"
	case "qbasic":
		l = "QBASIC"
	case "smalltalk", "gst":
		l = "SMALLTALK"
	case "golfscript":
		l = "GOLFSCRIPT"
	case "mojo":
		l = "MOJO"
	case "deno":
		l = "DENO"
	case "kotlin-jvm":
		l = "KOTLIN_JVM"
	case "duckdb":
		l = "DUCKDB"
	case "bqn":
		l = "BQN"
	case "apl", "gnu-apl":
		l = "APL"
	case "uiua":
		l = "UIUA"
	case "janet":
		l = "JANET"
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

func hardenCompileWorkspace(workDir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	const sandboxUID = 65532
	const sandboxGID = 65532
	scopedDirs := make(map[string]struct{}, len(security.WorkspaceScopedDirs(workDir)))
	for _, dir := range security.WorkspaceScopedDirs(workDir) {
		scopedDirs[dir] = struct{}{}
	}
	if err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != workDir {
			if _, ok := scopedDirs[path]; ok {
				return filepath.SkipDir
			}
		}
		if d.IsDir() {
			return os.Chmod(path, 0o777|os.ModeSticky)
		}
		return os.Chmod(path, 0o444)
	}); err != nil {
		return err
	}
	for _, dir := range security.WorkspaceScopedDirs(workDir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chown(dir, sandboxUID, sandboxGID); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
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

func executeBuild(ctx context.Context, workDir string, profile profiles.Profile, target string, req *model.CompileRequest, tuning config.RuntimeTuningConfig) model.CompileResponse {
	switch profile.CompileKind {
	case "c":
		return compileNative(ctx, workDir, target, gatherByExt(req.Sources, ".c", ".h"), "gcc", []string{"-O2", "-Wall", "-lm", "--static", "-DONLINE_JUDGE", "-std=" + profile.CompileStd})
	case "cpp":
		return compileNative(ctx, workDir, target, gatherByExt(req.Sources, ".cpp", ".cc", ".cxx", ".h", ".hpp"), "g++", []string{"-O2", "-Wall", "-lm", "--static", "-pipe", "-DONLINE_JUDGE", "-std=" + profile.CompileStd})
	case "asm":
		return compileNative(ctx, workDir, target, gatherByExt(req.Sources, ".s"), "gcc", []string{"-nostdlib", "-static", "-no-pie"})
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
		artifacts, err := readSingleArtifact(workDir, target, target, "exec")
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
		artifacts, err := readSingleArtifact(workDir, target, target, "exec")
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
		artifacts, err := readSingleArtifact(workDir, target, target, "exec")
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
		stdout, stderr, status, reason := runCommand(ctx, workDir, "groovyc", args, javaCompileEnv(workDir, 768))
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
		fullOut := newCompileOutputBuffer()
		fullErr := newCompileOutputBuffer()
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".clj") {
				continue
			}
			checked++
			sourcePath := filepath.Join(workDir, filepath.Clean(src.Name))
			parseExpr := fmt.Sprintf(`(require '[clojure.java.io :as io]) (with-open [r (java.io.PushbackReader. (io/reader %q))] (loop [] (let [form (read {:eof ::eof} r)] (when-not (= form ::eof) (recur)))))`, sourcePath)
			stdout, stderr, status, reason := runCommand(ctx, workDir, "clojure", []string{"-e", parseExpr}, javaCompileEnv(workDir, 768))
			fullOut.Append(stdout)
			fullErr.Append(stderr)
			if status != model.CompileStatusOK {
				return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no clojure sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
		if err != nil {
			return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
		}
		return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
	case "racket":
		return compileScriptCheck(ctx, workDir, req.Sources, "raco", []string{"make"})
	case "scheme":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".scm"}, "no scheme sources")
	case "awk":
		return compileCheckedSources(ctx, workDir, req.Sources, []string{".awk"}, "no awk sources", "gawk", []string{"--sandbox", "--lint", "-f"}, nil)
	case "gdl":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".pro"}, "no gdl sources")
	case "octave":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".m"}, "no octave sources")
	case "vhdl":
		return compileVHDL(ctx, workDir, req.Sources, req.EntryPoint)
	case "verilog":
		return compileVerilog(ctx, workDir, target, req.Sources)
	case "crystal":
		return compileCrystal(ctx, workDir, target, req.Sources)
	case "vlang":
		return compileVLang(ctx, workDir, target, req.Sources)
	case "odin":
		return compileOdin(ctx, workDir, target, req.Sources)
	case "c3":
		return compileC3(ctx, workDir, target, req.Sources)
	case "hare":
		return compileHare(ctx, workDir, target, req.Sources)
	case "vbnet":
		return compileVBNet(ctx, workDir, req.Sources)
	case "gleam":
		return compileGleam(ctx, workDir, req.Sources)
	case "cuda-ocelot":
		return compileCUDAOcelot(ctx, workDir, target, req.Sources)
	case "carbon":
		return compileCheckedSources(ctx, workDir, req.Sources, []string{".carbon"}, "no carbon sources", "carbon", []string{"compile", "--phase=check"}, nil)
	case "graphql":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".graphql"}, "no graphql sources")
	case "rocq":
		return compileRocq(ctx, workDir, req.Sources)
	case "lean4":
		return compileCheckedSources(ctx, workDir, req.Sources, []string{".lean"}, "no lean sources", "lean", nil, nil)
	case "agda":
		return compileCheckedSources(ctx, workDir, req.Sources, []string{".agda"}, "no agda sources", "agda", nil, nil)
	case "dafny":
		return compileCheckedSources(ctx, workDir, req.Sources, []string{".dfy"}, "no dafny sources", "dafny", []string{"verify"}, nil)
	case "tla":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".tla", ".cfg"}, "no tla sources")
	case "why3":
		return compileCheckedSources(ctx, workDir, req.Sources, []string{".mlw"}, "no why3 sources", "aonohako-why3-prove", nil, nil)
	case "isabelle":
		return compileIsabelle(ctx, workDir, req.Sources)
	case "python":
		return compilePythonLike(ctx, workDir, req.Sources, "python3")
	case "pypy":
		return compilePythonLike(ctx, workDir, req.Sources, "pypy3")
	case "r":
		var checked int
		fullOut := newCompileOutputBuffer()
		fullErr := newCompileOutputBuffer()
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".r") {
				continue
			}
			checked++
			stdout, stderr, status, reason := runCommand(ctx, workDir, "/usr/lib/R/bin/exec/R", []string{"--vanilla", "--slave", "-e", "parse(file=commandArgs(TRUE)[1])", "--args", filepath.Join(workDir, filepath.Clean(src.Name))}, nil)
			fullOut.Append(stdout)
			fullErr.Append(stderr)
			if status != model.CompileStatusOK {
				return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no r sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
		if err != nil {
			return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
		}
		return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
	case "prolog":
		var checked int
		fullOut := newCompileOutputBuffer()
		fullErr := newCompileOutputBuffer()
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".pl") {
				continue
			}
			checked++
			stdout, stderr, status, reason := runCommand(ctx, workDir, "swipl", []string{"-q", "-f", "none", "-g", "halt", "-t", "halt", filepath.Join(workDir, filepath.Clean(src.Name))}, nil)
			fullOut.Append(stdout)
			fullErr.Append(stderr)
			if status != model.CompileStatusOK {
				return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no prolog sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
		if err != nil {
			return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
		}
		return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
	case "lisp":
		var checked int
		fullOut := newCompileOutputBuffer()
		fullErr := newCompileOutputBuffer()
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".lisp") && !strings.HasSuffix(strings.ToLower(src.Name), ".lsp") {
				continue
			}
			checked++
			clean := filepath.Clean(src.Name)
			sourcePath := filepath.Join(workDir, clean)
			outputPath := filepath.Join(workDir, ".cache", strings.TrimSuffix(filepath.Base(clean), filepath.Ext(clean))+".fasl")
			eval := fmt.Sprintf(`(handler-case (progn (compile-file %q :output-file %q) (sb-ext:exit :code 0)) (error (e) (format *error-output* "~A~%%" e) (sb-ext:exit :code 1)))`, sourcePath, outputPath)
			stdout, stderr, status, reason := runCommand(ctx, workDir, "sbcl", []string{"--noinform", "--non-interactive", "--eval", eval}, nil)
			fullOut.Append(stdout)
			fullErr.Append(stderr)
			if status != model.CompileStatusOK {
				return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
			}
		}
		if checked == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no lisp sources"}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool {
			l := strings.ToLower(name)
			return strings.HasSuffix(l, ".lisp") || strings.HasSuffix(l, ".lsp")
		}, "")
		if err != nil {
			return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
		}
		return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
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
		return compileKotlinNative(ctx, workDir, target, req.Sources, tuning)
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
		artifacts, err := readSingleArtifact(workDir, target, target, "exec")
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
		artifacts, err := readSingleArtifact(workDir, target, target, "exec")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
	case "nasm":
		var rootSource string
		for _, src := range req.Sources {
			if !strings.HasSuffix(strings.ToLower(src.Name), ".asm") {
				continue
			}
			clean := filepath.Clean(src.Name)
			if rootSource == "" || strings.EqualFold(filepath.Base(clean), "Main.asm") {
				rootSource = filepath.Join(workDir, clean)
			}
			if strings.EqualFold(filepath.Base(clean), "Main.asm") {
				break
			}
		}
		if rootSource == "" {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no nasm sources"}
		}
		objectPath := filepath.Join(workDir, target+".o")
		stdout, stderr, status, reason := runCommand(ctx, workDir, "nasm", []string{"-felf64", rootSource, "-o", objectPath}, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		linkOut, linkErr, linkStatus, linkReason := runCommand(ctx, workDir, "gcc", []string{"-nostdlib", "-static", "-no-pie", objectPath, "-o", target}, nil)
		stdout += linkOut
		stderr += linkErr
		if linkStatus != model.CompileStatusOK {
			return model.CompileResponse{Status: linkStatus, Stdout: stdout, Stderr: stderr, Reason: linkReason}
		}
		artifacts, err := readSingleArtifact(workDir, target, target, "exec")
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
		stdout, stderr, status, reason := runCommand(ctx, workDir, "erlc", args, []string{"ERL_AFLAGS=" + erlangAFlags(tuning)})
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
	case "vb6":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".bas", ".frm", ".cls"}, "no vb6 sources")
	case "classic-basic":
		return compileClassicBasic(ctx, workDir, target, req.Sources)
	case "smalltalk":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".st"}, "no smalltalk sources")
	case "golfscript":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".gs"}, "no golfscript sources")
	case "mojo":
		return compileMojo(ctx, workDir, target, req.Sources)
	case "deno":
		return compileCheckedSources(ctx, workDir, req.Sources, []string{".ts", ".js"}, "no deno sources", "deno", []string{"check"}, nil)
	case "kotlin-jvm":
		return compileKotlinJVM(ctx, workDir, target, req.Sources, tuning)
	case "duckdb":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".sql"}, "no duckdb sources")
	case "bqn":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".bqn"}, "no bqn sources")
	case "apl":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".apl"}, "no apl sources")
	case "uiua":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".ua"}, "no uiua sources")
	case "janet":
		return compilePassThroughIfExt(workDir, req.Sources, []string{".janet"}, "no janet sources")
	case "whitespace":
		return compileWhitespace(workDir, req.Sources)
	case "brainfuck":
		return compileBrainfuck(workDir, req.Sources)
	case "wasm":
		return compileWasm(ctx, workDir, target, req.Sources)
	case "ocaml":
		return compileOCaml(ctx, workDir, target, req.Sources)
	case "elixir":
		return compileElixir(ctx, workDir, req.Sources, tuning)
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
		stdout, stderr, status, reason := runCommand(ctx, workDir, "dart", []string{"compile", "exe", rootSource, "-o", filepath.Join(workDir, target)}, []string{"DART_SUPPRESS_ANALYTICS=true"})
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		artifacts, err := readSingleArtifact(workDir, target, target, "exec")
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

func sourcePathsByExt(workDir string, sources []model.Source, exts ...string) []string {
	allowed := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		allowed[strings.ToLower(ext)] = struct{}{}
	}
	var out []string
	for _, src := range sources {
		if _, ok := allowed[strings.ToLower(filepath.Ext(src.Name))]; ok {
			out = append(out, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	sort.Strings(out)
	return out
}

func selectPrimarySource(workDir string, sources []model.Source, exts []string, preferredBases ...string) string {
	allowed := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		allowed[strings.ToLower(ext)] = struct{}{}
	}
	preferred := make(map[string]int, len(preferredBases))
	for i, base := range preferredBases {
		preferred[strings.ToLower(base)] = i + 1
	}
	bestRank := len(preferredBases) + 1
	var selected string
	for _, src := range sources {
		if _, ok := allowed[strings.ToLower(filepath.Ext(src.Name))]; !ok {
			continue
		}
		clean := filepath.Clean(src.Name)
		rank := bestRank
		if value, ok := preferred[strings.ToLower(filepath.Base(clean))]; ok {
			rank = value
		}
		if selected == "" || rank < bestRank || (rank == bestRank && clean < selected) {
			selected = clean
			bestRank = rank
		}
	}
	if selected == "" {
		return ""
	}
	return filepath.Join(workDir, selected)
}

func compilePassThroughIfExt(workDir string, sources []model.Source, exts []string, noSourceReason string) model.CompileResponse {
	if len(sourcePathsByExt(workDir, sources, exts...)) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: noSourceReason}
	}
	return passThroughArtifacts(workDir, sources)
}

func compileCheckedSources(ctx context.Context, workDir string, sources []model.Source, exts []string, noSourceReason, bin string, prefix, env []string) model.CompileResponse {
	paths := sourcePathsByExt(workDir, sources, exts...)
	if len(paths) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: noSourceReason}
	}
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for _, path := range paths {
		args := append(append([]string{}, prefix...), path)
		stdout, stderr, status, reason := runCommand(ctx, workDir, bin, args, env)
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool {
		ext := strings.ToLower(filepath.Ext(name))
		for _, allowed := range exts {
			if ext == strings.ToLower(allowed) {
				return true
			}
		}
		return false
	}, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
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
	args := []string{"--edition", edition, "-O", "-o", target, primary}
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
	args := []string{"build", "-o", target}
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
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileVerilog(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	verilogFiles := sourcePathsByExt(workDir, sources, ".v", ".sv")
	if len(verilogFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no verilog sources"}
	}
	if !strings.HasSuffix(strings.ToLower(target), ".vvp") {
		target += ".vvp"
	}
	args := []string{"-g2012", "-o", filepath.Join(workDir, target)}
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

func compileCrystal(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".cr"}, "Main.cr")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no crystal sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "crystal", []string{"build", rootSource, "--release", "--no-debug", "-o", filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileVLang(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".v"}, "Main.v")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no vlang sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "v", []string{"-o", filepath.Join(workDir, target), rootSource}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileOdin(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	if len(sourcePathsByExt(workDir, sources, ".odin")) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no odin sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "odin", []string{"build", ".", "-out:" + filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileC3(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".c3"}, "Main.c3")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no c3 sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "c3c", []string{"compile", rootSource, "-o", filepath.Join(workDir, target)}, nil)
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
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

func compilePythonLike(ctx context.Context, workDir string, sources []model.Source, interpreter string) model.CompileResponse {
	stdout, stderr, status, reason := runCommand(ctx, workDir, interpreter, []string{"-I", "-S", "-m", "compileall", "-b", "."}, nil)
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
	fullOut := newCompileOutputBuffer()
	fullErr := newCompileOutputBuffer()
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		abs := filepath.Join(workDir, clean)
		args := append(append([]string{}, prefix...), abs)
		stdout, stderr, status, reason := runCommand(ctx, workDir, bin, args, nil)
		fullOut.Append(stdout)
		fullErr.Append(stderr)
		if status != model.CompileStatusOK {
			return compileResponseWithCapturedOutput(status, nil, reason, fullOut, fullErr)
		}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool { return true }, "")
	if err != nil {
		return compileResponseWithCapturedOutput(model.CompileStatusInternal, nil, err.Error(), fullOut, fullErr)
	}
	return compileResponseWithCapturedOutput(model.CompileStatusOK, artifacts, "", fullOut, fullErr)
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
	args := []string{"--module", "commonjs", "--target", "es2019", "--sourceMap", "--outDir", "dist"}
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
	args := []string{"-O", "-module-cache-path", moduleCacheDir, "-o", target}
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
		sdkDirs, err := filepath.Glob("/opt/dotnet/sdk/*")
		if err != nil || len(sdkDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet sdk not found"}
		}
		sort.Strings(sdkDirs)
		fsharpDir := filepath.Join(sdkDirs[len(sdkDirs)-1], "FSharp")
		fscPath := filepath.Join(fsharpDir, "fsc.dll")
		if _, err := os.Stat(fscPath); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "fsharp compiler not found"}
		}
		fsharpCorePath := filepath.Join(fsharpDir, "FSharp.Core.dll")
		if _, err := os.Stat(fsharpCorePath); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "FSharp.Core not found"}
		}
		refDirs, err := filepath.Glob("/opt/dotnet/packs/Microsoft.NETCore.App.Ref/*/ref/net8.0")
		if err != nil || len(refDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference pack not found"}
		}
		sort.Strings(refDirs)
		refDLLs, err := filepath.Glob(filepath.Join(refDirs[len(refDirs)-1], "*.dll"))
		if err != nil || len(refDLLs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference assemblies not found"}
		}
		sort.Strings(refDLLs)
		outDLL := filepath.Join(workDir, "App.dll")
		args := []string{
			fscPath,
			"--nologo",
			"--target:exe",
			"--targetprofile:netcore",
			"--noframework",
			"--out:" + outDLL,
		}
		for _, refDLL := range refDLLs {
			args = append(args, "-r:"+refDLL)
		}
		args = append(args, "-r:"+fsharpCorePath)
		for _, file := range fsFiles {
			args = append(args, filepath.Join(projectDir, file))
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		runtimeConfig, err := dotnetRuntimeConfig()
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		if err := os.WriteFile(filepath.Join(workDir, "App.runtimeconfig.json"), runtimeConfig, 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool {
			lower := strings.ToLower(name)
			return lower == "app.dll" || lower == "app.runtimeconfig.json" || lower == "fsharp.core.dll"
		}, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
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

func compileVBNet(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	projectDir := filepath.Join(workDir, "vbproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	var projectPath string
	var vbFiles []string
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, ".vbproj") && projectPath == "" {
			projectPath = filepath.Join(projectDir, clean)
		}
		if strings.HasSuffix(lower, ".vb") {
			vbFiles = append(vbFiles, clean)
		}
	}
	if projectPath == "" && len(vbFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no vbnet sources"}
	}
	if err := materializeSources(projectDir, sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	if projectPath == "" {
		sdkDirs, err := filepath.Glob("/opt/dotnet/sdk/*")
		if err != nil || len(sdkDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet sdk not found"}
		}
		sort.Strings(sdkDirs)
		vbcPath := filepath.Join(sdkDirs[len(sdkDirs)-1], "Roslyn", "bincore", "vbc.dll")
		if _, err := os.Stat(vbcPath); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "vb compiler not found"}
		}
		refDirs, err := filepath.Glob("/opt/dotnet/packs/Microsoft.NETCore.App.Ref/*/ref/net8.0")
		if err != nil || len(refDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference pack not found"}
		}
		sort.Strings(refDirs)
		refDLLs, err := filepath.Glob(filepath.Join(refDirs[len(refDirs)-1], "*.dll"))
		if err != nil || len(refDLLs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference assemblies not found"}
		}
		sort.Strings(refDLLs)
		outDLL := filepath.Join(workDir, "App.dll")
		args := []string{vbcPath, "-nologo", "-target:exe", "-optimize+", "-out:" + outDLL}
		for _, refDLL := range refDLLs {
			args = append(args, "-r:"+refDLL)
		}
		for _, file := range vbFiles {
			args = append(args, filepath.Join(projectDir, file))
		}
		stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		runtimeConfig, err := dotnetRuntimeConfig()
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		if err := os.WriteFile(filepath.Join(workDir, "App.runtimeconfig.json"), runtimeConfig, 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool {
			lower := strings.ToLower(name)
			return lower == "app.dll" || lower == "app.runtimeconfig.json"
		}, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
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
	stdout, stderr, status, reason := runCommand(ctx, workDir, "gleam", []string{"build", "--offline"}, []string{
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

func compileClassicBasic(ctx context.Context, workDir, target string, sources []model.Source) model.CompileResponse {
	rootSource := selectPrimarySource(workDir, sources, []string{".bas"}, "Main.bas")
	if rootSource == "" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no classic-basic sources"}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "fbc", []string{"-lang", "qb", "-x", filepath.Join(workDir, target), rootSource}, nil)
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

func compileKotlinJVM(ctx context.Context, workDir, target string, sources []model.Source, tuning config.RuntimeTuningConfig) model.CompileResponse {
	kt := sourcePathsByExt(workDir, sources, ".kt")
	if len(kt) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no kotlin-jvm sources"}
	}
	if !strings.HasSuffix(strings.ToLower(target), ".jar") {
		target += ".jar"
	}
	tuning = tuning.WithSafeDefaults()
	args := []string{
		"-J-Xms64m",
		fmt.Sprintf("-J-Xmx%dm", max(512, tuning.KotlinNativeCompilerHeapMB)),
		"-J-Xss1m",
		"-J-XX:+UseSerialGC",
		"-include-runtime",
		"-d",
		filepath.Join(workDir, target),
	}
	args = append(args, kt...)
	stdout, stderr, status, reason := runCommand(ctx, workDir, "kotlinc", args, javaCompileEnv(workDir, max(512, tuning.KotlinNativeCompilerHeapMB)))
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := readSingleArtifact(workDir, target, target, "")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
	}
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
}

func compileRocq(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	bin := "rocq"
	prefix := []string{"c"}
	if _, err := exec.LookPath("rocq"); err != nil {
		bin = "coqc"
		prefix = []string{"-q"}
	}
	return compileCheckedSources(ctx, workDir, sources, []string{".v"}, "no rocq sources", bin, prefix, []string{"OCAMLRUNPARAM=" + ocamlCompileRunParam})
}

func compileIsabelle(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	thyFiles := sourcePathsByExt(workDir, sources, ".thy")
	if len(thyFiles) == 0 {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no isabelle sources"}
	}
	if _, err := os.Stat(filepath.Join(workDir, "ROOT")); err != nil {
		theories := make([]string, 0, len(thyFiles))
		for _, path := range thyFiles {
			theories = append(theories, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		}
		sort.Strings(theories)
		root := "session Aonohako = HOL +\n  theories\n"
		for _, theory := range theories {
			root += "    " + theory + "\n"
		}
		if err := os.WriteFile(filepath.Join(workDir, "ROOT"), []byte(root), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "isabelle", []string{"build", "-D", "."}, isabelleCompileEnv())
	if status != model.CompileStatusOK {
		return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
	}
	artifacts, err := collectArtifacts(workDir, func(name string) bool {
		lower := strings.ToLower(name)
		return lower == "root" || strings.HasSuffix(lower, ".thy")
	}, "")
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
	artifacts, err := readSingleArtifact(workDir, target, target, "")
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
	artifacts, err := readSingleArtifact(workDir, target, target, "exec")
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

func compileCSharp(ctx context.Context, workDir string, sources []model.Source) model.CompileResponse {
	projectDir := filepath.Join(workDir, "csproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	var hasProject bool
	var projectPath string
	var csFiles []string
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
		}
		if strings.HasSuffix(strings.ToLower(clean), ".cs") {
			csFiles = append(csFiles, filepath.Join(workDir, clean))
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
		if len(csFiles) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "no csharp sources"}
		}
		sdkDirs, err := filepath.Glob("/opt/dotnet/sdk/*")
		if err != nil || len(sdkDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet sdk not found"}
		}
		sort.Strings(sdkDirs)
		cscPath := filepath.Join(sdkDirs[len(sdkDirs)-1], "Roslyn", "bincore", "csc.dll")
		if _, err := os.Stat(cscPath); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "csc compiler not found"}
		}
		refDirs, err := filepath.Glob("/opt/dotnet/packs/Microsoft.NETCore.App.Ref/*/ref/net8.0")
		if err != nil || len(refDirs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference pack not found"}
		}
		sort.Strings(refDirs)
		refDLLs, err := filepath.Glob(filepath.Join(refDirs[len(refDirs)-1], "*.dll"))
		if err != nil || len(refDLLs) == 0 {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "dotnet reference assemblies not found"}
		}
		sort.Strings(refDLLs)
		outDLL := filepath.Join(workDir, "App.dll")
		globalUsingsPath := filepath.Join(workDir, "Aonohako.GlobalUsings.g.cs")
		globalUsings := "global using System;\n" +
			"global using System.Collections.Generic;\n" +
			"global using System.IO;\n" +
			"global using System.Linq;\n" +
			"global using System.Net.Http;\n" +
			"global using System.Threading;\n" +
			"global using System.Threading.Tasks;\n"
		if err := os.WriteFile(globalUsingsPath, []byte(globalUsings), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		args := []string{cscPath, "-nologo", "-target:exe", "-langversion:latest", "-optimize+", "-out:" + outDLL}
		for _, refDLL := range refDLLs {
			args = append(args, "-r:"+refDLL)
		}
		args = append(args, csFiles...)
		args = append(args, globalUsingsPath)
		stdout, stderr, status, reason := runCommand(ctx, workDir, "dotnet", args, nil)
		if status != model.CompileStatusOK {
			return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
		}
		runtimeConfig, err := dotnetRuntimeConfig()
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		if err := os.WriteFile(filepath.Join(workDir, "App.runtimeconfig.json"), runtimeConfig, 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		artifacts, err := collectArtifacts(workDir, func(name string) bool {
			lower := strings.ToLower(name)
			return lower == "app.dll" || lower == "app.runtimeconfig.json"
		}, "")
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error(), Stdout: stdout, Stderr: stderr}
		}
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts, Stdout: stdout, Stderr: stderr}
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

func dotnetRuntimeConfig() ([]byte, error) {
	runtimeDirs, err := filepath.Glob("/opt/dotnet/shared/Microsoft.NETCore.App/*")
	if err != nil || len(runtimeDirs) == 0 {
		return nil, fmt.Errorf("dotnet runtime not found")
	}
	sort.Strings(runtimeDirs)
	runtimeVersion := filepath.Base(runtimeDirs[len(runtimeDirs)-1])
	return []byte(fmt.Sprintf("{\n  \"runtimeOptions\": {\n    \"tfm\": \"net8.0\",\n    \"framework\": {\n      \"name\": \"Microsoft.NETCore.App\",\n      \"version\": %q\n    }\n  }\n}\n", runtimeVersion)), nil
}

func dotnetBuildEnv() []string {
	return []string{
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1",
		"DOTNET_GENERATE_ASPNET_CERTIFICATE=false",
		"DOTNET_NOLOGO=1",
		"MSBuildEnableWorkloadResolver=false",
	}
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

func isabelleCompileEnv() []string {
	return []string{
		"ISABELLE_JAVA_OPTIONS=-Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC",
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

func RunSandboxedCommand(ctx context.Context, workDir, bin string, args, env []string) (stdout, stderr, status, reason string) {
	stdout, stderr, status, reason = runSandboxedCommand(ctx, workDir, bin, args, env)
	stdout, _ = capCompileOutputValue(stdout)
	stderr, _ = capCompileOutputValue(stderr)
	return stdout, stderr, status, reason
}

func runSandboxedCommand(ctx context.Context, workDir, bin string, args, env []string) (stdout, stderr, status, reason string) {
	for _, dir := range security.WorkspaceScopedDirs(workDir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", "", model.CompileStatusInternal, "workspace prep failed: " + err.Error()
		}
	}
	if os.Geteuid() == 0 {
		scopedDirs := make(map[string]struct{}, len(security.WorkspaceScopedDirs(workDir)))
		for _, dir := range security.WorkspaceScopedDirs(workDir) {
			scopedDirs[dir] = struct{}{}
		}
		if err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if path != workDir {
				if _, ok := scopedDirs[path]; ok {
					return filepath.SkipDir
				}
			}
			return os.Chmod(path, 0o777|os.ModeSticky)
		}); err != nil {
			return "", "", model.CompileStatusInternal, "workspace prep failed: " + err.Error()
		}
		for _, dir := range security.WorkspaceScopedDirs(workDir) {
			if err := os.Chown(dir, 65532, 65532); err != nil {
				return "", "", model.CompileStatusInternal, "workspace prep failed: " + err.Error()
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				return "", "", model.CompileStatusInternal, "workspace prep failed: " + err.Error()
			}
		}
	}
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
	for _, key := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "no_proxy"} {
		finalEnv[key] = ""
	}
	command := append([]string{bin}, args...)
	helperEnv := make([]string, 0, len(finalEnv))
	for key, value := range finalEnv {
		helperEnv = append(helperEnv, key+"="+value)
	}
	sort.Strings(helperEnv)
	if !filepath.IsAbs(command[0]) {
		path, err := util.ResolveCommandPath(command[0], helperEnv)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return "", "", model.CompileStatusInternal, bin + " not found"
			}
			return "", "", model.CompileStatusInternal, err.Error()
		}
		command[0] = path
	}
	commandName := filepath.Base(command[0])
	isDotnet := commandName == "dotnet"
	isDotnetLike := isDotnet || commandName == "dafny"
	if isDotnetLike {
		if err := security.ResetDotnetSharedState(); err != nil {
			return "", "", model.CompileStatusInternal, "dotnet state cleanup failed: " + err.Error()
		}
	}
	// CoreCLR reserves a very large memfd-backed double-mapped region during
	// startup, so finite RLIMIT_AS and RLIMIT_FSIZE values can fail before user
	// code. Dotnet still has RSS, workspace, stdout/stderr, fd, and thread caps.
	disableAddressSpaceLimit := isDotnetLike || commandName == "c3c" || commandName == "carbon" || commandName == "kotlinc"
	allowProcessGroups := commandName == "swiftc"
	allowChmod := isDotnetLike || commandName == "hare" || commandName == "isabelle"
	openFileLimit := security.OpenFileLimitForCommand(command[0])
	memoryLimitMB := compileSandboxMemoryMB
	if commandName == "kotlinc-native" {
		memoryLimitMB = 4096
	}
	if commandName == "kotlinc" || commandName == "dafny" || commandName == "isabelle" {
		memoryLimitMB = 4096
	}
	memoryLimitKB := int64(memoryLimitMB) * 1024
	helperReq := sandbox.ExecRequest{
		Command: append([]string(nil), command...),
		Dir:     workDir,
		Env:     helperEnv,
		Limits: model.Limits{
			TimeMs:         int(buildTimeout / time.Millisecond),
			MemoryMB:       memoryLimitMB,
			WorkspaceBytes: compileWorkspaceBytes,
		},
		ThreadLimit:              compileSandboxThreadLimit,
		OpenFileLimit:            openFileLimit,
		StackLimitBytes:          security.StackLimitForCommand(command[0]),
		FileSizeLimitBytes:       security.FileSizeLimitForCommand(command[0], compileWorkspaceBytes),
		EnableNetwork:            false,
		AllowUnixSockets:         true,
		AllowProcesses:           true,
		AllowProcessGroups:       allowProcessGroups,
		AllowMemfdCreate:         isDotnetLike,
		AllowChmod:               allowChmod,
		DisableAddressSpaceLimit: disableAddressSpaceLimit,
		DisableFileSizeLimit:     isDotnetLike,
	}
	rawReq, err := json.Marshal(helperReq)
	if err != nil {
		return "", "", model.CompileStatusInternal, "sandbox request failed: " + err.Error()
	}

	requestRead, requestWrite, err := os.Pipe()
	if err != nil {
		return "", "", model.CompileStatusInternal, "sandbox request pipe failed: " + err.Error()
	}
	defer requestRead.Close()
	defer requestWrite.Close()
	helperPath, err := os.Executable()
	if err != nil {
		return "", "", model.CompileStatusInternal, "resolve helper failed: " + err.Error()
	}
	cmd := exec.CommandContext(ctx, helperPath)
	cmd.Dir = workDir
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		sandbox.HelperModeEnv + "=" + sandbox.HelperModeExec,
		sandbox.RequestFDEnv + "=3",
	}
	cmd.ExtraFiles = []*os.File{requestRead}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: 65532, Gid: 65532}
	}
	stdoutFile, err := os.CreateTemp(filepath.Join(workDir, ".tmp"), "compile-stdout-*")
	if err != nil {
		return "", "", model.CompileStatusInternal, "stdout capture failed: " + err.Error()
	}
	defer func() {
		_ = stdoutFile.Close()
		_ = os.Remove(stdoutFile.Name())
	}()
	stderrFile, err := os.CreateTemp(filepath.Join(workDir, ".tmp"), "compile-stderr-*")
	if err != nil {
		return "", "", model.CompileStatusInternal, "stderr capture failed: " + err.Error()
	}
	defer func() {
		_ = stderrFile.Close()
		_ = os.Remove(stderrFile.Name())
	}()
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		return "", "", model.CompileStatusInternal, "start failed: " + err.Error()
	}
	cgroupParentDir := compileCgroupParentFromContext(ctx)
	var runGroup cgroup.Group
	if cgroupParentDir != "" {
		if err := cgroup.EnableControllers(cgroupParentDir, []string{"cpu", "memory", "pids"}); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return "", "", model.CompileStatusInternal, "cgroup controller setup failed: " + err.Error()
		}
		group, err := cgroup.CreateRunGroup(cgroupParentDir, cgroup.RunName("compile"), cgroup.Limits{
			MemoryMaxBytes:  memoryLimitKB * 1024,
			PidsMax:         compileSandboxThreadLimit + 32,
			CPUQuotaMicros:  cgroup.SingleCPUQuotaMicros,
			CPUPeriodMicros: cgroup.DefaultCPUPeriodMicros,
		})
		if err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return "", "", model.CompileStatusInternal, "cgroup create failed: " + err.Error()
		}
		runGroup = group
		if err := runGroup.AddProc(cmd.Process.Pid); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			_ = runGroup.RemoveWithRetry(250 * time.Millisecond)
			return "", "", model.CompileStatusInternal, "cgroup add process failed: " + err.Error()
		}
		defer func() {
			_ = runGroup.RemoveWithRetry(250 * time.Millisecond)
		}()
	}
	_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", cmd.Process.Pid), []byte("1000\n"), 0o644)
	_ = requestRead.Close()
	if n, err := requestWrite.Write(rawReq); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return "", "", model.CompileStatusInternal, "sandbox request write failed: " + err.Error()
	} else if n != len(rawReq) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return "", "", model.CompileStatusInternal, "sandbox request write failed: short write"
	}
	if err := requestWrite.Close(); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return "", "", model.CompileStatusInternal, "sandbox request write failed: " + err.Error()
	}
	pgid := cmd.Process.Pid
	descendantPIDs := func() map[int]bool {
		descendants := map[int]bool{pgid: true}
		for changed := true; changed; {
			changed = false
			entries, err := os.ReadDir("/proc")
			if err != nil {
				break
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				pid, err := strconv.Atoi(entry.Name())
				if err != nil || descendants[pid] {
					continue
				}
				raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
				if err != nil {
					continue
				}
				ppid := 0
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.HasPrefix(line, "PPid:") {
						fields := strings.Fields(line)
						if len(fields) >= 2 {
							ppid, _ = strconv.Atoi(fields[1])
						}
						break
					}
				}
				if ppid > 0 && descendants[ppid] {
					descendants[pid] = true
					changed = true
				}
			}
		}
		return descendants
	}
	processTreeRSSKB := func(pids map[int]bool) int64 {
		pageKB := int64(os.Getpagesize() / 1024)
		var total int64
		for pid := range pids {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
			if err != nil {
				continue
			}
			fields := strings.Fields(string(raw))
			if len(fields) < 2 {
				continue
			}
			rssPages, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				continue
			}
			total += rssPages * pageKB
		}
		return total
	}
	killSandbox := func() {
		descendants := descendantPIDs()
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		for pid := range descendants {
			if pid != pgid {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	readCaptured := func(file *os.File) string {
		if _, err := file.Seek(0, 0); err != nil {
			return ""
		}
		data, err := io.ReadAll(io.LimitReader(file, compileOutputCaptureBytes+1))
		if err != nil {
			return ""
		}
		return string(data)
	}
	defer killSandbox()
	watchdog := time.NewTicker(25 * time.Millisecond)
	defer watchdog.Stop()
	lastWorkspaceScan := time.Time{}
	for {
		select {
		case <-ctx.Done():
			killSandbox()
			<-waitCh
			return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusTimeout, ctx.Err().Error()
		case <-watchdog.C:
			if runGroup.Path != "" {
				if stats, err := cgroup.ReadStats(runGroup.Path); err == nil {
					if stats.MemoryLimitBreached(memoryLimitKB * 1024) {
						killSandbox()
						<-waitCh
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "memory limit exceeded"
					}
					if stats.PidsLimitBreached() {
						killSandbox()
						<-waitCh
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "process limit exceeded"
					}
				}
			}
			if rssKB := processTreeRSSKB(descendantPIDs()); rssKB > memoryLimitKB {
				killSandbox()
				<-waitCh
				return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "memory limit exceeded"
			}
			if lastWorkspaceScan.IsZero() || time.Since(lastWorkspaceScan) >= 25*time.Millisecond {
				lastWorkspaceScan = time.Now()
				usage, err := workspacequota.Scan(workDir)
				if errors.Is(err, workspacequota.ErrEntryLimitExceeded) {
					killSandbox()
					<-waitCh
					return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace entry limit exceeded"
				}
				if errors.Is(err, workspacequota.ErrDepthExceeded) {
					killSandbox()
					<-waitCh
					return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace depth exceeded"
				}
				if err != nil {
					killSandbox()
					<-waitCh
					return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace scan failed"
				}
				if usage.Bytes > int64(compileWorkspaceBytes) {
					killSandbox()
					<-waitCh
					return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace quota exceeded"
				}
			}
		case err := <-waitCh:
			if err != nil {
				reason := err.Error()
				if ps := cmd.ProcessState; ps != nil {
					if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
						if ws.Signaled() {
							reason = fmt.Sprintf("sandbox command killed by signal %s", ws.Signal())
						} else if ws.Exited() {
							reason = fmt.Sprintf("sandbox command exited with code %d", ws.ExitStatus())
						}
					}
				}
				return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, reason
			}
			return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusOK, ""
		}
	}
}

func runCommand(ctx context.Context, workDir, bin string, args, env []string) (stdout, stderr, status, reason string) {
	return runSandboxedCommand(ctx, workDir, bin, args, env)
}

func readSingleArtifact(root, rel, name, mode string) ([]model.Artifact, error) {
	artifact, err := openArtifact(root, rel)
	if err != nil {
		return nil, fmt.Errorf("read artifact failed: %w", err)
	}
	defer artifact.cleanup()
	if artifact.info.Size() > maxArtifactBytes {
		return nil, fmt.Errorf("artifact too large: %s", name)
	}
	data, err := io.ReadAll(artifact.file)
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
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("artifact path contains a symlink: %s", d.Name())
		}
		if include != nil && !include(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		artifact, err := openArtifact(root, rel)
		if err != nil {
			return err
		}
		info := artifact.info
		if info.Size() > maxArtifactBytes {
			artifact.cleanup()
			return fmt.Errorf("artifact too large: %s", d.Name())
		}
		totalBytes += info.Size()
		if totalBytes > maxArtifactTotalBytes {
			artifact.cleanup()
			return fmt.Errorf("artifact total size exceeded")
		}
		data, err := io.ReadAll(artifact.file)
		if err != nil {
			artifact.cleanup()
			return err
		}
		name := filepath.ToSlash(rel)
		if prefix != "" {
			name = filepath.ToSlash(filepath.Join(prefix, rel))
		}
		artifacts = append(artifacts, model.Artifact{Name: name, DataB64: util.EncodeB64(data)})
		artifact.cleanup()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return artifacts, nil
}
