package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"aonohako/internal/runtimepacks"
)

const fingerprintVersion = "aonohako-runtime-fingerprint-v1"

type sourceFingerprint struct {
	Common      string
	GoModules   string
	RustCrates  string
}

type matrixEntry struct {
	Name        string `json:"name"`
	Languages   string `json:"languages,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Changed     bool   `json:"changed"`
}

type impactOutput struct {
	Base       string        `json:"base,omitempty"`
	Production []matrixEntry `json:"production"`
	CI         []matrixEntry `json:"ci"`
}

func main() {
	var base string
	var catalogPath string
	flag.StringVar(&base, "base", "", "optional successful Git revision to compare against")
	flag.StringVar(&catalogPath, "catalog", "runtime-images.yml", "runtime catalog for the current checkout")
	flag.Parse()

	currentCatalog, err := runtimepacks.LoadCatalog(catalogPath)
	if err != nil {
		log.Fatal(err)
	}
	currentSource, err := fingerprintSource("")
	if err != nil {
		log.Fatal(err)
	}

	var baseCatalog *runtimepacks.Catalog
	var baseSource sourceFingerprint
	if base != "" {
		baseCatalog, baseSource, err = loadBaseState(base, catalogPath)
		if err != nil {
			// A missing/unparseable baseline must never cause a false-negative skip.
			fmt.Fprintf(os.Stderr, "runtime-impact: baseline %s unavailable (%v); rebuilding all runtimes\n", base, err)
			baseCatalog = nil
		}
	}

	production, err := currentCatalog.ProductionImages()
	if err != nil {
		log.Fatal(err)
	}
	ci, err := currentCatalog.CILanguageImages()
	if err != nil {
		log.Fatal(err)
	}

	out := impactOutput{
		Base:       base,
		Production: buildEntries(production, currentSource, baseCatalog, baseSource, true),
		CI:         buildEntries(ci, currentSource, baseCatalog, baseSource, false),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(out); err != nil {
		log.Fatal(err)
	}
}

func buildEntries(current []runtimepacks.ImageSpec, currentSource sourceFingerprint, baseCatalog *runtimepacks.Catalog, baseSource sourceFingerprint, production bool) []matrixEntry {
	baseSpecs := map[string]runtimepacks.ImageSpec{}
	if baseCatalog != nil {
		var specs []runtimepacks.ImageSpec
		var err error
		if production {
			specs, err = baseCatalog.ProductionImages()
		} else {
			specs, err = baseCatalog.CILanguageImages()
		}
		if err == nil {
			for _, spec := range specs {
				baseSpecs[spec.Name] = spec
			}
		} else {
			baseCatalog = nil
		}
	}

	entries := make([]matrixEntry, 0, len(current))
	for _, spec := range current {
		fingerprint := imageFingerprint(spec, currentSource)
		changed := true
		if baseCatalog != nil {
			if old, ok := baseSpecs[spec.Name]; ok {
				changed = imageFingerprint(old, baseSource) != fingerprint
			}
		}
		entries = append(entries, matrixEntry{
			Name:        spec.Name,
			Languages:   strings.Join(spec.Languages, ","),
			Fingerprint: fingerprint,
			Changed:     changed,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func imageFingerprint(spec runtimepacks.ImageSpec, source sourceFingerprint) string {
	h := sha256.New()
	writeHashPart(h, fingerprintVersion)
	specJSON, err := json.Marshal(spec)
	if err != nil {
		panic(err)
	}
	writeHashPart(h, string(specJSON))
	writeHashPart(h, source.Common)
	if slices.Contains(spec.Languages, "go") {
		writeHashPart(h, source.GoModules)
	}
	if slices.Contains(spec.Languages, "rust") {
		writeHashPart(h, source.RustCrates)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashPart(h hashWriter, value string) {
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{0})
}

func loadBaseState(base, catalogPath string) (*runtimepacks.Catalog, sourceFingerprint, error) {
	rel, err := repositoryRelativePath(catalogPath)
	if err != nil {
		return nil, sourceFingerprint{}, err
	}
	data, err := gitOutput("show", base+":"+filepath.ToSlash(rel))
	if err != nil {
		return nil, sourceFingerprint{}, fmt.Errorf("read baseline catalog: %w", err)
	}
	tmp, err := os.CreateTemp("", "aonohako-runtime-catalog-*.yml")
	if err != nil {
		return nil, sourceFingerprint{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, sourceFingerprint{}, err
	}
	if err := tmp.Close(); err != nil {
		return nil, sourceFingerprint{}, err
	}
	catalog, err := runtimepacks.LoadCatalog(tmpPath)
	if err != nil {
		return nil, sourceFingerprint{}, fmt.Errorf("parse baseline catalog: %w", err)
	}
	source, err := fingerprintSource(base)
	if err != nil {
		return nil, sourceFingerprint{}, err
	}
	return &catalog, source, nil
}

func repositoryRelativePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rootBytes, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(rootBytes))
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("catalog %q is outside the git repository", path)
	}
	return rel, nil
}

func fingerprintSource(ref string) (sourceFingerprint, error) {
	var paths []string
	if ref == "" {
		raw, err := gitOutput("ls-files", "-z")
		if err != nil {
			return sourceFingerprint{}, err
		}
		paths = splitNUL(raw)
	} else {
		raw, err := gitOutput("ls-tree", "-r", "--name-only", "-z", ref)
		if err != nil {
			return sourceFingerprint{}, err
		}
		paths = splitNUL(raw)
	}
	sort.Strings(paths)

	common := sha256.New()
	goModules := sha256.New()
	rustCrates := sha256.New()
	for _, path := range paths {
		bucket := fingerprintBucket(path)
		if bucket == "" {
			continue
		}
		var data []byte
		var err error
		if ref == "" {
			data, err = os.ReadFile(filepath.FromSlash(path))
		} else {
			data, err = gitOutput("show", ref+":"+path)
		}
		if err != nil {
			return sourceFingerprint{}, fmt.Errorf("read %s at %q: %w", path, ref, err)
		}
		var h hashWriter
		switch bucket {
		case "go":
			h = goModules
		case "rust":
			h = rustCrates
		default:
			h = common
		}
		writeHashPart(h, path)
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}
	return sourceFingerprint{
		Common:     hex.EncodeToString(common.Sum(nil)),
		GoModules:  hex.EncodeToString(goModules.Sum(nil)),
		RustCrates: hex.EncodeToString(rustCrates.Sum(nil)),
	}, nil
}

func fingerprintBucket(path string) string {
	path = filepath.ToSlash(path)
	base := filepath.Base(path)
	switch {
	case path == "runtime-images.yml":
		// The resolved ImageSpec below captures semantic catalog changes without
		// rebuilding unrelated profiles for edits elsewhere in this YAML file.
		return ""
	case strings.HasPrefix(path, ".github/"):
		return ""
	case strings.HasPrefix(path, "docs/"):
		return ""
	case strings.HasPrefix(path, "cmd/runtime-impact/"):
		return ""
	case strings.HasPrefix(path, "cmd/runtime-matrix/"):
		return ""
	case strings.HasSuffix(base, "_test.go"):
		return ""
	case base == "README.md" || base == "LICENSE" || base == "SECURITY.md" || base == "CONTRIBUTING.md":
		return ""
	case path == ".gitignore":
		return ""
	case strings.HasPrefix(path, "go-modules/"):
		return "go"
	case strings.HasPrefix(path, "rust-crates/"):
		return "rust"
	default:
		// Unknown production inputs deliberately invalidate every profile. This
		// biases the optimizer toward extra work rather than an unsafe skip.
		return "common"
	}
}

func splitNUL(data []byte) []string {
	raw := strings.Split(string(data), "\x00")
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func gitOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, err
	}
	return out, nil
}
