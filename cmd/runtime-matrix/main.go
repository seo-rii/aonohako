package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"

	"aonohako/internal/runtimepacks"
)

func main() {
	var catalogPath string
	var mode string
	var only string

	flag.StringVar(&catalogPath, "catalog", "runtime-images.yml", "path to runtime catalog")
	flag.StringVar(&mode, "mode", "production", "matrix mode: production or ci")
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
	if only != "" {
		filtered := make([]runtimepacks.ImageSpec, 0, len(specs))
		for _, spec := range specs {
			if spec.Name == only {
				filtered = append(filtered, spec)
			}
		}
		specs = filtered
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(specs); err != nil {
		log.Fatal(err)
	}
}
