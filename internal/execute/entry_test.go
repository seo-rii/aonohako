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
