package runtimepolicy

import (
	"fmt"
	"strings"
)

const MaxProfileNameBytes = 64
const MaxProblemIDBytes = 128

func ValidateProfileName(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > MaxProfileNameBytes {
		return fmt.Errorf("runtime_profile must be at most %d bytes", MaxProfileNameBytes)
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '-', '_', '.':
			continue
		default:
			return fmt.Errorf("runtime_profile may contain only ASCII letters, digits, '-', '_', and '.'")
		}
	}
	return nil
}

func ValidateProblemID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > MaxProblemIDBytes {
		return fmt.Errorf("problem_id must be at most %d bytes", MaxProblemIDBytes)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("problem_id must not contain '..'")
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '-', '_', '.', ':', '/':
			continue
		default:
			return fmt.Errorf("problem_id may contain only ASCII letters, digits, '-', '_', '.', ':', and '/'")
		}
	}
	return nil
}
