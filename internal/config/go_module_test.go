package config

import (
	"testing"

	"aonohako/internal/gomodulepolicy"
	"aonohako/internal/platform"
)

func configureGoModulePolicyLoadTest(t *testing.T) {
	t.Helper()
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_DEFAULT_GO_MODULE_MODE", "")
	t.Setenv("AONOHAKO_ALLOW_REQUEST_GO_INSTALLED_MODULES", "")
	t.Setenv("AONOHAKO_PROBLEM_GO_MODULE_MODES", "")
}

func TestLoadDefaultsGoModulesToStdlib(t *testing.T) {
	configureGoModulePolicyLoadTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DefaultGoModuleMode != gomodulepolicy.ModeStdlib {
		t.Fatalf("default mode = %q, want stdlib", cfg.DefaultGoModuleMode)
	}
	if !cfg.AllowRequestGoInstalledModules {
		t.Fatalf("dev should allow request-selected installed modules")
	}
}

func TestLoadParsesGoModulePolicy(t *testing.T) {
	configureGoModulePolicyLoadTest(t)
	t.Setenv("AONOHAKO_DEFAULT_GO_MODULE_MODE", "installed")
	t.Setenv("AONOHAKO_ALLOW_REQUEST_GO_INSTALLED_MODULES", "false")
	t.Setenv("AONOHAKO_PROBLEM_GO_MODULE_MODES", `{"contest-1/a":"stdlib","contest-1/b":"installed"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DefaultGoModuleMode != gomodulepolicy.ModeInstalled {
		t.Fatalf("default mode = %q, want installed", cfg.DefaultGoModuleMode)
	}
	if cfg.AllowRequestGoInstalledModules {
		t.Fatalf("request-selected installed modules should be disabled")
	}
	if got := cfg.ProblemGoModuleModes["contest-1/a"]; got != gomodulepolicy.ModeStdlib {
		t.Fatalf("contest-1/a mode = %q, want stdlib", got)
	}
	if got := cfg.ProblemGoModuleModes["contest-1/b"]; got != gomodulepolicy.ModeInstalled {
		t.Fatalf("contest-1/b mode = %q, want installed", got)
	}
}

func TestLoadRejectsInvalidGoModulePolicy(t *testing.T) {
	configureGoModulePolicyLoadTest(t)
	t.Setenv("AONOHAKO_DEFAULT_GO_MODULE_MODE", "all")
	if _, err := Load(); err == nil {
		t.Fatalf("invalid default mode unexpectedly succeeded")
	}

	configureGoModulePolicyLoadTest(t)
	t.Setenv("AONOHAKO_PROBLEM_GO_MODULE_MODES", `{"contest-1/a":"all"}`)
	if _, err := Load(); err == nil {
		t.Fatalf("invalid problem mode unexpectedly succeeded")
	}
}

func TestDefaultAllowRequestGoInstalledModules(t *testing.T) {
	if !defaultAllowRequestGoInstalledModules(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev}) {
		t.Fatalf("dev should allow installed-module requests")
	}
	if defaultAllowRequestGoInstalledModules(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetSelfHosted}) {
		t.Fatalf("selfhosted should deny installed-module requests by default")
	}
	if defaultAllowRequestGoInstalledModules(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetCloudRun}) {
		t.Fatalf("cloudrun should deny installed-module requests by default")
	}
}
