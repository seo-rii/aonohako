package config

import (
	"testing"

	"aonohako/internal/platform"
	"aonohako/internal/rustpolicy"
)

func configureRustCratePolicyLoadTest(t *testing.T) {
	t.Helper()
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_DEFAULT_RUST_CRATE_MODE", "")
	t.Setenv("AONOHAKO_ALLOW_REQUEST_RUST_INSTALLED_CRATES", "")
	t.Setenv("AONOHAKO_PROBLEM_RUST_CRATE_MODES", "")
}

func TestLoadDefaultsRustCratesToStdlib(t *testing.T) {
	configureRustCratePolicyLoadTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DefaultRustCrateMode != rustpolicy.CrateModeStdlib {
		t.Fatalf("default mode = %q, want stdlib", cfg.DefaultRustCrateMode)
	}
	if !cfg.AllowRequestRustInstalledCrates {
		t.Fatalf("dev should allow request-selected installed crates")
	}
}

func TestLoadParsesRustCratePolicy(t *testing.T) {
	configureRustCratePolicyLoadTest(t)
	t.Setenv("AONOHAKO_DEFAULT_RUST_CRATE_MODE", "installed")
	t.Setenv("AONOHAKO_ALLOW_REQUEST_RUST_INSTALLED_CRATES", "false")
	t.Setenv("AONOHAKO_PROBLEM_RUST_CRATE_MODES", `{"contest-1/a":"stdlib","contest-1/b":"installed"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DefaultRustCrateMode != rustpolicy.CrateModeInstalled {
		t.Fatalf("default mode = %q, want installed", cfg.DefaultRustCrateMode)
	}
	if cfg.AllowRequestRustInstalledCrates {
		t.Fatalf("request-selected installed crates should be disabled")
	}
	if got := cfg.ProblemRustCrateModes["contest-1/a"]; got != rustpolicy.CrateModeStdlib {
		t.Fatalf("contest-1/a mode = %q, want stdlib", got)
	}
	if got := cfg.ProblemRustCrateModes["contest-1/b"]; got != rustpolicy.CrateModeInstalled {
		t.Fatalf("contest-1/b mode = %q, want installed", got)
	}
}

func TestLoadRejectsInvalidRustCratePolicy(t *testing.T) {
	configureRustCratePolicyLoadTest(t)
	t.Setenv("AONOHAKO_DEFAULT_RUST_CRATE_MODE", "all")
	if _, err := Load(); err == nil {
		t.Fatalf("invalid default mode unexpectedly succeeded")
	}

	configureRustCratePolicyLoadTest(t)
	t.Setenv("AONOHAKO_PROBLEM_RUST_CRATE_MODES", `{"contest-1/a":"all"}`)
	if _, err := Load(); err == nil {
		t.Fatalf("invalid problem mode unexpectedly succeeded")
	}
}

func TestDefaultAllowRequestRustCrates(t *testing.T) {
	if !defaultAllowRequestRustCrates(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev}) {
		t.Fatalf("dev should allow installed-crate requests")
	}
	if defaultAllowRequestRustCrates(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetSelfHosted}) {
		t.Fatalf("selfhosted should deny installed-crate requests by default")
	}
	if defaultAllowRequestRustCrates(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetCloudRun}) {
		t.Fatalf("cloudrun should deny installed-crate requests by default")
	}
}
