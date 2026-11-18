package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"aonohako/internal/durante"
)

func runtimeOptionsCLIEnvironment(t *testing.T) {
	t.Helper()
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "AONOHAKO_") {
			t.Setenv(key, "")
		}
	}
	t.Setenv("AONOHAKO_DEPLOYMENT_TARGET", "dev")
	t.Setenv("AONOHAKO_INBOUND_AUTH", "none")
	t.Setenv("AONOHAKO_EXECUTION_TRANSPORT", "remote")
	t.Setenv("AONOHAKO_SANDBOX_BACKEND", "none")
	// There is deliberately no downstream runner. Description must stay offline.
	t.Setenv("AONOHAKO_REMOTE_RUNNER_URL", "http://127.0.0.1:1")
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", strings.Join(durante.RegisteredFamilies(), ","))
	t.Setenv("AONOHAKO_DURANTE_RUNTIME_IMAGE", "sha256:"+strings.Repeat("a", 64))
	t.Setenv("AONOHAKO_WORK_ROOT", filepath.Join(t.TempDir(), "unused-work-root"))
}

type describedRuntimeOptions struct {
	Schema         int    `json:"schema"`
	Authority      string `json:"authority"`
	RuntimeOptions []struct {
		Role             string                 `json:"role"`
		ProgramID        string                 `json:"program_id"`
		ParticipantIndex *int                   `json:"participant_index"`
		SourceLanguage   string                 `json:"source_language"`
		RuntimeOptions   durante.RuntimeOptions `json:"runtime_options"`
		RuntimeOptionsID string                 `json:"runtime_options_id"`
	} `json:"runtime_options"`
}

func describeRuntimeOptionsFixture(t *testing.T, payload string) describedRuntimeOptions {
	t.Helper()
	request := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(request, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"describe-runtime-options", "--request", request}, &stdout, &stderr); code != 0 {
		t.Fatalf("description failed: code=%d stderr=%s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	var result describedRuntimeOptions
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != 1 || result.Authority != durante.Authority || len(result.RuntimeOptions) == 0 {
		t.Fatalf("invalid description: %s", stdout.String())
	}
	for _, role := range result.RuntimeOptions {
		id, err := role.RuntimeOptions.ID()
		if err != nil || role.RuntimeOptionsID != id {
			t.Fatalf("description does not reproduce its published identity: id=%q err=%v", role.RuntimeOptionsID, err)
		}
	}
	if _, err := os.Stat(os.Getenv("AONOHAKO_WORK_ROOT")); !os.IsNotExist(err) {
		t.Fatalf("description materialized a work directory: %v", err)
	}
	return result
}

func TestRuntimeOptionsCLIUsesRawMemoryAndOperatorTuning(t *testing.T) {
	runtimeOptionsCLIEnvironment(t)
	request := `{"lang":"JAVASCRIPT","work_measurement":true,"limits":{"time_ms":1000,"memory_mb":64},"work_measurement_limits":{"cpu_time_ms":10000,"wall_time_ms":30000,"memory_mb":512}}`
	first := describeRuntimeOptionsFixture(t, request).RuntimeOptions[0]
	if first.Role != "contestant" || first.SourceLanguage != "JAVASCRIPT" || first.RuntimeOptions.MemoryMB != 512 ||
		!slices.Contains(first.RuntimeOptions.Argv, "--max-old-space-size=307") || !slices.Contains(first.RuntimeOptions.Argv, "--max-semi-space-size=8") {
		t.Fatalf("descriptor ignored the raw envelope: %+v", first)
	}
	t.Setenv("AONOHAKO_NODE_OLD_SPACE_PERCENT", "75")
	second := describeRuntimeOptionsFixture(t, request).RuntimeOptions[0]
	if !slices.Contains(second.RuntimeOptions.Argv, "--max-old-space-size=384") || first.RuntimeOptionsID == second.RuntimeOptionsID {
		t.Fatalf("operator runtime change did not change effective identity: %+v", second)
	}
}

func TestRuntimeOptionsCLIPreservesPeerRoles(t *testing.T) {
	runtimeOptionsCLIEnvironment(t)
	result := describeRuntimeOptionsFixture(t, `{"lang":"CPP20","work_measurement":true,"limits":{"time_ms":1000,"memory_mb":64},"interactor":{"lang":"PYTHON3","limits":{"time_ms":2000,"memory_mb":256}}}`)
	if len(result.RuntimeOptions) != 2 || result.RuntimeOptions[0].Role != "contestant" || result.RuntimeOptions[1].Role != "interactor" ||
		result.RuntimeOptions[0].SourceLanguage != "CPP20" || result.RuntimeOptions[1].SourceLanguage != "PYTHON3" ||
		result.RuntimeOptions[0].RuntimeOptions.MemoryMB != 64 || result.RuntimeOptions[1].RuntimeOptions.MemoryMB != 256 {
		t.Fatalf("role identities or memory limits were conflated: %+v", result)
	}
}

func TestRuntimeOptionsCLIAppliesHTTPRuntimePolicies(t *testing.T) {
	runtimeOptionsCLIEnvironment(t)
	t.Setenv("AONOHAKO_RUNTIME_TUNING_PROFILES", `{"fast":{"node_old_space_percent":75}}`)
	t.Setenv("AONOHAKO_PROBLEM_RUNTIME_PROFILES", `{"p":"fast"}`)
	node := describeRuntimeOptionsFixture(t, `{"lang":"JAVASCRIPT","problem_id":"p","work_measurement":true,"limits":{"time_ms":1000,"memory_mb":512}}`).RuntimeOptions[0]
	if !slices.Contains(node.RuntimeOptions.Argv, "--max-old-space-size=384") {
		t.Fatalf("problem runtime policy was omitted: %+v", node)
	}

	python := `{"lang":"PYTHON3","work_measurement":true,"limits":{"time_ms":1000,"memory_mb":256}}`
	stdlib := describeRuntimeOptionsFixture(t, python).RuntimeOptions[0]
	t.Setenv("AONOHAKO_DEFAULT_PYTHON_LIBRARY_MODE", "installed")
	installed := describeRuntimeOptionsFixture(t, python).RuntimeOptions[0]
	if installed.RuntimeOptionsID == stdlib.RuntimeOptionsID {
		t.Fatal("server default Python library mode was omitted")
	}
	explicit := describeRuntimeOptionsFixture(t, strings.Replace(python, `"lang":"PYTHON3"`, `"lang":"PYTHON3","python_library_mode":"installed"`, 1)).RuntimeOptions[0]
	if explicit.RuntimeOptionsID != installed.RuntimeOptionsID {
		t.Fatal("implicit and explicit effective Python library modes differ")
	}
	t.Setenv("AONOHAKO_DEFAULT_PYTHON_LIBRARY_MODE", "stdlib")
	t.Setenv("AONOHAKO_PROBLEM_PYTHON_LIBRARY_MODES", `{"python-problem":"installed"}`)
	mapped := describeRuntimeOptionsFixture(t, strings.Replace(python, `"lang":"PYTHON3"`, `"lang":"PYTHON3","problem_id":"python-problem"`, 1)).RuntimeOptions[0]
	if mapped.RuntimeOptionsID != installed.RuntimeOptionsID {
		t.Fatal("problem Python library policy was omitted")
	}

	path := filepath.Join(t.TempDir(), "conflict.json")
	if err := os.WriteFile(path, []byte(`{"lang":"JAVASCRIPT","problem_id":"p","runtime_profile":"other","work_measurement":true,"limits":{"time_ms":1000,"memory_mb":512}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"describe-runtime-options", "--request", path}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "conflicts with problem policy") {
		t.Fatalf("conflicting policy produced options: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRuntimeOptionsCLIRejectsInvalidOrCancelledInput(t *testing.T) {
	runtimeOptionsCLIEnvironment(t)
	for _, input := range []string{
		`null`,
		`{"lang":"CPP20","runtime_profiel":"fast"}`,
		`{"lang":"CPP20"} {"lang":"PYTHON3"}`,
		`{"lang":"JAVA17","work_measurement":true,"limits":{"time_ms":1000,"memory_mb":256}}`,
	} {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "request.json")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{"describe-runtime-options", "--request", path}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("invalid input produced options: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"describe-runtime-options", "--request", "not-opened.json"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("cancelled command did not stop: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}
