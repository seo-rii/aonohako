package runtimepacks

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSharedInstallsExpandOnceAndPreserveDirectCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-images.yml")
	body := `
shared_installs:
  base:
    apt: [curl]
    script: ["echo shared-base"]
  bundle:
    shared: [base]
    apt: [git]
    script: ["echo shared-bundle"]
languages:
  alpha:
    install:
      shared: [bundle]
      apt: [curl]
      script: ["echo direct"]
    smoke:
      command: ["alpha", "--version"]
  beta:
    install:
      shared: [base, bundle]
      script: ["echo direct"]
    smoke:
      command: ["beta", "--version"]
profiles:
  combined:
    base_image: debian:trixie-slim
    install:
      shared: [base]
    languages: [alpha, beta]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}

	catalog, err := LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	images, err := catalog.ProductionImages()
	if err != nil {
		t.Fatalf("ProductionImages returned error: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("production image count = %d, want 1", len(images))
	}
	if !reflect.DeepEqual(images[0].AptPackages, []string{"curl", "git"}) {
		t.Fatalf("apt packages = %v, want deduplicated shared packages", images[0].AptPackages)
	}
	wantScripts := []string{"echo shared-base", "echo shared-bundle", "echo direct", "echo direct"}
	if !reflect.DeepEqual(images[0].InstallScript, wantScripts) {
		t.Fatalf("install scripts = %v, want %v", images[0].InstallScript, wantScripts)
	}
}

func TestLoadCatalogRejectsUnknownSharedInstallReferences(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "shared install",
			body: `
shared_installs:
  wrapper:
    shared: [missing]
languages:
  alpha: {}
profiles:
  combined:
    base_image: debian:trixie-slim
    languages: [alpha]
`,
			want: "shared install wrapper references unknown shared install missing",
		},
		{
			name: "language",
			body: `
languages:
  alpha:
    install:
      shared: [missing]
profiles:
  combined:
    base_image: debian:trixie-slim
    languages: [alpha]
`,
			want: "language alpha references unknown shared install missing",
		},
		{
			name: "profile",
			body: `
languages:
  alpha: {}
profiles:
  combined:
    base_image: debian:trixie-slim
    install:
      shared: [missing]
    languages: [alpha]
`,
			want: "profile combined references unknown shared install missing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime-images.yml")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("WriteFile(%q): %v", path, err)
			}
			_, err := LoadCatalog(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadCatalog error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadCatalogRejectsSharedInstallCycles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-images.yml")
	body := `
shared_installs:
  first:
    shared: [second]
  second:
    shared: [first]
languages:
  alpha:
    install:
      shared: [first]
profiles:
  combined:
    base_image: debian:trixie-slim
    languages: [alpha]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}

	_, err := LoadCatalog(path)
	if err == nil || !strings.Contains(err.Error(), "shared install references contain a cycle") {
		t.Fatalf("LoadCatalog error = %v, want shared install cycle rejection", err)
	}
}
