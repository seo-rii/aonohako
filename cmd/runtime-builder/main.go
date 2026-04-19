package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"

	"aonohako/internal/runtimepacks"
)

func main() {
	var catalogPath string
	var mode string
	var tagPrefix string
	var dryRun bool
	var only string

	flag.StringVar(&catalogPath, "catalog", "runtime-images.yml", "path to runtime catalog")
	flag.StringVar(&mode, "mode", "production", "build mode: production or ci")
	flag.StringVar(&tagPrefix, "tag-prefix", "aonohako", "docker tag prefix")
	flag.BoolVar(&dryRun, "dry-run", false, "print commands without executing them")
	flag.StringVar(&only, "only", "", "optional image name filter")
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

	for _, spec := range specs {
		if only != "" && spec.Name != only {
			continue
		}
		build := spec.DockerBuild(".", tagPrefix)
		args := []string{"buildx", "build", "--load", "-f", build.File, "-t", build.Tag}
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
