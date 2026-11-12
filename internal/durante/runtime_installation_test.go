package durante

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionRecordsActualInstallationBeforeAndAfterObservation(t *testing.T) {
	for _, changed := range []string{"", "launcher", "client", "core", "manifest"} {
		t.Run("changed-"+changed, func(t *testing.T) {
			root := t.TempDir()
			launcher := runtimeInstallationFixture(t, root)
			client := filepath.Join(root, "client.so")
			if err := os.WriteFile(client, []byte("synthetic diagnostic client"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := ParseConfig("cpp", launcher, client, DispatchDefault)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Prepare(context.Background(), root, cfg, "CPP20", os.Geteuid(), os.Getegid())
			if err != nil {
				t.Fatal(err)
			}
			writeTree(t, s.dir)
			for path, expected := range map[string]string{launcher: s.installation.LauncherSHA256, client: s.installation.ClientSHA256, filepath.Join(root, releaseCore): s.installation.CoreSHA256} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(data)
				if expected != hex.EncodeToString(digest[:]) {
					t.Fatal("identity does not hash selected artifact")
				}
			}
			if changed != "" {
				path := map[string]string{"launcher": launcher, "client": client, "core": filepath.Join(root, releaseCore), "manifest": filepath.Join(root, ".durante-runtime.json")}[changed]
				if err := os.WriteFile(path, []byte("changed after prepare"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := s.Collect(10)
			if !r.ValidDiagnosticContract() || r.Family != "cpp" || r.EngineProfile != EngineProfile || r.Installation == nil {
				t.Fatalf("missing observation identity: %+v", r)
			}
			if changed == "" {
				if !r.Complete || !r.ValidInstallationIdentity() || *r.Instructions != 300 {
					t.Fatalf("unchanged installation unavailable: %+v", r)
				}
			} else if r.Complete || r.ValidInstallationIdentity() || r.Instructions != nil || r.REPBytes != nil || r.REPTelemetryStatus != REPUnavailable || r.Reason != "durante installation changed during observation" {
				t.Fatalf("changed installation produced accepted totals: %+v", r)
			}
		})
	}
}

func runtimeInstallationFixture(t *testing.T, root string) string {
	t.Helper()
	for _, dir := range []string{"bin64", "lib64/release"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(root, "bin64/drrun")
	if err := os.WriteFile(launcher, []byte("test launcher"), 0700); err != nil {
		t.Fatal(err)
	}
	core := []byte("synthetic patched engine, never executable")
	if err := os.WriteFile(filepath.Join(root, releaseCore), core, 0644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(core)
	manifest := map[string]any{"schema": 1, "inputs": map[string]any{"source_commit": runtimeSourceCommit, "source_sha256": runtimeSourceSHA256, "patch_sha256": runtimePatchSHA256, "recipe": 1, "profiles": map[string]string{"release": "Release", "debug": "Debug"}, "cmake_options": []string{}}, "artifacts": map[string]any{releaseCore: hex.EncodeToString(digest[:]), "lib64/debug/libdynamorio.so": strings.Repeat("a", 64), "lib64/release/libdynamorio.so.debug": nil, "lib64/debug/libdynamorio.so.debug": nil}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".durante-runtime.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	return launcher
}

func TestRuntimeInstallationChecksReviewedInputsAndActualCore(t *testing.T) {
	dir := t.TempDir()
	launcher := runtimeInstallationFixture(t, dir)
	if _, err := verifyRuntimeInstallation(launcher); err != nil {
		t.Fatal(err)
	}
	// Optional debug profiles are irrelevant to this fixed release invocation.
	if err := os.WriteFile(filepath.Join(dir, releaseCore), []byte("different engine"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRuntimeInstallation(launcher); err == nil {
		t.Fatal("changed release engine accepted")
	}
}

func TestRuntimeInstallationRejectsUnsafeOrUnknownProvenance(t *testing.T) {
	for name, edit := range map[string]func(*testing.T, string){
		"missing marker": func(t *testing.T, r string) { os.Remove(filepath.Join(r, ".durante-runtime.json")) },
		"malformed": func(t *testing.T, r string) {
			os.WriteFile(filepath.Join(r, ".durante-runtime.json"), []byte("{"), 0644)
		},
		"oversize": func(t *testing.T, r string) {
			os.WriteFile(filepath.Join(r, ".durante-runtime.json"), []byte(strings.Repeat(" ", 65537)), 0644)
		},
		"wrong source":     func(t *testing.T, r string) { replaceRuntimeMarker(t, r, runtimeSourceCommit, strings.Repeat("a", 40)) },
		"wrong archive":    func(t *testing.T, r string) { replaceRuntimeMarker(t, r, runtimeSourceSHA256, strings.Repeat("b", 64)) },
		"wrong patch":      func(t *testing.T, r string) { replaceRuntimeMarker(t, r, runtimePatchSHA256, strings.Repeat("c", 64)) },
		"wrong schema":     func(t *testing.T, r string) { replaceRuntimeMarker(t, r, `"schema":1`, `"schema":2`) },
		"duplicate schema": func(t *testing.T, r string) { replaceRuntimeMarker(t, r, `"schema":1`, `"schema":2,"schema":1`) },
		"duplicate patch": func(t *testing.T, r string) {
			replaceRuntimeMarker(t, r, `"patch_sha256":"`+runtimePatchSHA256+`"`, `"patch_sha256":"stock","patch_sha256":"`+runtimePatchSHA256+`"`)
		},
		"stock core": func(t *testing.T, r string) {
			data, _ := os.ReadFile(filepath.Join(r, releaseCore))
			d := sha256.Sum256(data)
			replaceRuntimeMarker(t, r, hex.EncodeToString(d[:]), stockReleaseSHA256)
		},
		"writable marker":            func(t *testing.T, r string) { os.Chmod(filepath.Join(r, ".durante-runtime.json"), 0666) },
		"writable core":              func(t *testing.T, r string) { os.Chmod(filepath.Join(r, releaseCore), 0666) },
		"writable profile directory": func(t *testing.T, r string) { os.Chmod(filepath.Join(r, "lib64/release"), 0777) },
		"symlink core": func(t *testing.T, r string) {
			p := filepath.Join(r, releaseCore)
			os.Rename(p, p+".real")
			os.Symlink(p+".real", p)
		},
		"symlink marker": func(t *testing.T, r string) {
			p := filepath.Join(r, ".durante-runtime.json")
			os.Rename(p, p+".real")
			os.Symlink(p+".real", p)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := t.TempDir()
			launcher := runtimeInstallationFixture(t, r)
			edit(t, r)
			if _, err := verifyRuntimeInstallation(launcher); err == nil {
				t.Fatal("unsafe installation accepted")
			}
		})
	}
}

func replaceRuntimeMarker(t *testing.T, root, old, new string) {
	t.Helper()
	path := filepath.Join(root, ".durante-runtime.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatal("fixture replacement target absent")
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), old, new)), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationIdentityRejectsAmbiguousJSONAndDigestAliases(t *testing.T) {
	identity := InstallationIdentity{SourceCommit: sourceCommit, RuntimeSourceCommit: runtimeSourceCommit,
		RuntimePatchSHA256: runtimePatchSHA256, LauncherSHA256: strings.Repeat("a", 64),
		ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true}
	valid := Report{Installation: &identity}
	if !valid.ValidInstallationIdentity() {
		t.Fatal("valid identity rejected")
	}
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		t.Run(key, func(t *testing.T) {
			prefix := string(data[:len(data)-1])
			for _, changed := range []string{
				prefix + `,"` + key + `":` + string(value) + `}`,
				prefix + `,"` + strings.ToUpper(key) + `":` + string(value) + `}`,
				strings.Replace(string(data), `"`+key+`":`+string(value), `"`+strings.ToUpper(key)+`":`+string(value), 1),
				strings.Replace(string(data), `"`+key+`":`+string(value), `"`+key+`":null`, 1),
			} {
				var decoded InstallationIdentity
				if err := json.Unmarshal([]byte(changed), &decoded); err == nil {
					t.Fatal("ambiguous or missing provenance field accepted", changed)
				}
			}
		})
	}
	for name, edit := range map[string]func(*InstallationIdentity){
		"uppercase launcher":   func(i *InstallationIdentity) { i.LauncherSHA256 = strings.ToUpper(i.LauncherSHA256) },
		"uppercase client":     func(i *InstallationIdentity) { i.ClientSHA256 = strings.ToUpper(i.ClientSHA256) },
		"uppercase core":       func(i *InstallationIdentity) { i.CoreSHA256 = strings.ToUpper(i.CoreSHA256) },
		"uppercase stock core": func(i *InstallationIdentity) { i.CoreSHA256 = strings.ToUpper(stockReleaseSHA256) },
		"stock core":           func(i *InstallationIdentity) { i.CoreSHA256 = stockReleaseSHA256 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := identity
			edit(&copy)
			if (&Report{Installation: &copy}).ValidInstallationIdentity() {
				t.Fatal("noncanonical or stock digest accepted")
			}
		})
	}
	var roundTrip InstallationIdentity
	if err := json.Unmarshal(data, &roundTrip); err != nil || roundTrip != identity {
		t.Fatal("valid identity did not round-trip", err)
	}
	wrapped := `{"installation":` + string(data) + `,"INSTALLATION":` + string(data) + `}`
	var report Report
	if err := json.Unmarshal([]byte(wrapped), &report); err == nil {
		t.Fatal("top-level identity alias accepted")
	}
}

func TestRuntimeManifestRejectsUppercaseActualCoreDigest(t *testing.T) {
	root := t.TempDir()
	launcher := runtimeInstallationFixture(t, root)
	core, err := os.ReadFile(filepath.Join(root, releaseCore))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(core)
	lower := hex.EncodeToString(digest[:])
	replaceRuntimeMarker(t, root, lower, strings.ToUpper(lower))
	if _, err := verifyRuntimeInstallation(launcher); err == nil {
		t.Fatal("uppercase digest matching actual bytes bypassed canonical manifest contract")
	}
}
