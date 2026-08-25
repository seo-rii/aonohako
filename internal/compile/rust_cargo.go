package compile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/rustpolicy"
	"aonohako/internal/util"

	"github.com/pelletier/go-toml/v2"
)

type cargoMetadata struct {
	Packages []cargoMetadataPackage `json:"packages"`
}

type cargoMetadataPackage struct {
	Name         string                `json:"name"`
	Edition      string                `json:"edition"`
	ManifestPath string                `json:"manifest_path"`
	DefaultRun   *string               `json:"default_run"`
	Targets      []cargoMetadataTarget `json:"targets"`
}

type cargoMetadataTarget struct {
	Name    string   `json:"name"`
	Kind    []string `json:"kind"`
	SrcPath string   `json:"src_path"`
}

type cargoLock struct {
	Packages []struct {
		Name     string `toml:"name"`
		Source   string `toml:"source"`
		Checksum string `toml:"checksum"`
	} `toml:"package"`
}

func compileRustCargo(ctx context.Context, job CompileJob) model.CompileResponse {
	if err := validateCargoSubmission(job.WorkDir, job.Request.Sources); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	runner := job.Runner
	if runner == nil {
		runner = sandboxCommandRunnerForRustMode(job.Request.RustCrateMode)
	}

	cargoHome := filepath.Join(job.WorkDir, ".cargo-home")
	targetDir := filepath.Join(job.WorkDir, ".cargo-target")
	for _, dir := range []string{cargoHome, targetDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "Cargo workspace preparation failed: " + err.Error()}
		}
	}
	env := []string{
		"CARGO_HOME=" + cargoHome,
		"CARGO_TARGET_DIR=" + targetDir,
		"CARGO_INCREMENTAL=0",
		"CARGO_NET_OFFLINE=true",
		"CARGO_TERM_COLOR=never",
		"RUST_BACKTRACE=0",
		"CARGO_ENCODED_RUSTFLAGS=--cfg\x1fONLINE_JUDGE",
	}

	metadataArgs := cargoOfflineArgs("metadata", "--format-version=1", "--no-deps", "--manifest-path", filepath.Join(job.WorkDir, "Cargo.toml"))
	metadataResult := runner.Run(ctx, job.WorkDir, "cargo", metadataArgs, env)
	if metadataResult.Status != model.CompileStatusOK {
		return model.CompileResponse{
			Status: metadataResult.Status,
			Stdout: metadataResult.Stdout,
			Stderr: metadataResult.Stderr,
			Reason: metadataResult.Reason,
		}
	}
	rootPackage, err := parseRootCargoPackage(job.WorkDir, metadataResult.Stdout)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error(), Stderr: metadataResult.Stderr}
	}
	if rootPackage.Edition != job.Profile.RustEdition {
		return model.CompileResponse{
			Status: model.CompileStatusInvalid,
			Reason: fmt.Sprintf("Cargo.toml package edition %q does not match requested Rust edition %q", rootPackage.Edition, job.Profile.RustEdition),
		}
	}
	binName, err := selectCargoBinary(job.WorkDir, rootPackage, job.Request.EntryPoint)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: err.Error()}
	}
	if _, err := validateTargetName(binName); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInvalid, Reason: "invalid Cargo binary target: " + err.Error()}
	}

	buildArgs := cargoOfflineArgs(
		"build", "--release", "--manifest-path", filepath.Join(job.WorkDir, "Cargo.toml"),
		"--target-dir", targetDir, "--package", rootPackage.Name, "--bin", binName,
	)
	buildResult := runner.Run(ctx, job.WorkDir, "cargo", buildArgs, env)
	if buildResult.Status != model.CompileStatusOK {
		return model.CompileResponse{
			Status: buildResult.Status,
			Stdout: buildResult.Stdout,
			Stderr: metadataResult.Stderr + buildResult.Stderr,
			Reason: buildResult.Reason,
		}
	}
	// Cargo hard-links its public binary to the hashed artifact in release/deps.
	// Copy it to a fresh inode so the normal artifact reader can retain its
	// hard-link rejection for all response payloads.
	cargoArtifactRel := filepath.Join(".cargo-target", "release", binName)
	artifactRel := filepath.Join(".cargo-target", "aonohako-artifact")
	copyResult := runner.Run(ctx, job.WorkDir, "cp", []string{
		"--remove-destination",
		"--reflink=never",
		"--",
		filepath.Join(job.WorkDir, cargoArtifactRel),
		filepath.Join(job.WorkDir, artifactRel),
	}, nil)
	if copyResult.Status != model.CompileStatusOK {
		return model.CompileResponse{
			Status: copyResult.Status,
			Stdout: buildResult.Stdout + copyResult.Stdout,
			Stderr: metadataResult.Stderr + buildResult.Stderr + copyResult.Stderr,
			Reason: copyResult.Reason,
		}
	}
	artifacts, err := readSingleArtifact(job.WorkDir, artifactRel, job.Target, "exec")
	if err != nil {
		return model.CompileResponse{
			Status: model.CompileStatusInternal,
			Reason: err.Error(),
			Stdout: buildResult.Stdout + copyResult.Stdout,
			Stderr: metadataResult.Stderr + buildResult.Stderr + copyResult.Stderr,
		}
	}
	return model.CompileResponse{
		Status:    model.CompileStatusOK,
		Artifacts: artifacts,
		Stdout:    buildResult.Stdout + copyResult.Stdout,
		Stderr:    metadataResult.Stderr + buildResult.Stderr + copyResult.Stderr,
	}
}

func cargoOfflineArgs(subcommand string, args ...string) []string {
	result := []string{
		"--offline",
		"--frozen",
		"--locked",
		"--config", "net.offline=true",
		"--config", `source.crates-io.replace-with="aonohako-vendored-sources"`,
		"--config", `source.aonohako-vendored-sources.directory="` + rustpolicy.InstalledVendorDir + `"`,
		subcommand,
	}
	return append(result, args...)
}

func parseRootCargoPackage(workDir, raw string) (cargoMetadataPackage, error) {
	var metadata cargoMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return cargoMetadataPackage{}, fmt.Errorf("Cargo metadata returned invalid JSON: %w", err)
	}
	wantManifest := filepath.Clean(filepath.Join(workDir, "Cargo.toml"))
	for _, pkg := range metadata.Packages {
		if filepath.Clean(pkg.ManifestPath) == wantManifest {
			return pkg, nil
		}
	}
	return cargoMetadataPackage{}, fmt.Errorf("Cargo metadata did not describe the submitted root package")
}

func selectCargoBinary(workDir string, pkg cargoMetadataPackage, requested string) (string, error) {
	bins := make([]string, 0, len(pkg.Targets))
	for _, target := range pkg.Targets {
		if slices.Contains(target.Kind, "bin") {
			bins = append(bins, target.Name)
		}
	}
	requested = strings.TrimSpace(requested)
	if requested != "" {
		requestedPath := filepath.Clean(filepath.Join(workDir, requested))
		for _, target := range pkg.Targets {
			if !slices.Contains(target.Kind, "bin") {
				continue
			}
			if target.Name == requested || filepath.Clean(target.SrcPath) == requestedPath {
				return target.Name, nil
			}
		}
		return "", fmt.Errorf("entry_point %q is not a Cargo binary target", requested)
	}
	if pkg.DefaultRun != nil && strings.TrimSpace(*pkg.DefaultRun) != "" {
		if slices.Contains(bins, *pkg.DefaultRun) {
			return *pkg.DefaultRun, nil
		}
		return "", fmt.Errorf("Cargo.toml default-run %q is not a binary target", *pkg.DefaultRun)
	}
	if len(bins) == 1 {
		return bins[0], nil
	}
	if len(bins) == 0 {
		return "", fmt.Errorf("Cargo package does not define a binary target")
	}
	return "", fmt.Errorf("entry_point is required when Cargo package defines multiple binary targets")
}

func validateCargoSubmission(workDir string, sources []model.Source) error {
	sourcePaths := make(map[string]struct{}, len(sources))
	for _, src := range sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			return err
		}
		sourcePaths[clean] = struct{}{}
		lower := strings.ToLower(filepath.ToSlash(clean))
		if lower == ".cargo-home" || strings.HasPrefix(lower, ".cargo-home/") ||
			lower == ".cargo-target" || strings.HasPrefix(lower, ".cargo-target/") {
			return fmt.Errorf("submitted sources may not use reserved Cargo workspace paths")
		}
		if strings.HasSuffix(lower, "/.cargo/config") || strings.HasSuffix(lower, "/.cargo/config.toml") ||
			lower == ".cargo/config" || lower == ".cargo/config.toml" {
			return fmt.Errorf("submitted Cargo configuration files are not allowed")
		}
	}
	if _, ok := sourcePaths["Cargo.toml"]; !ok {
		return fmt.Errorf("installed rust_crate_mode requires a root Cargo.toml")
	}
	if _, ok := sourcePaths["Cargo.lock"]; !ok {
		return fmt.Errorf("installed rust_crate_mode requires a root Cargo.lock")
	}

	if err := validateCargoManifest(workDir, "Cargo.toml", sourcePaths); err != nil {
		return err
	}
	lockData, err := os.ReadFile(filepath.Join(workDir, "Cargo.lock"))
	if err != nil {
		return fmt.Errorf("read Cargo.lock: %w", err)
	}
	var lock cargoLock
	if err := toml.Unmarshal(lockData, &lock); err != nil {
		return fmt.Errorf("invalid Cargo.lock: %w", err)
	}
	for _, pkg := range lock.Packages {
		if pkg.Source == "" {
			continue
		}
		if pkg.Source == "registry+https://github.com/rust-lang/crates.io-index" {
			if strings.TrimSpace(pkg.Checksum) == "" {
				return fmt.Errorf("Cargo.lock registry package %q is missing a checksum", pkg.Name)
			}
			continue
		}
		return fmt.Errorf("Cargo.lock package %q uses unsupported source %q", pkg.Name, pkg.Source)
	}
	return nil
}

func validateCargoManifest(workDir, manifestRel string, sourcePaths map[string]struct{}) error {
	manifestRel = filepath.Clean(manifestRel)
	data, err := os.ReadFile(filepath.Join(workDir, manifestRel))
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.ToSlash(manifestRel), err)
	}
	var manifest map[string]any
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("invalid %s: %w", filepath.ToSlash(manifestRel), err)
	}
	if _, workspace := manifest["workspace"]; workspace {
		return fmt.Errorf("Cargo workspaces are not supported; submit one root package")
	}
	for _, forbiddenTable := range []string{"patch", "replace"} {
		if _, found := manifest[forbiddenTable]; found {
			return fmt.Errorf("%s tables are not supported in submitted Cargo manifests", forbiddenTable)
		}
	}
	packageTable, ok := cargoMap(manifest["package"])
	if !ok {
		return fmt.Errorf("%s must define a [package] table", filepath.ToSlash(manifestRel))
	}
	if _, found := packageTable["workspace"]; found {
		return fmt.Errorf("%s package.workspace is not supported", filepath.ToSlash(manifestRel))
	}
	manifestDir := filepath.Dir(manifestRel)
	if buildPath, ok := packageTable["build"].(string); ok {
		if err := validateCargoSourcePath(workDir, manifestDir, buildPath, sourcePaths); err != nil {
			return fmt.Errorf("%s package.build: %w", filepath.ToSlash(manifestRel), err)
		}
	}
	for _, key := range []string{"lib", "bin", "example", "test", "bench"} {
		if err := validateCargoTargetPaths(workDir, manifestDir, manifest[key], sourcePaths); err != nil {
			return fmt.Errorf("%s %s target: %w", filepath.ToSlash(manifestRel), key, err)
		}
	}

	dependencyTables := cargoManifestDependencyTables(manifest)
	for tableName, dependencies := range dependencyTables {
		for dependencyName, spec := range dependencies {
			specMap, structured := cargoMap(spec)
			if !structured {
				continue
			}
			for _, forbidden := range []string{"git", "branch", "tag", "rev", "registry", "registry-index"} {
				if _, found := specMap[forbidden]; found {
					return fmt.Errorf("%s %s dependency %q uses unsupported %s source configuration", filepath.ToSlash(manifestRel), tableName, dependencyName, forbidden)
				}
			}
			if _, hasPath := specMap["path"]; hasPath {
				return fmt.Errorf("%s %s dependency %q uses an unsupported path source", filepath.ToSlash(manifestRel), tableName, dependencyName)
			}
		}
	}
	return nil
}

func cargoManifestDependencyTables(manifest map[string]any) map[string]map[string]any {
	result := make(map[string]map[string]any)
	for _, key := range []string{"dependencies", "dev-dependencies", "build-dependencies"} {
		if table, ok := cargoMap(manifest[key]); ok {
			result[key] = table
		}
	}
	if targets, ok := cargoMap(manifest["target"]); ok {
		for targetName, rawTarget := range targets {
			target, ok := cargoMap(rawTarget)
			if !ok {
				continue
			}
			for _, key := range []string{"dependencies", "dev-dependencies", "build-dependencies"} {
				if table, ok := cargoMap(target[key]); ok {
					result["target."+targetName+"."+key] = table
				}
			}
		}
	}
	return result
}

func validateCargoTargetPaths(workDir, manifestDir string, raw any, sourcePaths map[string]struct{}) error {
	validateTable := func(table map[string]any) error {
		pathValue, ok := table["path"]
		if !ok {
			return nil
		}
		pathString, ok := pathValue.(string)
		if !ok {
			return fmt.Errorf("path must be a string")
		}
		return validateCargoSourcePath(workDir, manifestDir, pathString, sourcePaths)
	}
	if table, ok := cargoMap(raw); ok {
		return validateTable(table)
	}
	if tables, ok := raw.([]any); ok {
		for _, rawTable := range tables {
			table, ok := cargoMap(rawTable)
			if ok {
				if err := validateTable(table); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateCargoSourcePath(workDir, manifestDir, raw string, sourcePaths map[string]struct{}) error {
	resolved, err := resolveCargoLocalPath(workDir, manifestDir, raw)
	if err != nil {
		return err
	}
	if _, submitted := sourcePaths[resolved]; !submitted {
		return fmt.Errorf("path %q must name a submitted source", raw)
	}
	return nil
}

func resolveCargoLocalPath(workDir, manifestDir, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" || filepath.IsAbs(raw) {
		return "", fmt.Errorf("path %q must be relative and remain inside the submitted workspace", raw)
	}
	resolved := filepath.Clean(filepath.Join(manifestDir, raw))
	absRoot, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	absResolved, err := filepath.Abs(filepath.Join(workDir, resolved))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, absResolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the submitted workspace", raw)
	}
	return filepath.Clean(rel), nil
}

func cargoMap(raw any) (map[string]any, bool) {
	value, ok := raw.(map[string]any)
	return value, ok
}
