package durante

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const runtimeSourceCommit = "6847bb210bea0ca11d1bf47704383d6a075da490"
const runtimeSourceSHA256 = "e2b7d3f1917ff64e644b81efd96a610a3c622f4857c75593fef5d5e581c231f6"

// Updated with the reviewed runtime patches. This is installation consistency,
// not a signature or attestation against an adversarial image administrator.
// Profiles v5 through v11 share the first runtime patch; v7 through v11 also
// share its code-replay patch. v12 carries the runtime fixes in Durante #96.
// Each engine_vNN.go selects the pair its worker installs.
const runtimePatchSHA256V7 = "59fc79b5055a0c42b463703768891f0a10199ef1135d43028826c8c3894bab7c"
const runtimeCodeReplayPatchSHA256V7 = "391cd6f5e35dcbda372cab5dd98fa40a5b2a41effda52ecfce9afa945acb249b"
const runtimePatchSHA256V12 = "8b82ea0b82b3bfdfa3cf92a5fb46e4a9cc457fc4e52747412adfbcbf18b70251"
const runtimeCodeReplayPatchSHA256V12 = "aad0137aadca8a0932ca22f67ea95260880eb6bb93a229fd540c01241dc80d9d"

// RuntimePatchSHA256 and RuntimeCodeReplayPatchSHA256 are the runtime patch
// digests this worker's installation records.
const (
	RuntimePatchSHA256           = runtimePatchSHA256
	RuntimeCodeReplayPatchSHA256 = runtimeCodeReplayPatchSHA256
)

const archivedV5V6CodeReplayPatchSHA256 = "aad9b561fb89e09d52d7a765124226659b8dbd8b886da61bc73d15fde28b5b72"
const stockReleaseSHA256 = "9181f594aeeed921f44d572aebf429069f5f1e33a01f95b4ae32a100bf48788c"
const releaseCore = "lib64/release/libdynamorio.so"

const (
	engineProfileV7  = "dynamorio-11.3-durante-v7-vm32m"
	sourceCommitV7   = "41743068ef9070b6e04195bf7822c5ee513660ff"
	engineProfileV8  = "dynamorio-11.3-durante-v8-vm32m"
	sourceCommitV8   = "614761c18d4735f00e93667c74c099cfdb2800c7"
	engineProfileV9  = "dynamorio-11.3-durante-v9-vm32m"
	sourceCommitV9   = "27954247e094e682a7cf47504ff13682f970469a"
	engineProfileV10 = "dynamorio-11.3-durante-v10-vm32m"
	sourceCommitV10  = "191858a02666c1406b871e4974b9072b2c91bb34"
	engineProfileV11 = "dynamorio-11.3-durante-v11-vm32m"
	sourceCommitV11  = "74d9188f9fd86c75f079277e6e5278b379a0a1d7"
	engineProfileV12 = "dynamorio-11.3-durante-v12-vm32m"
	sourceCommitV12  = "66fbf09e065893376e2720af04f9930883161dfa"
)

// InstallationIdentity is reproducibility metadata from the trusted runner.
// Matching hashes do not attest application counters or an adversarial host.
type InstallationIdentity struct {
	SourceCommit        string `json:"source_commit"`
	RuntimeSourceCommit string `json:"runtime_source_commit"`
	RuntimePatchSHA256  string `json:"runtime_patch_sha256"`
	// Absent only in historical identities. Reading those records does not
	// qualify their installation for the current execution profile.
	RuntimeCodeReplayPatchSHA256 string `json:"runtime_code_replay_patch_sha256,omitempty"`
	LauncherSHA256               string `json:"launcher_sha256"`
	ClientSHA256                 string `json:"client_sha256"`
	CoreSHA256                   string `json:"release_core_sha256"`
	Unchanged                    bool   `json:"unchanged_before_after"`
}

func (i *InstallationIdentity) UnmarshalJSON(data []byte) error {
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	keys := []string{"source_commit", "runtime_source_commit", "runtime_patch_sha256", "launcher_sha256", "client_sha256", "release_core_sha256", "unchanged_before_after"}
	_, hasReplayPatch := fields["runtime_code_replay_patch_sha256"]
	if hasReplayPatch {
		keys = append(keys, "runtime_code_replay_patch_sha256")
	}
	if len(fields) != len(keys) {
		return fmt.Errorf("invalid installation identity fields")
	}
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("missing installation identity field")
		}
	}
	type plain InstallationIdentity
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if hasReplayPatch && decoded.RuntimeCodeReplayPatchSHA256 == "" {
		return fmt.Errorf("empty installation replay patch field")
	}
	*i = InstallationIdentity(decoded)
	return nil
}

func (r *Report) ValidInstallationIdentity() bool {
	return r != nil && r.EngineProfile == EngineProfile &&
		r.Installation.validForSource(SourceCommit, runtimePatchSHA256, runtimeCodeReplayPatchSHA256)
}

// ValidShadowInstallationIdentity checks archived observations against the
// installation pinned by the worker that emitted their engine profile.
func (r *Report) ValidShadowInstallationIdentity() bool {
	if r == nil {
		return false
	}
	commit, patch, replay, ok := profileInstallation(r.EngineProfile)
	if !ok {
		return false
	}
	// The first worker with shadow support used the same runtime patches as
	// v6 and a client that did not emit latency bounds.
	if r.EngineProfile == "dynamorio-11.3-durante-v5-vm32m" && r.LatencyBound != nil {
		return false
	}
	return r.Installation.validForSource(commit, patch, replay)
}

// profileInstallation returns the Durante source and runtime patch pair that
// the worker emitting an engine profile installed.
func profileInstallation(profile string) (commit, patch, replay string, ok bool) {
	switch profile {
	case engineProfileV12:
		return sourceCommitV12, runtimePatchSHA256V12, runtimeCodeReplayPatchSHA256V12, true
	case engineProfileV11:
		return sourceCommitV11, runtimePatchSHA256V7, runtimeCodeReplayPatchSHA256V7, true
	case engineProfileV10:
		return sourceCommitV10, runtimePatchSHA256V7, runtimeCodeReplayPatchSHA256V7, true
	case engineProfileV9:
		return sourceCommitV9, runtimePatchSHA256V7, runtimeCodeReplayPatchSHA256V7, true
	case engineProfileV8:
		return sourceCommitV8, runtimePatchSHA256V7, runtimeCodeReplayPatchSHA256V7, true
	case engineProfileV7:
		return sourceCommitV7, runtimePatchSHA256V7, runtimeCodeReplayPatchSHA256V7, true
	case "dynamorio-11.3-durante-v6-vm32m":
		return "238193f90f5896b300563180e7939088ac8c346c", runtimePatchSHA256V7, archivedV5V6CodeReplayPatchSHA256, true
	case "dynamorio-11.3-durante-v5-vm32m":
		return "4016b33fc69b93d36a13efbd00953213b8f6317f", runtimePatchSHA256V7, archivedV5V6CodeReplayPatchSHA256, true
	default:
		return "", "", "", false
	}
}

func (i *InstallationIdentity) validForSource(commit, patch, replayPatch string) bool {
	if i == nil || !i.Unchanged || i.SourceCommit != commit || i.RuntimeSourceCommit != runtimeSourceCommit || i.RuntimePatchSHA256 != patch || i.RuntimeCodeReplayPatchSHA256 != replayPatch {
		return false
	}
	for _, digest := range []string{i.LauncherSHA256, i.ClientSHA256, i.CoreSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
			return false
		}
	}
	return i.CoreSHA256 != stockReleaseSHA256
}

func snapshotInstallation(cfg Config) (InstallationIdentity, error) {
	core, err := verifyRuntimeInstallation(cfg.Drrun)
	if err != nil {
		return InstallationIdentity{}, err
	}
	identity := InstallationIdentity{SourceCommit: SourceCommit, RuntimeSourceCommit: runtimeSourceCommit,
		RuntimePatchSHA256: runtimePatchSHA256, RuntimeCodeReplayPatchSHA256: runtimeCodeReplayPatchSHA256, CoreSHA256: core}
	launcher, err := installationFile(cfg.Drrun, 64<<20)
	if err != nil {
		return InstallationIdentity{}, err
	}
	client, err := installationFile(cfg.Client, 64<<20)
	if err != nil {
		return InstallationIdentity{}, err
	}
	if err := verifyClientSource(client); err != nil {
		return InstallationIdentity{}, err
	}
	identity.LauncherSHA256 = fmt.Sprintf("%x", sha256.Sum256(launcher))
	identity.ClientSHA256 = fmt.Sprintf("%x", sha256.Sum256(client))
	return identity, nil
}

// From v8 on, the selected DSO must carry its reviewed source commit even
// when the operator overrides the client path. Existing v7 packages have no marker.
func verifyClientSource(data []byte) error {
	file, err := elf.NewFile(bytes.NewReader(data))
	if err != nil || file.Class != elf.ELFCLASS64 || file.Machine != elf.EM_X86_64 || file.Type != elf.ET_DYN {
		return fmt.Errorf("durante requires a Linux x86-64 ELF shared client")
	}
	found := false
	for _, section := range file.Sections {
		if section.Name != ".durante.source" {
			continue
		}
		if found || section.Type != elf.SHT_PROGBITS || section.Flags != 0 || section.Size != uint64(len(SourceCommit)) {
			return fmt.Errorf("invalid durante client source marker")
		}
		found = true
		source, err := section.Data()
		if err != nil || string(source) != SourceCommit {
			return fmt.Errorf("durante client source does not match %s", EngineProfile)
		}
	}
	if !found && EngineProfile != engineProfileV7 {
		return fmt.Errorf("durante client source marker is missing for %s", EngineProfile)
	}
	return nil
}

// manifestObject rejects duplicate fields before decoding security-relevant
// objects; json.Unmarshal alone would silently accept the last duplicate.
func manifestObject(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("invalid runtime manifest object")
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid runtime manifest field")
		}
		if _, ok := result[key]; ok {
			return nil, fmt.Errorf("duplicate runtime manifest field")
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, err
		}
		result[key] = raw
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("invalid runtime manifest end")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing runtime manifest data")
	}
	return result, nil
}

func installationFile(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, fmt.Errorf("runtime installation file is unavailable, unsafe or oversized")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || int64(len(data)) > maximum || int64(len(data)) != info.Size() {
		return nil, fmt.Errorf("runtime installation file changed or exceeds bound")
	}
	return data, nil
}

// verifyRuntimeInstallation checks the pinned builder inputs and actual release
// core selected by the fixed Wrap arguments. Debug symbols and unused profiles
// are not scanned on every execution. Deployments must keep this operator-owned
// installation immutable while the service runs.
func verifyRuntimeInstallation(drrun string) (string, error) {
	if filepath.Base(drrun) != "drrun" || filepath.Base(filepath.Dir(drrun)) != "bin64" {
		return "", fmt.Errorf("durante requires its installed bin64/drrun launcher")
	}
	root := filepath.Dir(filepath.Dir(drrun))
	for _, directory := range []string{root, filepath.Join(root, "bin64"), filepath.Join(root, "lib64"), filepath.Join(root, "lib64/release")} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return "", fmt.Errorf("durante runtime directory is unavailable or writable")
		}
	}
	data, err := installationFile(filepath.Join(root, ".durante-runtime.json"), 65536)
	if err != nil {
		return "", err
	}
	manifest, err := manifestObject(data)
	if err != nil {
		return "", err
	}
	var schema int
	if json.Unmarshal(manifest["schema"], &schema) != nil || schema != 1 {
		return "", fmt.Errorf("unsupported runtime manifest schema")
	}
	inputs, err := manifestObject(manifest["inputs"])
	if err != nil {
		return "", err
	}
	for key, want := range map[string]string{"source_commit": runtimeSourceCommit, "source_sha256": runtimeSourceSHA256, "patch_sha256": runtimePatchSHA256, "code_replay_patch_sha256": runtimeCodeReplayPatchSHA256} {
		var got string
		if json.Unmarshal(inputs[key], &got) != nil || got != want {
			return "", fmt.Errorf("durante runtime input %s does not match the reviewed pin", key)
		}
	}
	var recipe int
	if json.Unmarshal(inputs["recipe"], &recipe) != nil || recipe != 1 {
		return "", fmt.Errorf("unsupported runtime build recipe")
	}
	artifacts, err := manifestObject(manifest["artifacts"])
	if err != nil {
		return "", err
	}
	var expected string
	if json.Unmarshal(artifacts[releaseCore], &expected) != nil || len(expected) != 64 || expected == stockReleaseSHA256 {
		return "", fmt.Errorf("durante requires a recorded patched release core")
	}
	digest, err := hex.DecodeString(expected)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != expected {
		return "", fmt.Errorf("invalid runtime core digest")
	}
	corePath := filepath.Join(root, filepath.FromSlash(releaseCore))
	info, err := os.Lstat(corePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() <= 0 || info.Size() > 64<<20 {
		return "", fmt.Errorf("durante runtime core is unavailable, unsafe or oversized")
	}
	core, err := os.Open(corePath)
	if err != nil {
		return "", err
	}
	defer core.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(core, (64<<20)+1))
	if err != nil || size != info.Size() {
		return "", fmt.Errorf("durante runtime core changed during verification")
	}
	if !bytes.Equal(hash.Sum(nil), digest) {
		return "", fmt.Errorf("durante runtime release core hash mismatch")
	}
	return expected, nil
}
