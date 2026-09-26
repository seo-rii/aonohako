package runtimepacks

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

type refreshWorkflow struct {
	Env map[string]string `yaml:"env"`
	On  struct {
		WorkflowCall struct {
			Inputs map[string]struct {
				Type     string `yaml:"type"`
				Default  bool   `yaml:"default"`
				Required bool   `yaml:"required"`
			} `yaml:"inputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Jobs map[string]struct {
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
		Env  map[string]string `yaml:"env"`
	} `yaml:"jobs"`
}

func TestCIPropagatesRefreshToEveryRuntimeWorkflow(t *testing.T) {
	read := func(name string) refreshWorkflow {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		var workflow refreshWorkflow
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return workflow
	}
	const refreshEnv = "AONOHAKO_RUNTIME_REFRESH"
	const dispatch = "${{ inputs.force_runtime_checks || false }}"
	ci := read("ci.yml")
	if ci.Env[refreshEnv] != dispatch {
		t.Fatal("direct profile, sandbox, SBOM and mixin builds must inherit dispatch refresh")
	}
	for _, name := range []string{"toolchain-profile", "sandbox", "image-sbom", "mixin-smoke"} {
		job, ok := ci.Jobs[name]
		if !ok {
			t.Fatalf("missing runtime job %s", name)
		}
		if override, ok := job.Env[refreshEnv]; ok && override != dispatch {
			t.Errorf("%s overrides dispatch refresh with %q", name, override)
		}
	}
	for _, name := range []string{"language-smoke", "language-smoke-cache-read", "language-smoke-cache-write", "toolchain-profile-cache-read", "toolchain-profile-cache-write"} {
		job, ok := ci.Jobs[name]
		if !ok || job.Uses == "" || job.With["refresh_runtime"] != dispatch {
			t.Errorf("%s must forward the dispatch flag to its reusable workflow", name)
		}
	}
	for _, name := range []string{"runtime-smoke.yml", "toolchain-profile.yml"} {
		workflow := read(name)
		input, ok := workflow.On.WorkflowCall.Inputs["refresh_runtime"]
		if !ok || input.Type != "boolean" || input.Default || input.Required {
			t.Errorf("%s must declare an optional refresh boolean defaulting to false", name)
		}
		if workflow.Env[refreshEnv] != "${{ inputs.refresh_runtime }}" {
			t.Errorf("%s does not pass refresh to the runtime builder environment", name)
		}
	}
}
