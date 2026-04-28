package runtimepolicy

import (
	"strings"
	"testing"
)

func TestValidateProfileName(t *testing.T) {
	for _, name := range []string{"", "default", "low-memory", "java_1", "team.alpha"} {
		if err := ValidateProfileName(name); err != nil {
			t.Fatalf("ValidateProfileName(%q): %v", name, err)
		}
	}
	for _, name := range []string{"has space", "한글", "semi;colon", strings.Repeat("a", MaxProfileNameBytes+1)} {
		if err := ValidateProfileName(name); err == nil {
			t.Fatalf("ValidateProfileName(%q) unexpectedly succeeded", name)
		}
	}
}
