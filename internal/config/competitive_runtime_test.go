package config

import (
	"strings"
	"testing"
)

func TestLoadCompetitiveRuntimeImageBinding(t *testing.T) {
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_AUTH", "none")
	for _, value := range []string{"", "sha256:" + strings.Repeat("a", 64)} {
		t.Setenv("AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT", value)
		cfg, err := Load()
		if err != nil || cfg.CompetitiveRuntimeFingerprint != value {
			t.Fatalf("image binding %q: %+v %v", value, cfg, err)
		}
	}
	for _, value := range []string{"latest", "sha256:" + strings.Repeat("A", 64), " sha256:" + strings.Repeat("a", 64)} {
		t.Setenv("AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT", value)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "COMPETITIVE_RUNTIME_FINGERPRINT") {
			t.Fatalf("invalid image binding %q accepted: %v", value, err)
		}
	}
}
