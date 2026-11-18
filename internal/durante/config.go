package durante

import (
	"fmt"
	"path/filepath"
	"strings"
)

const DefaultDrrun = "/opt/durante/dynamorio/bin64/drrun"
const DefaultClient = "/opt/durante/lib/libdurante.so"
const DispatchDefault = "default"
const DispatchGLIBC241 = "glibc-2.41-sse2-v1"

type Config struct {
	Families        []string
	Drrun           string
	Client          string
	DispatchProfile string
	ResourcePolicy  *ResourcePolicy
	TimePolicy      *TimePolicy
	TimeModels      *TimeModels
	// RuntimeImage is operator-supplied deployment metadata, not an attestation
	// of the executing image. It is configured independently of either time setting.
	RuntimeImage string
	// CPUIDProfile asks the client to report one fixed virtual processor to all
	// application CPUID/XGETBV, so library and runtime dispatch, and therefore
	// counts, do not follow the host processor. Empty exposes host CPUID.
	CPUIDProfile string
	// LatencyBound requests the client's version-1 block latency counter.
	LatencyBound bool
	// RuntimeDeterminism selects the option set that pins runtime-internal
	// nondeterminism of measured executions (see RuntimeDeterminismV1). Empty
	// leaves every launcher unchanged.
	RuntimeDeterminism string
	// Judge turns qualified diagnostic observations into actual verdicts. It is
	// disabled unless explicitly configured; see docs/durante-judging.md.
	Judge JudgePolicy
}

func ParseConfig(familiesValue, drrun, client, dispatch string) (Config, error) {
	cfg := Config{Drrun: strings.TrimSpace(drrun), Client: strings.TrimSpace(client), DispatchProfile: strings.TrimSpace(dispatch)}
	if strings.TrimSpace(familiesValue) != "" {
		for _, name := range strings.Split(familiesValue, ",") {
			cfg.Families = append(cfg.Families, strings.TrimSpace(name))
		}
	}
	if err := ValidateFamilies(cfg.Families); err != nil {
		return Config{}, err
	}
	if cfg.Drrun == "" {
		cfg.Drrun = DefaultDrrun
	}
	if cfg.Client == "" {
		cfg.Client = DefaultClient
	}
	if cfg.DispatchProfile == "" {
		cfg.DispatchProfile = DispatchDefault
	}
	if cfg.DispatchProfile != DispatchDefault && cfg.DispatchProfile != DispatchGLIBC241 {
		return Config{}, fmt.Errorf("unknown durante dispatch profile %q", cfg.DispatchProfile)
	}
	for _, path := range []string{cfg.Drrun, cfg.Client} {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
			return Config{}, fmt.Errorf("durante paths must be absolute")
		}
	}
	return cfg, nil
}

// ParseCPUIDProfile accepts an empty value (host CPUID visible) or a profile
// implemented by the vendored client.
func ParseCPUIDProfile(raw string) (string, error) {
	profile := strings.TrimSpace(raw)
	if profile != "" && !ValidCPUIDProfile(profile) {
		return "", fmt.Errorf("unknown durante CPUID profile %q", profile)
	}
	return profile, nil
}

func (c Config) Family(language string) (string, error) {
	family, ok := FamilyForLanguage(language)
	if !ok {
		return "", fmt.Errorf("work_measurement requires a registered source-language profile")
	}
	if err := ValidateMeasurementFamily(family); err != nil {
		return "", err
	}
	enabled := false
	for _, name := range c.Families {
		enabled = enabled || name == family
	}
	if !enabled {
		return "", fmt.Errorf("work measurement family %q is not enabled", family)
	}
	if c.DispatchProfile == DispatchGLIBC241 && family != "cpp" {
		return "", fmt.Errorf("durante dispatch profile %s is qualified only for C/C++ diagnostics", c.DispatchProfile)
	}
	return family, nil
}
