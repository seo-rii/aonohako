package execute

import (
	"fmt"
	"slices"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/security"
	"aonohako/internal/util"
)

// These names are descriptor tokens, never paths used to execute a target.
const runtimeOptionsWorkspace = "/__aonohako_workspace__"
const runtimeOptionsProgram = "/__aonohako_program__"

// sandboxBaseEnvironment is shared with the real executor. Supported descriptor
// launchers have no additional language-specific environment after this step;
// do not extend the allowlist without incorporating such overrides here.
func sandboxBaseEnvironment(ws Workspace, req *model.RunRequest) []string {
	innerEnv := append(util.BaseEnv(), security.ThreadLimitEnv()...)
	innerEnv = append(innerEnv, security.WorkspaceScopedEnv(ws.RootDir)...)
	if !req.EnableNetwork {
		innerEnv = append(innerEnv, "http_proxy=", "https_proxy=", "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*", "no_proxy=*")
	}
	if firstImagePath(req.SidecarOutputs) != "" {
		innerEnv = append(innerEnv, "IMG_CAPTURE=1")
	}
	return innerEnv
}

func resolveRuntimeTuning(req *model.RunRequest, defaults config.RuntimeTuningConfig, named map[string]config.RuntimeTuningConfig) (config.RuntimeTuningConfig, error) {
	if req.RuntimeProfile == "" {
		return defaults.WithSafeDefaults(), nil
	}
	if err := runtimepolicy.ValidateProfileName(req.RuntimeProfile); err != nil {
		return config.RuntimeTuningConfig{}, fmt.Errorf("invalid runtime_profile: %w", err)
	}
	tuning, ok := named[req.RuntimeProfile]
	if !ok {
		return config.RuntimeTuningConfig{}, fmt.Errorf("unknown runtime_profile: %s", req.RuntimeProfile)
	}
	return tuning.WithSafeDefaults(), nil
}

func cloneRuntimeTuningProfiles(profiles map[string]config.RuntimeTuningConfig) map[string]config.RuntimeTuningConfig {
	copy := make(map[string]config.RuntimeTuningConfig, len(profiles))
	for name, tuning := range profiles {
		copy[name] = tuning.WithSafeDefaults()
	}
	return copy
}

func effectiveRuntimeOptions(req *model.RunRequest, cfg durante.Config, tuning config.RuntimeTuningConfig) (durante.RuntimeOptions, error) {
	if req == nil || !durante.SupportsRuntimeOptions(req.Lang) {
		return durante.RuntimeOptions{}, fmt.Errorf("durante schema 2 runtime options does not support this launcher")
	}
	if err := pythonpolicy.ValidateOptionalLibraryMode(req.PythonLibraryMode); err != nil {
		return durante.RuntimeOptions{}, fmt.Errorf("invalid python_library_mode: %w", err)
	}
	if len(req.DisallowedCPUFeatures) != 0 {
		return durante.RuntimeOptions{}, fmt.Errorf("durante runtime options does not support disallowed_cpu_features")
	}
	// Invoke the real command builder. In the supported set, program/entry paths
	// occur only as the program operand and do not select runtime flags. The
	// submitted program, stdin and communication protocol arguments are inputs,
	// not runtime configuration, and are deliberately outside this identity.
	runLang := profiles.NormalizeRunLang(req.Lang)
	argv, env, err := cfg.MeasuredRuntime(runLang, buildCommandWithRuntimeTuning(runtimeOptionsProgram, runLang, req, tuning),
		sandboxBaseEnvironment(Workspace{RootDir: runtimeOptionsWorkspace}, req))
	if err != nil {
		return durante.RuntimeOptions{}, err
	}
	// The sandbox passes this array directly to execve. Do not invent last- or
	// first-wins semantics for duplicate keys: supported descriptors require a
	// unique environment, preserving every actual value before sorting.
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if _, duplicate := values[key]; duplicate || !found || key == "" {
			return durante.RuntimeOptions{}, fmt.Errorf("durante runtime options requires a unique environment")
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	canonicalEnv := make([]string, 0, len(keys))
	for _, key := range keys {
		canonicalEnv = append(canonicalEnv, key+"="+values[key])
	}
	options := durante.RuntimeOptions{Version: 1, Argv: argv, Env: canonicalEnv, MemoryMB: req.Limits.MemoryMB}
	return options, options.Validate()
}

func timePolicyForRequest(req *model.RunRequest, cfg durante.Config, tuning config.RuntimeTuningConfig) (durante.TimePolicy, string, error) {
	if cfg.TimeModels == nil || cfg.TimeModels.Schema != 2 {
		return cfg.TimePolicyFor(req.Lang)
	}
	options, err := effectiveRuntimeOptions(req, cfg, tuning)
	if err != nil {
		return durante.TimePolicy{}, "", err
	}
	return cfg.TimePolicyForOptions(req.Lang, options)
}

func ordinaryTimePolicy(req *model.RunRequest, cfg durante.Config, tuning config.RuntimeTuningConfig) (durante.TimePolicy, string, error) {
	if req.WorkMeasurementLimits == nil {
		return durante.TimePolicy{}, "", fmt.Errorf("durante time requires raw work measurement limits")
	}
	raw := workMeasurementRawRequest(req)
	return timePolicyForRequest(&raw, cfg, tuning)
}

func peerRuntimeRequest(req *model.RunRequest, role workMeasurementRole) (*model.RunRequest, error) {
	if req.Communication == nil {
		if role.role == "checker" && req.SPJ != nil {
			return spjRunRequest(req), nil
		}
		if role.role == "interactor" && req.Interactor != nil {
			return interactorRunRequest(req), nil
		}
		return req, nil
	}
	for _, program := range req.Programs {
		if program.ID != role.programID {
			continue
		}
		limits := req.Limits
		if role.role == "manager" {
			limits = communicationManagerLimits(req.Limits)
		} else if limits.WorkspaceBytes <= 0 || limits.WorkspaceBytes > communicationParticipantWorkspaceBytes {
			limits.WorkspaceBytes = communicationParticipantWorkspaceBytes
		}
		return communicationProgramRequest(req, program, limits), nil
	}
	return nil, fmt.Errorf("unknown work measurement role program %q", role.programID)
}

type WorkMeasurementRuntimeOptionsDescription struct {
	Role             string                 `json:"role"`
	ProgramID        string                 `json:"program_id"`
	ParticipantIndex *int                   `json:"participant_index,omitempty"`
	SourceLanguage   string                 `json:"source_language"`
	RuntimeOptions   durante.RuntimeOptions `json:"runtime_options"`
	RuntimeOptionsID string                 `json:"runtime_options_id"`
}

// DescribeWorkMeasurementRuntimeOptions is an offline operator inspection API.
// Supply a request after HTTP problem/default policies have been applied and the
// execution settings of the intended runner. No target/artifact is executed or
// materialized, and no time model or qualification is manufactured.
func DescribeWorkMeasurementRuntimeOptions(req *model.RunRequest, execution config.ExecutionConfig) ([]WorkMeasurementRuntimeOptionsDescription, error) {
	if req == nil {
		return nil, fmt.Errorf("nil request")
	}
	tuning, err := resolveRuntimeTuning(req, execution.RuntimeTuning, execution.RuntimeTuningProfiles)
	if err != nil {
		return nil, err
	}
	roles, err := workMeasurementRequestRoles(req)
	if err != nil {
		return nil, err
	}
	descriptions := make([]WorkMeasurementRuntimeOptionsDescription, 0, len(roles))
	for _, role := range roles {
		roleReq, err := peerRuntimeRequest(req, role)
		if err != nil {
			return nil, err
		}
		if req.WorkMeasurementLimits != nil {
			if req.Interactor != nil || req.Communication != nil || req.SPJ != nil {
				return nil, fmt.Errorf("peer runtime options cannot use an ordinary raw envelope")
			}
			raw := workMeasurementRawRequest(roleReq)
			roleReq = &raw
		}
		options, err := peerRuntimeOptions(role, roleReq, execution.WorkMeasurement, tuning)
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", role.role, role.programID, err)
		}
		id, err := options.ID()
		if err != nil {
			return nil, err
		}
		profile, _ := profiles.Resolve(role.language) // Supported launchers have a canonical profile.
		descriptions = append(descriptions, WorkMeasurementRuntimeOptionsDescription{
			Role: role.role, ProgramID: role.programID, ParticipantIndex: role.participantIndex,
			SourceLanguage: profile.SourceLang, RuntimeOptions: options, RuntimeOptionsID: id,
		})
	}
	return descriptions, nil
}

func optionalRuntimeTuning(tuning []config.RuntimeTuningConfig) config.RuntimeTuningConfig {
	if len(tuning) != 0 {
		return tuning[0].WithSafeDefaults()
	}
	return config.DefaultRuntimeTuningConfig()
}
