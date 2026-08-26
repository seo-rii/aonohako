package gomodulepolicy

import (
	"fmt"
	"strings"
)

type Mode string

const (
	ModeStdlib    Mode = "stdlib"
	ModeInstalled Mode = "installed"

	ExternalModuleGID uint32 = 65528
	InstalledCache           = "/usr/local/lib/aonohako/go-modcache"
)

func ParseMode(raw string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case ModeStdlib, ModeInstalled:
		return mode, nil
	default:
		return "", fmt.Errorf("must be %q or %q", ModeStdlib, ModeInstalled)
	}
}

func ValidateOptionalMode(mode Mode) error {
	if mode == "" {
		return nil
	}
	parsed, err := ParseMode(string(mode))
	if err != nil || parsed != mode {
		return fmt.Errorf("must be %q or %q", ModeStdlib, ModeInstalled)
	}
	return nil
}

func EffectiveMode(mode Mode) Mode {
	if mode == ModeInstalled {
		return ModeInstalled
	}
	return ModeStdlib
}

func UsesGoCompiler(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "go", "golang":
		return true
	default:
		return false
	}
}
