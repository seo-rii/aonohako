package workmeter

import (
	"fmt"
	"path/filepath"
	"strings"
)

const DefaultDrrun = "/opt/workmeter/dynamorio/bin64/drrun"
const DefaultClient = "/opt/workmeter/lib/libworkmeter.so"
const DispatchDefault = "default"
const DispatchGLIBC241 = "glibc-2.41-sse2-v1"

type Config struct {
	Families        []string
	Drrun           string
	Client          string
	DispatchProfile string
	ResourcePolicy  *ResourcePolicy
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
		return Config{}, fmt.Errorf("unknown workmeter dispatch profile %q", cfg.DispatchProfile)
	}
	for _, path := range []string{cfg.Drrun, cfg.Client} {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
			return Config{}, fmt.Errorf("workmeter paths must be absolute")
		}
	}
	return cfg, nil
}

func (c Config) Family(language string) (string, error) {
	family, ok := FamilyForLanguage(language)
	if !ok {
		return "", fmt.Errorf("work_measurement requires a registered source-language profile")
	}
	enabled := false
	for _, name := range c.Families {
		enabled = enabled || name == family
	}
	if !enabled {
		return "", fmt.Errorf("work measurement family %q is not enabled", family)
	}
	if c.DispatchProfile == DispatchGLIBC241 && family != "cpp" {
		return "", fmt.Errorf("workmeter dispatch profile %s is qualified only for C/C++ diagnostics", c.DispatchProfile)
	}
	return family, nil
}
