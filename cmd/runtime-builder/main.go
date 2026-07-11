package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"aonohako/internal/runtimepacks"
)

func main() {
	var catalogPath string
	var mode string
	var tagPrefix string
	var dryRun bool
	var only string
	var pythonPackagesContext string
	var push bool
	var cacheFrom string
	var cacheTo string

	flag.StringVar(&catalogPath, "catalog", "runtime-images.yml", "path to runtime catalog")
	flag.StringVar(&mode, "mode", "production", "build mode: production or ci")
	flag.StringVar(&tagPrefix, "tag-prefix", "aonohako", "docker tag prefix")
	flag.BoolVar(&dryRun, "dry-run", false, "print commands without executing them")
	flag.StringVar(&only, "only", "", "optional image name filter")
	flag.StringVar(&pythonPackagesContext, "python-packages-context", defaultPythonPackagesContext(), "optional directory copied into /usr/local/lib/aonohako/python")
	flag.BoolVar(&push, "push", false, "push image to registry instead of loading into the local docker image store")
	flag.StringVar(&cacheFrom, "cache-from", os.Getenv("AONOHAKO_DOCKER_CACHE_FROM"), "optional docker buildx cache source, for example type=gha,scope=aonohako-type-i")
	flag.StringVar(&cacheTo, "cache-to", os.Getenv("AONOHAKO_DOCKER_CACHE_TO"), "optional docker buildx cache destination, for example type=gha,mode=max,scope=aonohako-type-i")
	flag.Parse()

	catalog, err := runtimepacks.LoadCatalog(catalogPath)
	if err != nil {
		log.Fatal(err)
	}

	var specs []runtimepacks.ImageSpec
	switch mode {
	case "production":
		specs, err = catalog.ProductionImages()
	case "ci":
		specs, err = catalog.CILanguageImages()
	default:
		log.Fatalf("unsupported mode %q", mode)
	}
	if err != nil {
		log.Fatal(err)
	}
	if only != "" {
		matched := false
		for _, spec := range specs {
			if spec.Name == only {
				matched = true
				break
			}
		}
		if !matched {
			log.Fatalf("no runtime image matches -only %q", only)
		}
	}

	pythonPackagesContext, cleanup, err := resolvePythonPackagesContext(pythonPackagesContext)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	for _, spec := range specs {
		if only != "" && spec.Name != only {
			continue
		}
		build := spec.DockerBuild(".", tagPrefix)
		if build.BuildContexts == nil {
			build.BuildContexts = map[string]string{}
		}
		build.BuildContexts["aonohako-python-packages"] = pythonPackagesContext
		args := []string{"buildx", "build"}
		if push {
			args = append(args, "--push")
		} else {
			args = append(args, "--load")
		}
		if cacheFrom != "" {
			args = append(args, "--cache-from", cacheFrom)
		}
		if cacheTo != "" {
			args = append(args, "--cache-to", cacheTo)
		}
		args = append(args, "-f", build.File, "-t", build.Tag)
		contextKeys := make([]string, 0, len(build.BuildContexts))
		for key := range build.BuildContexts {
			contextKeys = append(contextKeys, key)
		}
		sort.Strings(contextKeys)
		for _, key := range contextKeys {
			args = append(args, "--build-context", fmt.Sprintf("%s=%s", key, build.BuildContexts[key]))
		}
		keys := make([]string, 0, len(build.BuildArgs))
		for key := range build.BuildArgs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			args = append(args, "--build-arg", fmt.Sprintf("%s=%s", key, build.BuildArgs[key]))
		}
		args = append(args, build.Context)

		if dryRun {
			fmt.Println("docker " + shellJoin(args))
			continue
		}

		cmd := exec.Command("docker", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatal(err)
		}
	}
}

func resolvePythonPackagesContext(path string) (string, func(), error) {
	if path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", func() {}, fmt.Errorf("python packages context %q: %w", path, err)
		}
		if !info.IsDir() {
			return "", func() {}, fmt.Errorf("python packages context %q is not a directory", path)
		}
		return path, func() {}, nil
	}

	dir, err := os.MkdirTemp("", "aonohako-python-packages-*")
	if err != nil {
		return "", func() {}, err
	}
	if err := os.WriteFile(filepath.Join(dir, ".empty"), nil, 0o644); err != nil {
		os.RemoveAll(dir)
		return "", func() {}, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func defaultPythonPackagesContext() string {
	if path := os.Getenv("AONOHAKO_PYTHON_PACKAGES_CONTEXT"); path != "" {
		return path
	}
	if info, err := os.Stat("python"); err == nil && info.IsDir() {
		return "python"
	}
	return ""
}

func shellJoin(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			out = append(out, "''")
			continue
		}
		needsQuote := false
		for _, ch := range part {
			if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\'' || ch == '"' {
				needsQuote = true
				break
			}
		}
		if !needsQuote {
			out = append(out, part)
			continue
		}
		out = append(out, "'"+replaceSingleQuotes(part)+"'")
	}
	return joinWithSpaces(out)
}

func replaceSingleQuotes(raw string) string {
	out := make([]rune, 0, len(raw))
	for _, ch := range raw {
		if ch == '\'' {
			out = append(out, '\'', '\\', '\'', '\'')
			continue
		}
		out = append(out, ch)
	}
	return string(out)
}

func joinWithSpaces(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, part := range parts[1:] {
		out += " " + part
	}
	return out
}
