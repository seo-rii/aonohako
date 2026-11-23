package runtimeidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
)

type CompetitiveIdentity struct {
	RuntimeFingerprint string `json:"runtime_fingerprint"`
	RunnerSHA256       string `json:"runner_sha256"`
	ImageDigest        string `json:"image_digest"`
}

var executableOnce sync.Once
var executableSHA256 string
var executableError error

func ValidImageDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	for _, char := range value[7:] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// Competitive binds the trusted deployment's immutable image digest to the
// actual running executable. This does not read a caller-provided fingerprint.
func Competitive(imageDigest string) (CompetitiveIdentity, error) {
	if !ValidImageDigest(imageDigest) {
		return CompetitiveIdentity{}, fmt.Errorf("competitive runtime requires an immutable sha256 image digest")
	}
	executableOnce.Do(func() {
		path := "/proc/self/exe"
		if runtime.GOOS != "linux" {
			path, executableError = os.Executable()
			if executableError != nil {
				return
			}
		}
		file, err := os.Open(path)
		if err != nil {
			executableError = err
			return
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			executableError = err
			return
		}
		executableSHA256 = hex.EncodeToString(hash.Sum(nil))
	})
	if executableError != nil {
		return CompetitiveIdentity{}, executableError
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, "aonohako-competitive-runtime-v1\x00"+imageDigest+"\x00"+executableSHA256+"\x00")
	return CompetitiveIdentity{
		RuntimeFingerprint: hex.EncodeToString(hash.Sum(nil)),
		RunnerSHA256:       executableSHA256, ImageDigest: imageDigest,
	}, nil
}
