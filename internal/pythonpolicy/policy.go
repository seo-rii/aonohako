package pythonpolicy

import (
	"fmt"
	"strings"
)

type LibraryMode string

const (
	LibraryModeStdlib    LibraryMode = "stdlib"
	LibraryModeInstalled LibraryMode = "installed"

	ExternalLibraryGID uint32 = 65530
)

func ParseLibraryMode(raw string) (LibraryMode, error) {
	mode := LibraryMode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case LibraryModeStdlib, LibraryModeInstalled:
		return mode, nil
	default:
		return "", fmt.Errorf("must be %q or %q", LibraryModeStdlib, LibraryModeInstalled)
	}
}

func ValidateOptionalLibraryMode(mode LibraryMode) error {
	if mode == "" {
		return nil
	}
	parsed, err := ParseLibraryMode(string(mode))
	if err != nil || parsed != mode {
		return fmt.Errorf("must be %q or %q", LibraryModeStdlib, LibraryModeInstalled)
	}
	return nil
}

func EffectiveLibraryMode(mode LibraryMode) LibraryMode {
	if mode == LibraryModeInstalled {
		return LibraryModeInstalled
	}
	return LibraryModeStdlib
}
