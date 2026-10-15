package execute

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/entrypolicy"
	"aonohako/internal/model"
)

func TestEntryCommandUsesImageOwnedRunner(t *testing.T) {
	req := &model.RunRequest{EntryMode: "state", Limits: model.Limits{MemoryMB: 256}}
	args := buildCommand("/tmp/Main.entry.json", "entry", req)
	if args[0] != "node" || !slices.Contains(args, entrypolicy.RunnerPath) || args[len(args)-1] != "state" || !slices.Contains(args, "--disallow-code-generation-from-strings") {
		t.Fatalf("command=%v", args)
	}
}

func TestEntryStateVerdictsAndBudgetExit(t *testing.T) {
	req := &model.RunRequest{Lang: "entry", EntryMode: "state", ExpectedStdout: `{"version":1,"objects":{"sprite":{"x":30}}}`}
	for _, tc := range []struct{ actual, status string }{
		{`{"version":1,"objects":{"sprite":{"x":30,"visible":true}}}`, model.RunStatusAccepted},
		{`{"version":1,"objects":{"sprite":{"x":31}}}`, model.RunStatusWA},
		{`invalid private state`, model.RunStatusWA},
	} {
		status, _, _, source := evaluateRunStatus(context.Background(), Workspace{}, req, execResult{Status: "OK"}, []byte(tc.actual), "stdout", "", nil, config.DefaultRuntimeTuningConfig(), "")
		if status != tc.status || source != "entry_state" {
			t.Fatalf("verdict=%s source=%s", status, source)
		}
	}
	code := 124
	status, reason, source := classifyRunStatusWithoutOutput(req, execResult{Status: "OK", ExitCode: &code})
	if status != model.RunStatusTLE || source != "entry_limit" || reason == "" {
		t.Fatalf("limit verdict=%s %s %s", status, reason, source)
	}
	req.Lang = "javascript"
	status, _, _ = classifyRunStatusWithoutOutput(req, execResult{Status: "OK", ExitCode: &code})
	if status != model.RunStatusRE {
		t.Fatalf("other runtime exit 124 became %s", status)
	}
}

func TestEntryExecutionFailuresTakePriorityOverOutputJudging(t *testing.T) {
	budgetExit, runtimeExit := 124, 7
	for _, mode := range []string{"stdio", "state"} {
		for _, tc := range []struct {
			name       string
			result     execResult
			wantStatus string
			wantSource string
		}{
			{"logical budget", execResult{Status: "OK", ExitCode: &budgetExit, StdoutTruncated: true}, model.RunStatusTLE, "entry_limit"},
			{"reported memory", execResult{Status: "OK", ExitCode: &budgetExit, MemoryKB: 256*1024 + 1, StdoutTruncated: true}, model.RunStatusMLE, "memory_reported"},
			{"final cpu time", execResult{Status: "OK", ExitCode: &budgetExit, CPUTimeMs: 3001, StdoutTruncated: true}, model.RunStatusTLE, "cpu_time_final"},
			{"sandbox memory", execResult{Status: model.RunStatusMLE, Reason: "memory limit exceeded", VerdictSource: "memory_cgroup", ExitCode: &budgetExit, StdoutTruncated: true}, model.RunStatusMLE, "memory_cgroup"},
			{"runtime error", execResult{Status: "OK", ExitCode: &runtimeExit, StdoutTruncated: true}, model.RunStatusRE, "exit_code"},
			{"output limit", execResult{Status: "OK", StdoutTruncated: true}, model.RunStatusRE, "stdout_limit"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				req := &model.RunRequest{Lang: "entry", EntryMode: mode, ExpectedStdout: "invalid private state expectation", Limits: model.Limits{TimeMs: 3000, MemoryMB: 256}}
				status, _, reason, source := evaluateRunStatus(context.Background(), Workspace{}, req, tc.result, []byte("truncated private output"), "stdout", "", nil, config.DefaultRuntimeTuningConfig(), "")
				if status != tc.wantStatus || source != tc.wantSource {
					t.Fatalf("verdict=%s source=%s; want %s %s", status, source, tc.wantStatus, tc.wantSource)
				}
				if tc.result.Reason != "" && reason != tc.result.Reason {
					t.Fatalf("execution reason lost: %q", reason)
				}
			})
		}
	}
}

// Runs in the privileged runtime image CI. Ordinary developer workspaces may
// lack helper privilege or installed image-owned Entry assets.
func TestRunEntryStateDoesNotExposeHiddenState(t *testing.T) {
	forceDirectMode(t)
	if _, err := os.Stat(entrypolicy.RunnerPath); err != nil {
		t.Skip("Entry runtime image assets are unavailable")
	}
	project := map[string]any{"scenes": []any{map[string]any{"id": "scene"}}, "objects": []any{map[string]any{"id": "sprite", "scene": "scene", "objectType": "sprite", "entity": map[string]any{"x": 30}, "script": []any{}}}}
	envelope, _ := json.Marshal(map[string]any{"format": "entry-submission", "version": 1, "engineVersion": "4.0.23", "graderVersion": "entry-v1", "mode": "state", "project": project})
	req := &model.RunRequest{Lang: "entry", EntryMode: "state", Binaries: []model.Binary{{Name: entrypolicy.SourceName, DataB64: b64(string(envelope))}}, Stdin: `{"version":1,"seed":1,"ticks":1,"events":[],"initial":{"objects":{"sprite":{"x":12345}}}}`, ExpectedStdout: `{"version":1,"objects":{"sprite":{"x":30}}}`, Limits: model.Limits{TimeMs: 3000, MemoryMB: 256}}
	logs := 0
	result := New().Run(context.Background(), req, Hooks{OnLog: func(string, string) { logs++ }})
	if result.Status != model.RunStatusWA || result.Stdout != "" || result.Stderr != "" || logs != 0 {
		t.Fatalf("private state leaked or verdict failed: %+v logs=%d", result, logs)
	}
}
