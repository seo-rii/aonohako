package runtimepacks

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var catalogIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const (
	ciImageNamePrefix       = "ci-"
	maxDockerTagLength      = 128
	maxCILanguageNameLength = maxDockerTagLength - len(ciImageNamePrefix)
)

type InstallSpec struct {
	Shared       []string `yaml:"shared"`
	Apt          []string `yaml:"apt"`
	Pip          []string `yaml:"pip"`
	NPM          []string `yaml:"npm"`
	Script       []string `yaml:"script"`
	SandboxTools []string `yaml:"sandbox_tools"`
}

type SmokeSpec struct {
	Command []string `yaml:"command"`
}

type LanguageSpec struct {
	Install InstallSpec `yaml:"install"`
	Smoke   SmokeSpec   `yaml:"smoke"`
}

type ProfileSpec struct {
	BaseImage string      `yaml:"base_image"`
	Languages []string    `yaml:"languages"`
	Install   InstallSpec `yaml:"install"`
}

type Catalog struct {
	SharedInstalls map[string]InstallSpec  `yaml:"shared_installs"`
	Languages      map[string]LanguageSpec `yaml:"languages"`
	Profiles       map[string]ProfileSpec  `yaml:"profiles"`
}

type ImageSpec struct {
	Name          string
	BaseImage     string
	Languages     []string
	AptPackages   []string
	PipPackages   []string
	NPMPackages   []string
	InstallScript []string
	SandboxTools  []string
	SmokeCommand  []string
}

type DockerBuildSpec struct {
	Tag           string
	File          string
	Context       string
	BuildArgs     map[string]string
	BuildContexts map[string]string
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
	if catalog.SharedInstalls == nil {
		catalog.SharedInstalls = map[string]InstallSpec{}
	}
	for sharedName, install := range catalog.SharedInstalls {
		if !catalogIdentifierPattern.MatchString(sharedName) {
			return Catalog{}, fmt.Errorf("shared install name %q is invalid", sharedName)
		}
		for _, referencedName := range install.Shared {
			if _, ok := catalog.SharedInstalls[referencedName]; !ok {
				return Catalog{}, fmt.Errorf("shared install %s references unknown shared install %s", sharedName, referencedName)
			}
		}
	}
	resolvedShared := make(map[string]bool, len(catalog.SharedInstalls))
	for len(resolvedShared) < len(catalog.SharedInstalls) {
		madeProgress := false
		for sharedName, install := range catalog.SharedInstalls {
			if resolvedShared[sharedName] {
				continue
			}
			ready := true
			for _, referencedName := range install.Shared {
				if !resolvedShared[referencedName] {
					ready = false
					break
				}
			}
			if ready {
				resolvedShared[sharedName] = true
				madeProgress = true
			}
		}
		if !madeProgress {
			return Catalog{}, fmt.Errorf("shared install references contain a cycle")
		}
	}
	for languageName, language := range catalog.Languages {
		if !catalogIdentifierPattern.MatchString(languageName) || len(languageName) > maxCILanguageNameLength {
			return Catalog{}, fmt.Errorf("language name %q is invalid", languageName)
		}
		for _, sharedName := range language.Install.Shared {
			if _, ok := catalog.SharedInstalls[sharedName]; !ok {
				return Catalog{}, fmt.Errorf("language %s references unknown shared install %s", languageName, sharedName)
			}
		}
	}
	for profileName, profile := range catalog.Profiles {
		if !catalogIdentifierPattern.MatchString(profileName) || len(profileName) > maxDockerTagLength {
			return Catalog{}, fmt.Errorf("profile name %q is invalid", profileName)
		}
		for _, sharedName := range profile.Install.Shared {
			if _, ok := catalog.SharedInstalls[sharedName]; !ok {
				return Catalog{}, fmt.Errorf("profile %s references unknown shared install %s", profileName, sharedName)
			}
		}
		seenLanguages := make(map[string]struct{}, len(profile.Languages))
		for _, lang := range profile.Languages {
			if _, duplicate := seenLanguages[lang]; duplicate {
				return Catalog{}, fmt.Errorf("profile %s contains duplicate language %s", profileName, lang)
			}
			seenLanguages[lang] = struct{}{}
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
		images = append(images, c.buildImage(name, profile, nil))
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
		images = append(images, c.buildImage(ciImageNamePrefix+lang, ProfileSpec{
			BaseImage: baseImage,
			Languages: []string{lang},
			Install:   c.Profiles[profileName].Install,
		}, c.Languages[lang].Smoke.Command))
	}
	return images, nil
}

func (c Catalog) buildImage(name string, profile ProfileSpec, smoke []string) ImageSpec {
	spec := ImageSpec{
		Name:      name,
		BaseImage: profile.BaseImage,
		Languages: slices.Clone(profile.Languages),
	}
	sort.Strings(spec.Languages)
	type pendingInstall struct {
		sharedName string
		install    InstallSpec
	}
	pending := make([]pendingInstall, 0, len(profile.Install.Shared)+len(spec.Languages)+1)
	for _, sharedName := range profile.Install.Shared {
		pending = append(pending, pendingInstall{sharedName: sharedName})
	}
	pending = append(pending, pendingInstall{install: profile.Install})
	for _, lang := range spec.Languages {
		langSpec := c.Languages[lang]
		for _, sharedName := range langSpec.Install.Shared {
			pending = append(pending, pendingInstall{sharedName: sharedName})
		}
		pending = append(pending, pendingInstall{install: langSpec.Install})
	}
	expandedShared := make(map[string]bool, len(c.SharedInstalls))
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if current.sharedName != "" {
			if expandedShared[current.sharedName] {
				continue
			}
			expandedShared[current.sharedName] = true
			sharedInstall := c.SharedInstalls[current.sharedName]
			expansion := make([]pendingInstall, 0, len(sharedInstall.Shared)+1)
			for _, sharedName := range sharedInstall.Shared {
				expansion = append(expansion, pendingInstall{sharedName: sharedName})
			}
			expansion = append(expansion, pendingInstall{install: sharedInstall})
			pending = append(expansion, pending...)
			continue
		}
		spec.AptPackages = append(spec.AptPackages, current.install.Apt...)
		spec.PipPackages = append(spec.PipPackages, current.install.Pip...)
		spec.NPMPackages = append(spec.NPMPackages, current.install.NPM...)
		spec.InstallScript = append(spec.InstallScript, current.install.Script...)
		spec.SandboxTools = append(spec.SandboxTools, current.install.SandboxTools...)
	}
	spec.AptPackages = dedupeSorted(spec.AptPackages)
	spec.PipPackages = dedupeSorted(spec.PipPackages)
	spec.NPMPackages = dedupeSorted(spec.NPMPackages)
	spec.SandboxTools = dedupeSorted(spec.SandboxTools)
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
			"IMAGE_NAME":     s.Name,
			"LANGUAGES":      strings.Join(s.Languages, ","),
			"RUNTIME_BASE":   s.BaseImage,
			"APT_PACKAGES":   strings.Join(s.AptPackages, " "),
			"PIP_PACKAGES":   strings.Join(s.PipPackages, " "),
			"NPM_PACKAGES":   strings.Join(s.NPMPackages, " "),
			"INSTALL_SCRIPT": strings.Join(s.InstallScript, "\n"),
			"SANDBOX_TOOLS":  strings.Join(s.SandboxTools, " "),
			"SMOKE_COMMAND":  strings.Join(s.SmokeCommand, "\t"),
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
