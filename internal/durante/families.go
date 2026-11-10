// Package durante integrates diagnostic software-work accounting. Its units
// are not CPU time and its in-process counters are not a security boundary.
package durante

import (
	"fmt"
	"sort"
	"strings"

	"aonohako/internal/profiles"
)

type familySpec struct {
	ID              string
	CompileKinds    []string
	SourceLanguages []string
	// RunLanguages is only consulted for names without a canonical source profile.
	// An ambiguous generic binary name must not imply C/C++ provenance.
	RunLanguages []string
}

var families = map[string]familySpec{}

func registerFamily(spec familySpec) {
	if spec.ID == "" {
		panic("empty durante family")
	}
	if _, exists := families[spec.ID]; exists {
		panic("duplicate durante family: " + spec.ID)
	}
	for _, lang := range spec.RunLanguages {
		if lang == "binary" {
			panic("durante generic binary has no source provenance")
		}
	}
	families[spec.ID] = spec
}

func RegisteredFamilies() []string {
	out := make([]string, 0, len(families))
	for name := range families {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func FamilyForLanguage(language string) (string, bool) {
	profile, canonical := profiles.Resolve(language)
	match := ""
	for name, spec := range families {
		found := false
		if canonical {
			for _, kind := range spec.CompileKinds {
				found = found || kind == profile.CompileKind
			}
			for _, source := range spec.SourceLanguages {
				found = found || source == profile.SourceLang
			}
		} else {
			for _, run := range spec.RunLanguages {
				found = found || run == strings.ToLower(strings.TrimSpace(language))
			}
		}
		if found {
			if match != "" {
				return "", false
			} // Overlapping registrations fail closed.
			match = name
		}
	}
	return match, match != ""
}

func ValidateFamilies(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if _, ok := families[name]; !ok {
			return fmt.Errorf("unknown durante family %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate durante family %q", name)
		}
		seen[name] = true
	}
	return nil
}
