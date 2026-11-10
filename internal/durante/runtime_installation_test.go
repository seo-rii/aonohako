package durante

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if err := verifyRuntimeInstallation(launcher); err != nil {
		t.Fatal(err)
	}
	// Optional debug profiles are irrelevant to this fixed release invocation.
	if err := os.WriteFile(filepath.Join(dir, releaseCore), []byte("different engine"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := verifyRuntimeInstallation(launcher); err == nil {
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
			if err := verifyRuntimeInstallation(launcher); err == nil {
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
