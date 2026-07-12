package compile

import (
	"path/filepath"
	"sort"
	"strings"

	"aonohako/internal/model"
)

func gatherByExt(sources []model.Source, exts ...string) []string {
	allowed := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		allowed[strings.ToLower(ext)] = struct{}{}
	}
	var out []string
	for _, src := range sources {
		name := strings.ToLower(src.Name)
		ext := strings.ToLower(filepath.Ext(name))
		if _, ok := allowed[ext]; ok {
			if ext == ".h" || ext == ".hpp" {
				continue
			}
			out = append(out, filepath.Clean(src.Name))
		}
	}
	return out
}

func sourcePathsByExt(workDir string, sources []model.Source, exts ...string) []string {
	allowed := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		allowed[strings.ToLower(ext)] = struct{}{}
	}
	var out []string
	for _, src := range sources {
		if _, ok := allowed[strings.ToLower(filepath.Ext(src.Name))]; ok {
			out = append(out, filepath.Join(workDir, filepath.Clean(src.Name)))
		}
	}
	sort.Strings(out)
	return out
}

func selectPrimarySource(workDir string, sources []model.Source, exts []string, preferredBases ...string) string {
	allowed := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		allowed[strings.ToLower(ext)] = struct{}{}
	}
	preferred := make(map[string]int, len(preferredBases))
	for i, base := range preferredBases {
		preferred[strings.ToLower(base)] = i + 1
	}
	bestRank := len(preferredBases) + 1
	var selected string
	for _, src := range sources {
		if _, ok := allowed[strings.ToLower(filepath.Ext(src.Name))]; !ok {
			continue
		}
		clean := filepath.Clean(src.Name)
		rank := len(preferredBases) + 1
		if value, ok := preferred[strings.ToLower(filepath.Base(clean))]; ok {
			rank = value
		}
		if selected == "" || rank < bestRank || (rank == bestRank && clean < selected) {
			selected = clean
			bestRank = rank
		}
	}
	if selected == "" {
		return ""
	}
	return filepath.Join(workDir, selected)
}
