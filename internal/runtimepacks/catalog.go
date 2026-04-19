package runtimepacks

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type InstallSpec struct {
	Apt []string `yaml:"apt"`
	Pip []string `yaml:"pip"`
	NPM []string `yaml:"npm"`
}

type SmokeSpec struct {
	Command []string `yaml:"command"`
}

type LanguageSpec struct {
	Install InstallSpec `yaml:"install"`
	Smoke   SmokeSpec   `yaml:"smoke"`
}

type ProfileSpec struct {
	BaseImage string   `yaml:"base_image"`
	Languages []string `yaml:"languages"`
}

type Catalog struct {
	Languages map[string]LanguageSpec `yaml:"languages"`
	Profiles  map[string]ProfileSpec  `yaml:"profiles"`
}

type ImageSpec struct {
	Name         string
	BaseImage    string
	Languages    []string
	AptPackages  []string
	PipPackages  []string
	NPMPackages  []string
	SmokeCommand []string
}

type DockerBuildSpec struct {
	Tag       string
	File      string
	Context   string
	BuildArgs map[string]string
}

func LoadCatalog(path string) (Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, err
	}
	var catalog Catalog
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		return Catalog{}, err
	}
	if catalog.Languages == nil {
		catalog.Languages = map[string]LanguageSpec{}
	}
	if catalog.Profiles == nil {
		catalog.Profiles = map[string]ProfileSpec{}
	}
	for profileName, profile := range catalog.Profiles {
		for _, lang := range profile.Languages {
			if _, ok := catalog.Languages[lang]; !ok {
				return Catalog{}, fmt.Errorf("profile %s references unknown language %s", profileName, lang)
			}
		}
	}
	return catalog, nil
}

func (c Catalog) ProductionImages() ([]ImageSpec, error) {
	names := sortedKeys(c.Profiles)
	images := make([]ImageSpec, 0, len(names))
	for _, name := range names {
		profile := c.Profiles[name]
		images = append(images, c.buildImage(name, profile.BaseImage, profile.Languages, nil))
	}
	return images, nil
}

func (c Catalog) CILanguageImages() ([]ImageSpec, error) {
	languages := sortedKeys(c.Languages)
	images := make([]ImageSpec, 0, len(languages))
	for _, lang := range languages {
		profileName, baseImage := c.profileForLanguage(lang)
		if profileName == "" {
			return nil, fmt.Errorf("language %s is not assigned to any profile", lang)
		}
		images = append(images, c.buildImage("ci-"+lang, baseImage, []string{lang}, c.Languages[lang].Smoke.Command))
	}
	return images, nil
}

func (c Catalog) buildImage(name, baseImage string, languages []string, smoke []string) ImageSpec {
	spec := ImageSpec{
		Name:      name,
		BaseImage: baseImage,
		Languages: slices.Clone(languages),
	}
	sort.Strings(spec.Languages)
	for _, lang := range spec.Languages {
		langSpec := c.Languages[lang]
		spec.AptPackages = append(spec.AptPackages, langSpec.Install.Apt...)
		spec.PipPackages = append(spec.PipPackages, langSpec.Install.Pip...)
		spec.NPMPackages = append(spec.NPMPackages, langSpec.Install.NPM...)
	}
	spec.AptPackages = dedupeSorted(spec.AptPackages)
	spec.PipPackages = dedupeSorted(spec.PipPackages)
	spec.NPMPackages = dedupeSorted(spec.NPMPackages)
	if len(smoke) > 0 {
		spec.SmokeCommand = slices.Clone(smoke)
	}
	return spec
}

func (s ImageSpec) DockerBuild(contextDir, tagPrefix string) DockerBuildSpec {
	return DockerBuildSpec{
		Tag:     strings.TrimRight(tagPrefix, ":") + ":" + s.Name,
		File:    filepath.ToSlash(filepath.Join("docker", "runtime.Dockerfile")),
		Context: contextDir,
		BuildArgs: map[string]string{
			"IMAGE_NAME":    s.Name,
			"LANGUAGES":     strings.Join(s.Languages, ","),
			"RUNTIME_BASE":  s.BaseImage,
			"APT_PACKAGES":  strings.Join(s.AptPackages, " "),
			"PIP_PACKAGES":  strings.Join(s.PipPackages, " "),
			"NPM_PACKAGES":  strings.Join(s.NPMPackages, " "),
			"SMOKE_COMMAND": strings.Join(s.SmokeCommand, "\t"),
		},
	}
}

func (c Catalog) profileForLanguage(language string) (string, string) {
	for _, name := range sortedKeys(c.Profiles) {
		profile := c.Profiles[name]
		for _, lang := range profile.Languages {
			if lang == language {
				return name, profile.BaseImage
			}
		}
	}
	return "", ""
}

func dedupeSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
