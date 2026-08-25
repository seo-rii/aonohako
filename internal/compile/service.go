package compile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/gomodulepolicy"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/security"
	"aonohako/internal/util"
)

const buildTimeout = 60 * time.Second

const (
	MaxDecodedSourceBytes      = 16 << 20
	MaxDecodedSourceTotalBytes = 48 << 20
	MaxSourceFiles             = 512
	OutputCaptureBytes         = 1 << 20

	maxDecodedSourceBytes      = MaxDecodedSourceBytes
	maxDecodedSourceTotalBytes = MaxDecodedSourceTotalBytes
	maxArtifactBytes           = 16 << 20
	maxArtifactTotalBytes      = 48 << 20
	maxSourceFiles             = MaxSourceFiles
	ocamlCompileRunParam       = "s=32k"
	compileSandboxMemoryMB     = 2048
	compileSandboxThreadLimit  = 256
	compileWorkspaceBytes      = 512 << 20
	compileOutputCaptureBytes  = OutputCaptureBytes
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
	sourcePaths := make(map[string]struct{}, len(req.Sources))
	for i, src := range req.Sources {
		cleanSource, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: fmt.Sprintf("sources[%d].name: %s", i, err.Error())}
		}
		if _, exists := sourcePaths[cleanSource]; exists {
			return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "duplicate source path: " + cleanSource}
		}
		sourcePaths[cleanSource] = struct{}{}
	}
	profile, ok := resolveProfile(req.Lang)
	if !ok {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "unsupported lang: " + req.Lang}
	}
	if err := gomodulepolicy.ValidateOptionalMode(req.GoModuleMode); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "invalid go_module_mode: " + err.Error()}
	}
	if req.GoModuleMode != "" && profile.CompileKind != "go" {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "go_module_mode requires a Go compile request"}
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
		_, found := sourcePaths[cleanEntryPoint]
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

	return capCompileResponseOutput(executeBuild(ctx, workDir, profile, target, req, tuning), workDir)
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

func executeBuild(ctx context.Context, workDir string, profile profiles.Profile, target string, req *model.CompileRequest, tuning config.RuntimeTuningConfig) model.CompileResponse {
	if compiler, ok := lookupCompiler(profile.CompileKind); ok {
		runner := sandboxCommandRunner{}
		if profile.CompileKind == "go" {
			runner = sandboxCommandRunnerForGoMode(req.GoModuleMode)
		}
		return compiler.Compile(ctx, CompileJob{
			WorkDir: workDir,
			Target:  target,
			Profile: profile,
			Request: req,
			Tuning:  tuning,
			Runner:  runner,
		})
	}
	return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "unsupported compile kind: " + profile.CompileKind}
}
