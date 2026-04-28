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

func TestValidateProblemID(t *testing.T) {
	for _, id := range []string{"", "abc1000", "contest-1/a", "tenant:problem.1", "team_2/problem-3"} {
		if err := ValidateProblemID(id); err != nil {
			t.Fatalf("ValidateProblemID(%q): %v", id, err)
		}
	}
	for _, id := range []string{"has space", "한글", "semi;colon", "../escape", strings.Repeat("a", MaxProblemIDBytes+1)} {
		if err := ValidateProblemID(id); err == nil {
			t.Fatalf("ValidateProblemID(%q) unexpectedly succeeded", id)
		}
	}
}
