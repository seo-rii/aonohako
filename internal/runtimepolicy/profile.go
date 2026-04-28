package runtimepolicy

import "fmt"

const MaxProfileNameBytes = 64

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
