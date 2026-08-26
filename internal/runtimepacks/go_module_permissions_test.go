package runtimepacks

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aonohako/internal/gomodulepolicy"
)

func TestDockerBuildEnablesGoModuleIsolationOnlyForGoImages(t *testing.T) {
	goBuild := (ImageSpec{
		Name:      "go-runtime",
		BaseImage: "debian:trixie-slim",
		Languages: []string{"go", "plain"},
	}).DockerBuild(".", "aonohako")
	if got := goBuild.BuildArgs["GO_MODULE_ISOLATION"]; got != "true" {
		t.Fatalf("Go module isolation arg = %q, want true", got)
	}
	if got := goBuild.BuildArgs["GO_EXTERNAL_MODULE_GID"]; got != strconv.FormatUint(uint64(gomodulepolicy.ExternalModuleGID), 10) {
		t.Fatalf("Go module GID arg = %q", got)
	}

	nonGoBuild := (ImageSpec{
		Name:      "plain-runtime",
		BaseImage: "debian:trixie-slim",
		Languages: []string{"plain"},
	}).DockerBuild(".", "aonohako")
	if got := nonGoBuild.BuildArgs["GO_MODULE_ISOLATION"]; got != "false" {
		t.Fatalf("non-Go isolation arg = %q, want false", got)
	}
}

func TestRuntimeDockerfilePreloadsAndPermissionIsolatesGoModules(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	for _, marker := range []string{
		"source=go-modules,target=/tmp/aonohako-go-modules,ro",
		"go mod download all",
		"go mod verify",
		"go test -mod=readonly ./...",
		"ARG GO_MODULE_ISOLATION=false",
		"ARG GO_EXTERNAL_MODULE_GID=65528",
		`if [[ "${GO_MODULE_ISOLATION}" == "true" ]]`,
		"/usr/local/lib/aonohako/go-modcache",
		`chown -R "0:${GO_EXTERNAL_MODULE_GID}"`,
		"-type d -exec chmod 0750",
		"-type f -exec chmod 0640",
		"AONOHAKO_GO_MODULE_ISOLATION=${GO_MODULE_ISOLATION}",
		"AONOHAKO_GO_EXTERNAL_MODULE_GID=${GO_EXTERNAL_MODULE_GID}",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile missing Go module marker %q", marker)
		}
	}
}

func TestCheckedInGoModuleCatalogIsVersionLocked(t *testing.T) {
	goModPath := filepath.Join("..", "..", "go-modules", "go.mod")
	goSumPath := filepath.Join("..", "..", "go-modules", "go.sum")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	goSum, err := os.ReadFile(goSumPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "github.com/emirpasic/gods v1.18.1") {
		t.Fatalf("Go module catalog does not pin gods v1.18.1: %s", goMod)
	}
	for _, marker := range []string{
		"github.com/emirpasic/gods v1.18.1 h1:",
		"github.com/emirpasic/gods v1.18.1/go.mod h1:",
	} {
		if !strings.Contains(string(goSum), marker) {
			t.Fatalf("Go module lock missing %q", marker)
		}
	}
}
