package runtimepacks

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aonohako/internal/rustpolicy"
)

func TestDockerBuildEnablesRustCrateIsolationOnlyForRustImages(t *testing.T) {
	rustBuild := (ImageSpec{
		Name:      "rust-runtime",
		BaseImage: "debian:trixie-slim",
		Languages: []string{"rust", "plain"},
	}).DockerBuild(".", "aonohako")
	if got := rustBuild.BuildArgs["RUST_CRATE_ISOLATION"]; got != "true" {
		t.Fatalf("Rust crate isolation arg = %q, want true", got)
	}
	if got := rustBuild.BuildArgs["RUST_EXTERNAL_CRATE_GID"]; got != strconv.FormatUint(uint64(rustpolicy.ExternalCrateGID), 10) {
		t.Fatalf("Rust crate GID arg = %q", got)
	}

	nonRustBuild := (ImageSpec{
		Name:      "plain-runtime",
		BaseImage: "debian:trixie-slim",
		Languages: []string{"plain"},
	}).DockerBuild(".", "aonohako")
	if got := nonRustBuild.BuildArgs["RUST_CRATE_ISOLATION"]; got != "false" {
		t.Fatalf("non-Rust isolation arg = %q, want false", got)
	}
}

func TestRuntimeDockerfileVendorsAndPermissionIsolatesRustCrates(t *testing.T) {
	path := filepath.Join("..", "..", "docker", "runtime.Dockerfile")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	body := string(data)
	for _, marker := range []string{
		"source=rust-crates,target=/tmp/aonohako-rust-crates,ro",
		"cargo vendor --locked --versioned-dirs",
		"cargo --offline --frozen --locked",
		"ARG RUST_CRATE_ISOLATION=false",
		"ARG RUST_EXTERNAL_CRATE_GID=65529",
		`if [[ "${RUST_CRATE_ISOLATION}" == "true" ]]`,
		rustpolicy.InstalledVendorDir,
		`chown -R "0:${RUST_EXTERNAL_CRATE_GID}"`,
		"-type d -exec chmod 0750",
		"-type f -exec chmod 0640",
		"AONOHAKO_RUST_CRATE_ISOLATION=${RUST_CRATE_ISOLATION}",
		"AONOHAKO_RUST_EXTERNAL_CRATE_GID=${RUST_EXTERNAL_CRATE_GID}",
	} {
		if !strings.Contains(body, marker) {
			t.Fatalf("runtime.Dockerfile missing Rust crate marker %q", marker)
		}
	}
}

func TestCheckedInRustCrateCatalogIsVersionLocked(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "rust-crates", "Cargo.toml")
	lockPath := filepath.Join("..", "..", "rust-crates", "Cargo.lock")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `itertools = "=0.14.0"`) {
		t.Fatalf("Rust crate catalog does not exactly pin itertools 0.14.0: %s", manifest)
	}
	for _, marker := range []string{
		`name = "itertools"`,
		`version = "0.14.0"`,
		`checksum = "2b192c782037fadd9cfa75548310488aabdbf3d2da73885b31bd0abd03351285"`,
		`name = "either"`,
		`version = "1.18.0"`,
	} {
		if !strings.Contains(string(lock), marker) {
			t.Fatalf("Rust crate lock missing %q", marker)
		}
	}
}
