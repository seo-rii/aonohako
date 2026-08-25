package rustpolicy

import (
	"fmt"
	"strings"
)

type CrateMode string

const (
	CrateModeStdlib    CrateMode = "stdlib"
	CrateModeInstalled CrateMode = "installed"

	ExternalCrateGID   uint32 = 65529
	InstalledVendorDir        = "/usr/local/lib/aonohako/rust/vendor"
)

func ParseCrateMode(raw string) (CrateMode, error) {
	mode := CrateMode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case CrateModeStdlib, CrateModeInstalled:
		return mode, nil
	default:
		return "", fmt.Errorf("must be %q or %q", CrateModeStdlib, CrateModeInstalled)
	}
}

func ValidateOptionalCrateMode(mode CrateMode) error {
	if mode == "" {
		return nil
	}
	parsed, err := ParseCrateMode(string(mode))
	if err != nil || parsed != mode {
		return fmt.Errorf("must be %q or %q", CrateModeStdlib, CrateModeInstalled)
	}
	return nil
}

func EffectiveCrateMode(mode CrateMode) CrateMode {
	if mode == CrateModeInstalled {
		return CrateModeInstalled
	}
	return CrateModeStdlib
}

func IsRustLanguage(lang string) bool {
	switch strings.ToUpper(strings.TrimSpace(lang)) {
	case "RUST", "RUST2015", "RUST2018", "RUST2021", "RUST2024":
		return true
	default:
		return false
	}
}
