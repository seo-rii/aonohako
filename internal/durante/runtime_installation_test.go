package durante

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
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
			client := runtimeClientFixture(t, root)
			cfg, err := ParseConfig("cpp", launcher, client, DispatchDefault)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Prepare(context.Background(), root, cfg, "CPP20", os.Geteuid(), os.Getegid())
			if err != nil {
				t.Fatal(err)
			}
			if s.installation.SourceCommit != SourceCommit || s.installation.RuntimePatchSHA256 != runtimePatchSHA256 || s.installation.RuntimeCodeReplayPatchSHA256 != runtimeCodeReplayPatchSHA256 {
				t.Fatal("installation does not identify all reviewed runtime inputs")
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

func runtimeClientFixture(t *testing.T, root string) string {
	t.Helper()
	var markers []string
	if EngineProfile != engineProfileV7 {
		markers = []string{SourceCommit}
	}
	path := filepath.Join(root, "client.so")
	if err := os.WriteFile(path, clientELF(t, markers...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Construct a shared object containing only the section names and any requested
// source markers. This exercises the same ELF parser without a compiler dependency.
func clientELF(t *testing.T, markers ...string) []byte {
	t.Helper()
	const names = "\x00.shstrtab\x00.durante.source\x00"
	sections := make([]elf.Section64, 2+len(markers))
	var header elf.Header64
	copy(header.Ident[:], "\x7fELF\x02\x01\x01")
	header.Type, header.Machine, header.Version = uint16(elf.ET_DYN), uint16(elf.EM_X86_64), uint32(elf.EV_CURRENT)
	header.Ehsize, header.Shentsize = uint16(binary.Size(header)), uint16(binary.Size(sections[0]))
	header.Shoff, header.Shnum, header.Shstrndx = uint64(header.Ehsize), uint16(len(sections)), 1
	start := header.Shoff + uint64(binary.Size(sections))
	sections[1] = elf.Section64{Name: 1, Type: uint32(elf.SHT_STRTAB), Off: start, Size: uint64(len(names))}
	payload := []byte(names)
	for i, marker := range markers {
		sections[i+2] = elf.Section64{Name: uint32(strings.Index(names, ".durante.source")), Type: uint32(elf.SHT_PROGBITS),
			Off: start + uint64(len(payload)), Size: uint64(len(marker))}
		payload = append(payload, marker...)
	}
	var buf bytes.Buffer
	for _, field := range []any{header, sections, payload} {
		if err := binary.Write(&buf, binary.LittleEndian, field); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestClientSourceMustMatchWorker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		markers []string
		valid   bool
	}{
		{"unmarked v7 package", nil, EngineProfile == engineProfileV7},
		{"v8 package", []string{sourceCommitV8}, EngineProfile == engineProfileV8},
		{"v9 package", []string{sourceCommitV9}, EngineProfile == engineProfileV9},
		{"v10 package", []string{sourceCommitV10}, EngineProfile == engineProfileV10},
		{"v11 package", []string{sourceCommitV11}, EngineProfile == engineProfileV11},
		{"unknown source", []string{strings.Repeat("a", 40)}, false},
		{"empty marker", []string{""}, false},
		{"trailing newline", []string{SourceCommit + "\n"}, false},
		{"duplicate marker", []string{SourceCommit, SourceCommit}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			launcher := runtimeInstallationFixture(t, root)
			client := filepath.Join(root, "custom-client.so")
			if err := os.WriteFile(client, clientELF(t, tc.markers...), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := snapshotInstallation(Config{Drrun: launcher, Client: client})
			if (err == nil) != tc.valid {
				t.Fatalf("worker %s, snapshot error %v", EngineProfile, err)
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
	manifest := map[string]any{"schema": 1, "inputs": map[string]any{"source_commit": runtimeSourceCommit, "source_sha256": runtimeSourceSHA256, "patch_sha256": runtimePatchSHA256, "code_replay_patch_sha256": runtimeCodeReplayPatchSHA256, "recipe": 1, "profiles": map[string]string{"release": "Release", "debug": "Debug"}, "cmake_options": []string{}}, "artifacts": map[string]any{releaseCore: hex.EncodeToString(digest[:]), "lib64/debug/libdynamorio.so": strings.Repeat("a", 64), "lib64/release/libdynamorio.so.debug": nil, "lib64/debug/libdynamorio.so.debug": nil}}
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
		"wrong source":  func(t *testing.T, r string) { replaceRuntimeMarker(t, r, runtimeSourceCommit, strings.Repeat("a", 40)) },
		"wrong archive": func(t *testing.T, r string) { replaceRuntimeMarker(t, r, runtimeSourceSHA256, strings.Repeat("b", 64)) },
		"wrong patch":   func(t *testing.T, r string) { replaceRuntimeMarker(t, r, runtimePatchSHA256, strings.Repeat("c", 64)) },
		"missing replay patch": func(t *testing.T, r string) {
			replaceRuntimeMarker(t, r, `"code_replay_patch_sha256":"`+runtimeCodeReplayPatchSHA256+`",`, "")
		},
		"wrong replay patch": func(t *testing.T, r string) {
			replaceRuntimeMarker(t, r, runtimeCodeReplayPatchSHA256, strings.Repeat("d", 64))
		},
		"null replay patch": func(t *testing.T, r string) {
			replaceRuntimeMarker(t, r, `"`+runtimeCodeReplayPatchSHA256+`"`, `null`)
		},
		"numeric replay patch": func(t *testing.T, r string) {
			replaceRuntimeMarker(t, r, `"`+runtimeCodeReplayPatchSHA256+`"`, `1`)
		},
		"duplicate replay patch": func(t *testing.T, r string) {
			replaceRuntimeMarker(t, r, `"code_replay_patch_sha256":"`+runtimeCodeReplayPatchSHA256+`"`, `"code_replay_patch_sha256":"old","code_replay_patch_sha256":"`+runtimeCodeReplayPatchSHA256+`"`)
		},
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

func TestArchivedShadowInstallationUsesItsOriginalReplayPatch(t *testing.T) {
	for _, historical := range []struct{ profile, source string }{
		{"dynamorio-11.3-durante-v6-vm32m", "238193f90f5896b300563180e7939088ac8c346c"},
		{"dynamorio-11.3-durante-v5-vm32m", "4016b33fc69b93d36a13efbd00953213b8f6317f"},
	} {
		i := InstallationIdentity{SourceCommit: historical.source, RuntimeSourceCommit: runtimeSourceCommit,
			RuntimePatchSHA256: runtimePatchSHA256, RuntimeCodeReplayPatchSHA256: archivedV5V6CodeReplayPatchSHA256,
			LauncherSHA256: strings.Repeat("a", 64), ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true}
		r := Report{EngineProfile: historical.profile, Installation: &i}
		if !r.ValidShadowInstallationIdentity() || r.ValidInstallationIdentity() {
			t.Fatalf("%s archive identity mismatch", historical.profile)
		}
		i.RuntimeCodeReplayPatchSHA256 = runtimeCodeReplayPatchSHA256
		if r.ValidShadowInstallationIdentity() {
			t.Fatalf("%s accepted a replay patch from a later engine", historical.profile)
		}
	}
}

func TestShadowRecordsPreserveProfileSourcePair(t *testing.T) {
	identity := InstallationIdentity{RuntimeSourceCommit: runtimeSourceCommit, RuntimePatchSHA256: runtimePatchSHA256,
		RuntimeCodeReplayPatchSHA256: runtimeCodeReplayPatchSHA256, LauncherSHA256: strings.Repeat("a", 64),
		ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true}
	profiles := map[string]string{engineProfileV7: sourceCommitV7, engineProfileV8: sourceCommitV8, engineProfileV9: sourceCommitV9, engineProfileV10: sourceCommitV10, engineProfileV11: sourceCommitV11}
	for profile, expected := range profiles {
		for _, source := range profiles {
			identity.SourceCommit = source
			r := Report{EngineProfile: profile, Installation: &identity}
			matches := source == expected
			if r.ValidShadowInstallationIdentity() != matches || r.ValidInstallationIdentity() != (matches && profile == EngineProfile) {
				t.Errorf("wrong installation acceptance for %s with source %s", profile, source)
			}
		}
	}
}

func TestInstallationIdentityRejectsAmbiguousJSONAndDigestAliases(t *testing.T) {
	identity := InstallationIdentity{SourceCommit: SourceCommit, RuntimeSourceCommit: runtimeSourceCommit,
		RuntimePatchSHA256: runtimePatchSHA256, RuntimeCodeReplayPatchSHA256: runtimeCodeReplayPatchSHA256, LauncherSHA256: strings.Repeat("a", 64),
		ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true}
	valid := Report{EngineProfile: EngineProfile, Installation: &identity}
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
		"old source": func(i *InstallationIdentity) { i.SourceCommit = "21397969d18c74ceef816779aba026358f483250" },
		"old runtime patch": func(i *InstallationIdentity) {
			i.RuntimePatchSHA256 = "7b2fbaca84e259aeeafdfc4902ce7a330c48cec51e16804fe74ee3b99ea39f6d"
		},
		"missing replay patch": func(i *InstallationIdentity) { i.RuntimeCodeReplayPatchSHA256 = "" },
		"wrong replay patch":   func(i *InstallationIdentity) { i.RuntimeCodeReplayPatchSHA256 = strings.Repeat("d", 64) },
		"uppercase replay patch": func(i *InstallationIdentity) {
			i.RuntimeCodeReplayPatchSHA256 = strings.ToUpper(i.RuntimeCodeReplayPatchSHA256)
		},
		"uppercase launcher":   func(i *InstallationIdentity) { i.LauncherSHA256 = strings.ToUpper(i.LauncherSHA256) },
		"uppercase client":     func(i *InstallationIdentity) { i.ClientSHA256 = strings.ToUpper(i.ClientSHA256) },
		"uppercase core":       func(i *InstallationIdentity) { i.CoreSHA256 = strings.ToUpper(i.CoreSHA256) },
		"uppercase stock core": func(i *InstallationIdentity) { i.CoreSHA256 = strings.ToUpper(stockReleaseSHA256) },
		"stock core":           func(i *InstallationIdentity) { i.CoreSHA256 = stockReleaseSHA256 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := identity
			edit(&copy)
			if (&Report{EngineProfile: EngineProfile, Installation: &copy}).ValidInstallationIdentity() {
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

func TestInstallationIdentityReadsLegacyWithoutQualifyingCurrentRuntime(t *testing.T) {
	legacy := InstallationIdentity{
		SourceCommit: "21397969d18c74ceef816779aba026358f483250", RuntimeSourceCommit: runtimeSourceCommit,
		RuntimePatchSHA256: "7b2fbaca84e259aeeafdfc4902ce7a330c48cec51e16804fe74ee3b99ea39f6d",
		LauncherSHA256:     strings.Repeat("a", 64), ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true,
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded InstallationIdentity
	if err := json.Unmarshal(data, &decoded); err != nil || decoded != legacy {
		t.Fatal("historical installation identity did not round-trip", err)
	}
	if (&Report{EngineProfile: EngineProfile, Installation: &decoded}).ValidInstallationIdentity() {
		t.Fatal("historical installation qualified as the current runtime")
	}
	roundTrip, err := json.Marshal(decoded)
	if err != nil || string(roundTrip) != string(data) || strings.Contains(string(roundTrip), "runtime_code_replay_patch_sha256") {
		t.Fatal("historical identity gained a replay patch claim", err)
	}
	prefix := string(data[:len(data)-1])
	for _, value := range []string{`null`, `1`, `true`, `{}`, `[]`, `""`} {
		var invalid InstallationIdentity
		if err := json.Unmarshal([]byte(prefix+`,"runtime_code_replay_patch_sha256":`+value+`}`), &invalid); err == nil {
			t.Fatal("invalid present replay patch field accepted", value)
		}
	}
	if err := json.Unmarshal([]byte(prefix+`,"unknown":"value"}`), &decoded); err == nil {
		t.Fatal("historical identity accepted an unknown field")
	}
	if err := json.Unmarshal([]byte(prefix+`,"runtime_code_replay_patch_sha256":"`+runtimeCodeReplayPatchSHA256+`"}`), &decoded); err != nil {
		t.Fatal("explicit replay field could not be read", err)
	}
	if (&Report{EngineProfile: EngineProfile, Installation: &decoded}).ValidInstallationIdentity() {
		t.Fatal("replay patch alone qualified a historical source/runtime pin")
	}
}

func TestRuntimeInstallationPinsMatchVendoredSource(t *testing.T) {
	vendor := filepath.Join("..", "..", "third_party")
	for dir, commit := range map[string]string{"durante": sourceCommitV7, "durante-v8": sourceCommitV8, "durante-v9": sourceCommitV9, "durante-v10": sourceCommitV10, "durante-v11": sourceCommitV11} {
		data, err := os.ReadFile(filepath.Join(vendor, dir, "UPSTREAM.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Commit string `json:"commit"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil || manifest.Commit != commit {
			t.Fatalf("%s installation source pin differs from vendor manifest", dir)
		}
	}
	for name, want := range map[string]string{
		"dynamorio-vfork-lifetime.patch": runtimePatchSHA256,
		"dynamorio-code-replay.patch":    runtimeCodeReplayPatchSHA256,
	} {
		data, err := os.ReadFile(filepath.Join(vendor, "durante", "toolkit", "patches", name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != want {
			t.Fatal("installation pin differs from vendored patch", name)
		}
	}
}
