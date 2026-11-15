package durante

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"aonohako/internal/profiles"
)

// RuntimeOptions describes the effective launch, not the names of request or
// operator presets. Program contents, stdin and random workspace names are not
// runtime options. The execution package substitutes documented placeholders
// for the program and workspace, preserving all other argv/environment values.
type RuntimeOptions struct {
	Version  uint64   `json:"version"`
	Argv     []string `json:"argv"`
	Env      []string `json:"env"`
	MemoryMB int      `json:"memory_mb"`
}

// SupportsRuntimeOptions is deliberately narrower than raw work measurement.
// Other launchers can depend on image-local discovery or additional runtime
// environment preparation and must not silently receive an incomplete binding.
func SupportsRuntimeOptions(language string) bool {
	profile, ok := profiles.Resolve(language)
	if !ok {
		return false
	}
	family, registered := FamilyForLanguage(profile.SourceLang)
	if !registered || ValidateMeasurementFamily(family) != nil {
		return false
	}
	switch profiles.NormalizeRunLang(profile.SourceLang) {
	case "binary", "go-binary", "mojo-binary", "chicken-scheme", "c3", "pony-binary", "python", "pypy", "javascript":
		return true
	default:
		return false
	}
}

func (o *RuntimeOptions) UnmarshalJSON(data []byte) error {
	var decoded RuntimeOptions
	if err := decodeTimeModelFields(data, "runtime options", map[string]any{
		"version": &decoded.Version, "argv": &decoded.Argv,
		"env": &decoded.Env, "memory_mb": &decoded.MemoryMB,
	}); err != nil {
		return err
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*o = decoded
	return nil
}

func (o RuntimeOptions) Validate() error {
	if o.Version != 1 || len(o.Argv) == 0 || o.Argv[0] == "" || len(o.Env) == 0 || o.MemoryMB <= 0 {
		return fmt.Errorf("durante runtime options requires version 1, argv, env and positive memory_mb")
	}
	for _, arg := range o.Argv {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("durante runtime options argv contains NUL")
		}
	}
	previous := ""
	for _, entry := range o.Env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || key <= previous || strings.ContainsRune(entry, '\x00') {
			return fmt.Errorf("durante runtime options env requires unique entries sorted by key")
		}
		previous = key
	}
	return nil
}

// ID hashes the fixed JSON field order above. Environment order is canonical;
// argv order remains significant. This is configuration identity, not runtime
// attestation or evidence that a model meets any accuracy/repeatability gate.
func (o RuntimeOptions) ID() (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "runtime-options-v1:sha256:" + hex.EncodeToString(sum[:]), nil
}

func (o *RuntimeOptions) clone() *RuntimeOptions {
	if o == nil {
		return nil
	}
	copy := *o
	copy.Argv, copy.Env = slices.Clone(o.Argv), slices.Clone(o.Env)
	return &copy
}
