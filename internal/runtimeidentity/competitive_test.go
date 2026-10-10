package runtimeidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestCompetitiveIdentityUsesResidentExecutableAndImmutableImage(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	identity, err := Competitive(image)
	if err != nil {
		t.Fatal(err)
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runnerHash := sha256.Sum256(data)
	if identity.RunnerSHA256 != hex.EncodeToString(runnerHash[:]) || identity.ImageDigest != image {
		t.Fatalf("identity did not bind resident executable: %+v", identity)
	}
	expected := sha256.Sum256([]byte("aonohako-competitive-runtime-v1\x00" + image + "\x00" + identity.RunnerSHA256 + "\x00"))
	if identity.RuntimeFingerprint != hex.EncodeToString(expected[:]) {
		t.Fatalf("fingerprint recipe mismatch: %+v", identity)
	}
	changed, err := Competitive("sha256:" + strings.Repeat("b", 64))
	if err != nil || changed.RuntimeFingerprint == identity.RuntimeFingerprint || changed.RunnerSHA256 != identity.RunnerSHA256 {
		t.Fatalf("image change did not change identity: %+v %v", changed, err)
	}
	for _, invalid := range []string{"", "mutable:latest", "sha256:" + strings.Repeat("A", 64), "sha256:abc"} {
		if _, err := Competitive(invalid); err == nil {
			t.Fatalf("invalid digest accepted: %q", invalid)
		}
	}
}
