package compile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
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
	versionedProfile, err := applyRequestedVersion(profile, req.Version)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	profile = versionedProfile
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
		slog.Warn("compile work directory creation failed", "err", err)
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "work directory creation failed"}
	}
	defer os.RemoveAll(workDir)
	for _, dir := range security.WorkspaceScopedDirs(workDir) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			slog.Warn("compile workspace preparation failed", "err", err)
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "workspace preparation failed"}
		}
	}

	if err := materializeSources(workDir, req.Sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	if err := hardenCompileWorkspace(workDir); err != nil {
		slog.Warn("compile workspace hardening failed", "err", err)
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "workspace hardening failed"}
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
	case "tcl":
		l = "TCL"
	case "gdl", "gnudatalanguage":
		l = "GDL"
	case "octave":
		l = "OCTAVE"
	case "ada":
		l = "ADA"
	case "cobol":
		l = "COBOL"
	case "gnucobol":
		l = "GNUCOBOL"
	case "cython":
		l = "CYTHON"
	case "dart":
		l = "DART"
	case "fortran", "fortan":
		l = "FORTRAN"
	case "d":
		l = "D"
	case "objective-c", "objc":
		l = "OBJC"
	case "objective-cpp", "objcpp":
		l = "OBJCPP"
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
	case "haxe":
		l = "HAXE"
	case "c", "c11":
		l = "C11"
	case "c89", "c90":
		l = "C89"
	case "c99":
		l = "C99"
	case "c17", "c18":
		l = "C17"
	case "c23":
		l = "C23"
	case "cpp", "c++":
		l = "CPP17"
	case "java":
		l = "JAVA11"
	case "groovy":
		l = "GROOVY"
	case "raku":
		l = "RAKU"
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
	case "freebasic":
		l = "FREEBASIC"
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
	case "kotlin-jvm", "kotlin_java", "kotlin-java", "kotlin/java", "kotlinjava":
		l = "KOTLIN_JVM"
	case "kotlin-jvm8", "kotlin-java8", "kotlin/java8":
		l = "KOTLIN_JVM8"
	case "kotlin-jvm11", "kotlin-java11", "kotlin/java11":
		l = "KOTLIN_JVM11"
	case "kotlin-jvm17", "kotlin-java17", "kotlin/java17":
		l = "KOTLIN_JVM17"
	case "kotlin-jvm21", "kotlin-java21", "kotlin/java21":
		l = "KOTLIN_JVM21"
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
	case "coffeescript", "coffee":
		l = "COFFEESCRIPT"
	case "sed":
		l = "SED"
	case "bc":
		l = "BC"
	case "forth":
		l = "FORTH"
	case "gforth":
		l = "GFORTH"
	case "whitespace":
		l = "WHITESPACE"
	case "bf", "brainfuck":
		l = "BF"
	case "wasm", "webassembly":
		l = "WASM"
	}
	return profiles.Resolve(l)
}

func applyRequestedVersion(profile profiles.Profile, raw string) (profiles.Profile, error) {
	version := normalizeRequestedVersion(raw)
	if version == "" {
		return profile, nil
	}

	switch profile.CompileKind {
	case "c":
		std, ok := cStandardVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported C version: %s", raw)
		}
		profile.CompileStd = std
	case "cpp":
		std, ok := cppStandardVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported C++ version: %s", raw)
		}
		profile.CompileStd = std
	case "java":
		release, ok := javaReleaseVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported Java version: %s", raw)
		}
		profile.JavaRelease = release
	case "kotlin-jvm":
		release, ok := javaReleaseVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported Kotlin/JVM target: %s", raw)
		}
		profile.JavaRelease = release
	case "rust":
		edition, ok := rustEditionVersion(version)
		if !ok {
			return profile, fmt.Errorf("unsupported Rust edition: %s", raw)
		}
		profile.RustEdition = edition
	default:
		return profile, fmt.Errorf("version is not supported for %s", profile.SourceLang)
	}
	return profile, nil
}

func normalizeRequestedVersion(raw string) string {
	version := strings.ToLower(strings.TrimSpace(raw))
	version = strings.TrimPrefix(version, "std=")
	version = strings.TrimPrefix(version, "--std=")
	version = strings.TrimPrefix(version, "release=")
	version = strings.TrimPrefix(version, "--release=")
	version = strings.TrimPrefix(version, "jvm=")
	version = strings.TrimPrefix(version, "jvmtarget=")
	version = strings.TrimPrefix(version, "jvm_target=")
	version = strings.TrimPrefix(version, "jvm-target=")
	version = strings.TrimPrefix(version, "--jvm-target=")
	version = strings.TrimPrefix(version, "--jvm_target=")
	version = strings.TrimPrefix(version, "edition=")
	version = strings.TrimPrefix(version, "--edition=")
	return strings.ReplaceAll(version, "_", "")
}

func cStandardVersion(version string) (string, bool) {
	switch version {
	case "89", "90", "ansi", "c89", "c90", "iso9899:1990":
		return "c90", true
	case "gnu89", "gnu90":
		return "gnu90", true
	case "99", "c99", "iso9899:1999":
		return "c99", true
	case "gnu99":
		return "gnu99", true
	case "11", "c11", "iso9899:2011":
		return "c11", true
	case "gnu11":
		return "gnu11", true
	case "17", "18", "c17", "c18", "iso9899:2017", "iso9899:2018":
		return "c17", true
	case "gnu17", "gnu18":
		return "gnu17", true
	case "23", "c23", "c2x", "iso9899:2024":
		return "c23", true
	case "gnu23", "gnu2x":
		return "gnu23", true
	default:
		return "", false
	}
}

func cppStandardVersion(version string) (string, bool) {
	switch version {
	case "03", "98", "cpp03", "cpp98", "c++03", "c++98":
		return "c++03", true
	case "gnu++03", "gnu++98":
		return "gnu++03", true
	case "11", "cpp11", "c++11":
		return "c++11", true
	case "gnu++11":
		return "gnu++11", true
	case "14", "cpp14", "c++14":
		return "c++14", true
	case "gnu++14":
		return "gnu++14", true
	case "17", "cpp17", "c++17":
		return "c++17", true
	case "gnu++17":
		return "gnu++17", true
	case "20", "cpp20", "c++20":
		return "c++20", true
	case "gnu++20":
		return "gnu++20", true
	case "23", "cpp23", "c++23":
		return "c++23", true
	case "gnu++23":
		return "gnu++23", true
	case "26", "cpp26", "c++26":
		return "c++26", true
	case "gnu++26":
		return "gnu++26", true
	default:
		return "", false
	}
}

func javaReleaseVersion(version string) (string, bool) {
	version = strings.TrimPrefix(version, "java")
	switch version {
	case "1.8":
		return "8", true
	case "8", "11", "15", "17", "21":
		return version, true
	default:
		return "", false
	}
}

func rustEditionVersion(version string) (string, bool) {
	version = strings.TrimPrefix(version, "rust")
	version = strings.TrimPrefix(version, "edition")
	switch version {
	case "2015", "2018", "2021", "2024":
		return version, true
	default:
		return "", false
	}
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
	if compiler, ok := lookupCompiler(profile.CompileKind); ok {
		return compiler.Compile(ctx, CompileJob{
			WorkDir: workDir,
			Target:  target,
			Profile: profile,
			Request: req,
			Tuning:  tuning,
			Runner:  sandboxCommandRunner{},
		})
	}
	return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "unsupported compile kind: " + profile.CompileKind}
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
	return passThroughCompiler{exts: exts, noSourceReason: noSourceReason}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: sources},
	})
}

func compileCheckedSources(ctx context.Context, workDir string, sources []model.Source, exts []string, noSourceReason, bin string, prefix, env []string) model.CompileResponse {
	return checkedSourcesCompiler{exts: exts, noSourceReason: noSourceReason, bin: bin, prefix: prefix, env: env}.Compile(ctx, CompileJob{
		WorkDir: workDir,
		Request: &model.CompileRequest{Sources: sources},
		Runner:  sandboxCommandRunner{},
	})
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
	homeDir := filepath.Join(workDir, ".home")
	tmpDir := filepath.Join(workDir, ".tmp")
	isabelleSettingDirs := []string{
		filepath.Join(homeDir, ".isabelle", "etc"),
		filepath.Join(homeDir, ".isabelle", "Isabelle2025-2", "etc"),
	}
	if bin, err := exec.LookPath("isabelle"); err == nil {
		if realBin, err := filepath.EvalSymlinks(bin); err == nil {
			identifierPath := filepath.Clean(filepath.Join(filepath.Dir(realBin), "..", "etc", "ISABELLE_IDENTIFIER"))
			if data, err := os.ReadFile(identifierPath); err == nil {
				if identifier := strings.TrimSpace(string(data)); identifier != "" {
					isabelleSettingDirs = append(isabelleSettingDirs, filepath.Join(homeDir, ".isabelle", identifier, "etc"))
				}
			}
		}
	}
	isabelleSettings := fmt.Sprintf(
		"ISABELLE_TMP_PREFIX=\"%[1]s/isabelle\"\n"+
			"ISABELLE_SETUP_CLASSPATH=\"$ISABELLE_HOME/lib/classes/isabelle.jar:$ISABELLE_HOME/src/Tools/Demo/lib/demo.jar:$ISABELLE_HOME/lib/classes/isabelle_graphbrowser.jar\"\n"+
			"ISABELLE_SETUP_JAR=\"\"\n"+
			"ISABELLE_TOOL_JAVA_OPTIONS=\"-Djava.awt.headless=true -Djava.io.tmpdir=%[1]s -Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m\"\n"+
			"ISABELLE_JAVA_SYSTEM_OPTIONS=\"-server -Dfile.encoding=UTF-8 -Djava.io.tmpdir=%[1]s -Disabelle.threads=1 -Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m\"\n"+
			"ISABELLE_JAVA_OPTIONS=\"-Djava.io.tmpdir=%[1]s -Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC\"\n",
		tmpDir,
	)
	seenSettings := map[string]struct{}{}
	for _, settingsDir := range isabelleSettingDirs {
		if _, ok := seenSettings[settingsDir]; ok {
			continue
		}
		seenSettings[settingsDir] = struct{}{}
		if err := os.MkdirAll(settingsDir, 0o777); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
		for dir := settingsDir; strings.HasPrefix(dir, homeDir+string(os.PathSeparator)); dir = filepath.Dir(dir) {
			if err := os.Chmod(dir, 0o777); err != nil {
				return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
			}
			if dir == homeDir {
				break
			}
		}
		if err := os.WriteFile(filepath.Join(settingsDir, "settings"), []byte(isabelleSettings), 0o644); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
		}
	}
	stdout, stderr, status, reason := runCommand(ctx, workDir, "isabelle", []string{"process_theories", "-o", "naproche_server=false", "-D", "."}, isabelleCompileEnv(workDir))
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

func isabelleCompileEnv(workDir string) []string {
	tmp := filepath.Join(workDir, ".tmp")
	return []string{
		fmt.Sprintf("JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=%s -Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC -XX:ReservedCodeCacheSize=32m -XX:MaxMetaspaceSize=192m -XX:CompressedClassSpaceSize=64m", tmp),
		"ISABELLE_JAVA_OPTIONS=-Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC",
		"ISABELLE_TOOL_JAVA_OPTIONS=-Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC",
		"ISABELLE_JAVA_SYSTEM_OPTIONS=-Xms64m -Xmx1024m -Xss1m -XX:+UseSerialGC",
		"ISABELLE_TMP_PREFIX=" + tmp,
	}
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
			slog.Warn("compile sandbox workspace preparation failed", "err", err)
			return "", "", model.CompileStatusInternal, "workspace preparation failed"
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
			slog.Warn("compile sandbox workspace permission walk failed", "err", err)
			return "", "", model.CompileStatusInternal, "workspace preparation failed"
		}
		for _, dir := range security.WorkspaceScopedDirs(workDir) {
			if err := os.Chown(dir, 65532, 65532); err != nil {
				slog.Warn("compile sandbox workspace ownership failed", "err", err)
				return "", "", model.CompileStatusInternal, "workspace preparation failed"
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				slog.Warn("compile sandbox workspace chmod failed", "err", err)
				return "", "", model.CompileStatusInternal, "workspace preparation failed"
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
	isIsabelle := commandName == "isabelle"
	if isDotnetLike {
		if err := security.ResetDotnetSharedState(); err != nil {
			return "", "", model.CompileStatusInternal, "dotnet state cleanup failed: " + err.Error()
		}
	}
	// CoreCLR reserves a very large memfd-backed double-mapped region during
	// startup, so finite RLIMIT_AS values can fail before user code. Dotnet-like
	// commands get a high finite RLIMIT_FSIZE floor because lower file-size
	// rlimits can break CoreCLR/F# startup before user code.
	disableAddressSpaceLimit := isDotnetLike || commandName == "c3c" || commandName == "carbon" || commandName == "kotlinc" || isIsabelle
	allowProcessGroups := commandName == "swiftc" || commandName == "hare" || isIsabelle
	allowChmod := isDotnetLike || commandName == "gleam" || commandName == "hare" || isIsabelle
	allowExecveat := commandName == "hare"
	openFileLimit := security.OpenFileLimitForCommand(command[0])
	memoryLimitMB := compileSandboxMemoryMB
	if commandName == "kotlinc-native" {
		memoryLimitMB = 4096
	}
	if commandName == "kotlinc" || commandName == "dafny" || commandName == "isabelle" || commandName == "deno" {
		memoryLimitMB = 4096
	}
	memoryLimitKB := int64(memoryLimitMB) * 1024
	threadLimit := compileSandboxThreadLimit
	if commandName == "dafny" {
		threadLimit = 1024
	}
	helperReq := sandbox.ExecRequest{
		Command: append([]string(nil), command...),
		Dir:     workDir,
		Env:     helperEnv,
		Limits: model.Limits{
			TimeMs:         int(buildTimeout / time.Millisecond),
			MemoryMB:       memoryLimitMB,
			WorkspaceBytes: compileWorkspaceBytes,
		},
		ThreadLimit:              threadLimit,
		OpenFileLimit:            openFileLimit,
		StackLimitBytes:          security.StackLimitForCommand(command[0]),
		FileSizeLimitBytes:       security.FileSizeLimitForCommand(command[0], compileWorkspaceBytes),
		EnableNetwork:            false,
		AllowUnixSockets:         true,
		AllowSocketBind:          isIsabelle,
		AllowSocketConnect:       isIsabelle,
		AllowSocketServer:        isIsabelle,
		AllowProcesses:           true,
		AllowProcessGroups:       allowProcessGroups,
		AllowMemfdCreate:         isDotnetLike || isIsabelle,
		AllowNumaPolicy:          isDotnetLike || isIsabelle,
		AllowChmod:               allowChmod,
		AllowExecveat:            allowExecveat,
		DisableAddressSpaceLimit: disableAddressSpaceLimit,
		DisableFileSizeLimit:     false,
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
			PidsMax:         threadLimit + 32,
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
			cleanupCompileCgroup(runGroup)
			return "", "", model.CompileStatusInternal, "cgroup add process failed: " + err.Error()
		}
		defer func() {
			cleanupCompileCgroup(runGroup)
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
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid == pgid || descendants[pid] {
				continue
			}
			status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
			if err != nil {
				continue
			}
			sandboxUID := false
			for _, line := range strings.Split(string(status), "\n") {
				if !strings.HasPrefix(line, "Uid:") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[1] == "65532" {
					sandboxUID = true
				}
				break
			}
			if !sandboxUID {
				continue
			}
			cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
			if err != nil {
				continue
			}
			if cwd == workDir || strings.HasPrefix(cwd, strings.TrimRight(workDir, string(os.PathSeparator))+string(os.PathSeparator)) {
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
					switch stats.FirstLimitBreach(memoryLimitKB * 1024) {
					case cgroup.LimitBreachMemory:
						killSandbox()
						<-waitCh
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "memory limit exceeded"
					case cgroup.LimitBreachPids:
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
			if runGroup.Path != "" {
				if stats, statsErr := cgroup.ReadStats(runGroup.Path); statsErr == nil {
					switch stats.FirstLimitBreach(memoryLimitKB * 1024) {
					case cgroup.LimitBreachMemory:
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "memory limit exceeded"
					case cgroup.LimitBreachPids:
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "process limit exceeded"
					}
				}
			}
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

func cleanupCompileCgroup(group cgroup.Group) {
	if strings.TrimSpace(group.Path) == "" {
		return
	}
	if err := group.KillAndRemoveWithRetry(250 * time.Millisecond); err != nil {
		slog.Warn("compile cgroup cleanup failed", "path", group.Path, "err", err)
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
