package durante

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const runtimeSourceCommit = "6847bb210bea0ca11d1bf47704383d6a075da490"
const runtimeSourceSHA256 = "e2b7d3f1917ff64e644b81efd96a610a3c622f4857c75593fef5d5e581c231f6"

// Updated with the reviewed runtime patch. This is installation consistency,
// not a signature or attestation against an adversarial image administrator.
const runtimePatchSHA256 = "7b2fbaca84e259aeeafdfc4902ce7a330c48cec51e16804fe74ee3b99ea39f6d"
const stockReleaseSHA256 = "9181f594aeeed921f44d572aebf429069f5f1e33a01f95b4ae32a100bf48788c"
const releaseCore = "lib64/release/libdynamorio.so"

// Updated together with the complete third_party/durante snapshot.
const sourceCommit = "21397969d18c74ceef816779aba026358f483250"

// InstallationIdentity is reproducibility metadata from the trusted runner.
// Matching hashes do not attest application counters or an adversarial host.
type InstallationIdentity struct {
	SourceCommit        string `json:"source_commit"`
	RuntimeSourceCommit string `json:"runtime_source_commit"`
	RuntimePatchSHA256  string `json:"runtime_patch_sha256"`
	LauncherSHA256      string `json:"launcher_sha256"`
	ClientSHA256        string `json:"client_sha256"`
	CoreSHA256          string `json:"release_core_sha256"`
	Unchanged           bool   `json:"unchanged_before_after"`
}

func (r *Report) ValidInstallationIdentity() bool {
	if r == nil || r.Installation == nil {
		return false
	}
	i := r.Installation
	if !i.Unchanged || i.SourceCommit != sourceCommit || i.RuntimeSourceCommit != runtimeSourceCommit || i.RuntimePatchSHA256 != runtimePatchSHA256 {
		return false
	}
	for _, digest := range []string{i.LauncherSHA256, i.ClientSHA256, i.CoreSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
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
	identity := InstallationIdentity{SourceCommit: sourceCommit, RuntimeSourceCommit: runtimeSourceCommit,
		RuntimePatchSHA256: runtimePatchSHA256, CoreSHA256: core}
	for _, item := range []struct {
		path        string
		destination *string
	}{{cfg.Drrun, &identity.LauncherSHA256}, {cfg.Client, &identity.ClientSHA256}} {
		// Bound the actual selected binaries, not an administrator-supplied digest.
		data, err := installationFile(item.path, 64<<20)
		if err != nil {
			return InstallationIdentity{}, err
		}
		digest := sha256.Sum256(data)
		*item.destination = hex.EncodeToString(digest[:])
	}
	return identity, nil
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
	for key, want := range map[string]string{"source_commit": runtimeSourceCommit, "source_sha256": runtimeSourceSHA256, "patch_sha256": runtimePatchSHA256} {
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
	if err != nil || len(digest) != sha256.Size {
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
