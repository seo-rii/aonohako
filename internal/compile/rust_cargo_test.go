package compile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/rustpolicy"
)

const validCargoManifest = `[package]
name = "submission"
version = "0.1.0"
edition = "2021"

[dependencies]
itertools = "0.14.0"
`

const validCargoLock = `version = 4

[[package]]
name = "either"
version = "1.15.0"
source = "registry+https://github.com/rust-lang/crates.io-index"
checksum = "0123456789abcdef"

[[package]]
name = "itertools"
version = "0.14.0"
source = "registry+https://github.com/rust-lang/crates.io-index"
checksum = "abcdef0123456789"
dependencies = ["either"]

[[package]]
name = "submission"
version = "0.1.0"
dependencies = ["itertools"]
`

func writeCargoSubmissionFile(t *testing.T, workDir, name, contents string) {
	t.Helper()
	path := filepath.Join(workDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func validCargoSources() []model.Source {
	return []model.Source{{Name: "Cargo.toml"}, {Name: "Cargo.lock"}, {Name: "src/main.rs"}}
}

func writeValidCargoSubmission(t *testing.T, workDir string) {
	t.Helper()
	writeCargoSubmissionFile(t, workDir, "Cargo.toml", validCargoManifest)
	writeCargoSubmissionFile(t, workDir, "Cargo.lock", validCargoLock)
	writeCargoSubmissionFile(t, workDir, "src/main.rs", "fn main() {}\n")
}

func TestValidateCargoSubmissionAcceptsLockedCratesIOPackage(t *testing.T) {
	workDir := t.TempDir()
	writeValidCargoSubmission(t, workDir)
	if err := validateCargoSubmission(workDir, validCargoSources()); err != nil {
		t.Fatalf("validateCargoSubmission(): %v", err)
	}
}

func TestValidateCargoSubmissionRejectsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		lock     string
		extra    []model.Source
		want     string
	}{
		{name: "workspace", manifest: validCargoManifest + "\n[workspace]\nmembers = []\n", lock: validCargoLock, want: "workspaces are not supported"},
		{name: "patch", manifest: validCargoManifest + "\n[patch.crates-io]\nitertools = { version = \"0.14.0\" }\n", lock: validCargoLock, want: "patch tables"},
		{name: "replace", manifest: validCargoManifest + "\n[replace]\n\"itertools:0.14.0\" = { version = \"0.14.0\" }\n", lock: validCargoLock, want: "replace tables"},
		{name: "package workspace", manifest: strings.Replace(validCargoManifest, "edition = \"2021\"", "edition = \"2021\"\nworkspace = \"..\"", 1), lock: validCargoLock, want: "package.workspace"},
		{name: "git dependency", manifest: strings.Replace(validCargoManifest, "itertools = \"0.14.0\"", "itertools = { git = \"https://example.invalid/repo\" }", 1), lock: validCargoLock, want: "unsupported git"},
		{name: "alternate registry", manifest: strings.Replace(validCargoManifest, "itertools = \"0.14.0\"", "itertools = { version = \"0.14.0\", registry = \"private\" }", 1), lock: validCargoLock, want: "unsupported registry"},
		{name: "path dependency", manifest: strings.Replace(validCargoManifest, "itertools = \"0.14.0\"", "itertools = { path = \"vendor/itertools\" }", 1), lock: validCargoLock, want: "unsupported path source"},
		{name: "escaping build script", manifest: strings.Replace(validCargoManifest, "edition = \"2021\"", "edition = \"2021\"\nbuild = \"../build.rs\"", 1), lock: validCargoLock, want: "escapes the submitted workspace"},
		{name: "missing target source", manifest: validCargoManifest + "\n[[bin]]\nname = \"solver\"\npath = \"src/solver.rs\"\n", lock: validCargoLock, want: "must name a submitted source"},
		{name: "git lock source", manifest: validCargoManifest, lock: strings.Replace(validCargoLock, "registry+https://github.com/rust-lang/crates.io-index", "git+https://example.invalid/repo", 1), want: "unsupported source"},
		{name: "missing registry checksum", manifest: validCargoManifest, lock: strings.Replace(validCargoLock, "checksum = \"0123456789abcdef\"\n", "", 1), want: "missing a checksum"},
		{name: "submitted cargo config", manifest: validCargoManifest, lock: validCargoLock, extra: []model.Source{{Name: "nested/.cargo/config.toml"}}, want: "configuration files are not allowed"},
		{name: "submitted cargo home", manifest: validCargoManifest, lock: validCargoLock, extra: []model.Source{{Name: ".cargo-home/config.toml"}}, want: "reserved Cargo workspace paths"},
		{name: "submitted cargo target", manifest: validCargoManifest, lock: validCargoLock, extra: []model.Source{{Name: ".cargo-target/release/submission"}}, want: "reserved Cargo workspace paths"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			writeCargoSubmissionFile(t, workDir, "Cargo.toml", tc.manifest)
			writeCargoSubmissionFile(t, workDir, "Cargo.lock", tc.lock)
			writeCargoSubmissionFile(t, workDir, "src/main.rs", "fn main() {}\n")
			sources := append(validCargoSources(), tc.extra...)
			for _, source := range tc.extra {
				writeCargoSubmissionFile(t, workDir, source.Name, "[net]\noffline = false\n")
			}
			err := validateCargoSubmission(workDir, sources)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateCargoSubmissionRequiresRootManifestAndLock(t *testing.T) {
	workDir := t.TempDir()
	writeCargoSubmissionFile(t, workDir, "src/main.rs", "fn main() {}\n")
	if err := validateCargoSubmission(workDir, []model.Source{{Name: "src/main.rs"}}); err == nil || !strings.Contains(err.Error(), "root Cargo.toml") {
		t.Fatalf("missing manifest error = %v", err)
	}
	writeCargoSubmissionFile(t, workDir, "Cargo.toml", validCargoManifest)
	if err := validateCargoSubmission(workDir, []model.Source{{Name: "Cargo.toml"}, {Name: "src/main.rs"}}); err == nil || !strings.Contains(err.Error(), "root Cargo.lock") {
		t.Fatalf("missing lock error = %v", err)
	}
}

type rustCargoSequenceRunner struct {
	commands []recordedCommand
	metadata string
	calls    int
}

func (r *rustCargoSequenceRunner) Run(_ context.Context, workDir, bin string, args, env []string) CommandResult {
	r.commands = append(r.commands, recordedCommand{workDir: workDir, bin: bin, args: slices.Clone(args), env: slices.Clone(env)})
	r.calls++
	if r.calls == 1 {
		return CommandResult{Status: model.CompileStatusOK, Stdout: r.metadata}
	}
	artifact := filepath.Join(workDir, ".cargo-target", "release", "submission")
	hashedArtifact := filepath.Join(workDir, ".cargo-target", "release", "deps", "submission-deadbeef")
	if err := os.MkdirAll(filepath.Dir(hashedArtifact), 0o755); err != nil {
		return CommandResult{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	if err := os.WriteFile(hashedArtifact, []byte("binary"), 0o755); err != nil {
		return CommandResult{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	if err := os.Link(hashedArtifact, artifact); err != nil {
		return CommandResult{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	return CommandResult{Status: model.CompileStatusOK}
}

func TestRustCargoCompilerUsesLockedOfflineVendor(t *testing.T) {
	workDir := t.TempDir()
	writeValidCargoSubmission(t, workDir)
	metadata, err := json.Marshal(cargoMetadata{Packages: []cargoMetadataPackage{{
		Name:         "submission",
		Edition:      "2021",
		ManifestPath: filepath.Join(workDir, "Cargo.toml"),
		Targets:      []cargoMetadataTarget{{Name: "submission", Kind: []string{"bin"}, SrcPath: filepath.Join(workDir, "src/main.rs")}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	runner := &rustCargoSequenceRunner{metadata: string(metadata)}
	resp := compileRustCargo(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Profile: profiles.Profile{RustEdition: "2021"},
		Request: &model.CompileRequest{
			EntryPoint:    "src/main.rs",
			RustCrateMode: rustpolicy.CrateModeInstalled,
			Sources:       validCargoSources(),
		},
		Runner: runner,
	})
	if resp.Status != model.CompileStatusOK || len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" {
		t.Fatalf("response = %+v", resp)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("commands = %+v", runner.commands)
	}
	for _, command := range runner.commands {
		if command.bin != "cargo" {
			t.Fatalf("command = %+v", command)
		}
		for _, want := range []string{"--offline", "--frozen", "--locked", `source.aonohako-vendored-sources.directory="` + rustpolicy.InstalledVendorDir + `"`} {
			if !slices.Contains(command.args, want) {
				t.Fatalf("Cargo args missing %q: %v", want, command.args)
			}
		}
		for _, want := range []string{
			"CARGO_HOME=" + filepath.Join(workDir, ".cargo-home"),
			"CARGO_TARGET_DIR=" + filepath.Join(workDir, ".cargo-target"),
			"CARGO_NET_OFFLINE=true",
		} {
			if !slices.Contains(command.env, want) {
				t.Fatalf("Cargo env missing %q: %v", want, command.env)
			}
		}
	}
	if !slices.Contains(runner.commands[0].args, "metadata") || !slices.Contains(runner.commands[1].args, "build") {
		t.Fatalf("unexpected Cargo commands: %+v", runner.commands)
	}
}

func TestSelectCargoBinarySupportsTargetNameSourceAndDefault(t *testing.T) {
	workDir := "/work/submission"
	defaultRun := "secondary"
	pkg := cargoMetadataPackage{
		DefaultRun: &defaultRun,
		Targets: []cargoMetadataTarget{
			{Name: "primary", Kind: []string{"bin"}, SrcPath: filepath.Join(workDir, "src/main.rs")},
			{Name: "secondary", Kind: []string{"bin"}, SrcPath: filepath.Join(workDir, "src/bin/secondary.rs")},
		},
	}
	for _, tc := range []struct {
		requested string
		want      string
	}{{"primary", "primary"}, {"src/bin/secondary.rs", "secondary"}, {"", "secondary"}} {
		got, err := selectCargoBinary(workDir, pkg, tc.requested)
		if err != nil || got != tc.want {
			t.Fatalf("selectCargoBinary(%q) = %q, %v; want %q", tc.requested, got, err, tc.want)
		}
	}
}
