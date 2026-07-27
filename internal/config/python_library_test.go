package config

import (
	"testing"

	"aonohako/internal/platform"
	"aonohako/internal/pythonpolicy"
)

func configurePythonPolicyLoadTest(t *testing.T) {
	t.Helper()
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "https://runner.internal")
	t.Setenv("AONOHAKO_DEFAULT_PYTHON_LIBRARY_MODE", "")
	t.Setenv("AONOHAKO_ALLOW_REQUEST_PYTHON_INSTALLED_LIBRARIES", "")
	t.Setenv("AONOHAKO_PROBLEM_PYTHON_LIBRARY_MODES", "")
}

func TestLoadDefaultsPythonLibrariesToStdlib(t *testing.T) {
	configurePythonPolicyLoadTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DefaultPythonLibraryMode != pythonpolicy.LibraryModeStdlib {
		t.Fatalf("default mode = %q, want stdlib", cfg.DefaultPythonLibraryMode)
	}
	if !cfg.AllowRequestPythonInstalledLibraries {
		t.Fatalf("dev should allow request-selected installed libraries")
	}
}

func TestLoadParsesPythonLibraryPolicy(t *testing.T) {
	configurePythonPolicyLoadTest(t)
	t.Setenv("AONOHAKO_DEFAULT_PYTHON_LIBRARY_MODE", "installed")
	t.Setenv("AONOHAKO_ALLOW_REQUEST_PYTHON_INSTALLED_LIBRARIES", "false")
	t.Setenv("AONOHAKO_PROBLEM_PYTHON_LIBRARY_MODES", `{"contest-1/a":"stdlib","contest-1/b":"installed"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DefaultPythonLibraryMode != pythonpolicy.LibraryModeInstalled {
		t.Fatalf("default mode = %q, want installed", cfg.DefaultPythonLibraryMode)
	}
	if cfg.AllowRequestPythonInstalledLibraries {
		t.Fatalf("request-selected installed libraries should be disabled")
	}
	if got := cfg.ProblemPythonLibraryModes["contest-1/a"]; got != pythonpolicy.LibraryModeStdlib {
		t.Fatalf("contest-1/a mode = %q, want stdlib", got)
	}
	if got := cfg.ProblemPythonLibraryModes["contest-1/b"]; got != pythonpolicy.LibraryModeInstalled {
		t.Fatalf("contest-1/b mode = %q, want installed", got)
	}
}

func TestLoadRejectsInvalidPythonLibraryPolicy(t *testing.T) {
	configurePythonPolicyLoadTest(t)
	t.Setenv("AONOHAKO_DEFAULT_PYTHON_LIBRARY_MODE", "all")
	if _, err := Load(); err == nil {
		t.Fatalf("invalid default mode unexpectedly succeeded")
	}

	configurePythonPolicyLoadTest(t)
	t.Setenv("AONOHAKO_PROBLEM_PYTHON_LIBRARY_MODES", `{"contest-1/a":"all"}`)
	if _, err := Load(); err == nil {
		t.Fatalf("invalid problem mode unexpectedly succeeded")
	}
}

func TestDefaultAllowRequestPythonLibraries(t *testing.T) {
	if !defaultAllowRequestPythonLibraries(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev}) {
		t.Fatalf("dev should allow installed-library requests")
	}
	if defaultAllowRequestPythonLibraries(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetSelfHosted}) {
		t.Fatalf("selfhosted should deny installed-library requests by default")
	}
	if defaultAllowRequestPythonLibraries(platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetCloudRun}) {
		t.Fatalf("cloudrun should deny installed-library requests by default")
	}
}
