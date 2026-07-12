package execute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/security"
)

func TestSandboxCommandBaseRejectsWorkspaceTrustedNameSpoof(t *testing.T) {
	workspaceRoot := "/tmp/aonohako-work/run-1"
	for _, name := range []string{
		"aonohako-gdl-run",
		"aonohako-gleam-run",
		"aonohako-tla-run",
		"dotnet",
		"ghdl",
		"vvp",
		"wasmtime",
	} {
		command := []string{filepath.Join(workspaceRoot, "box", name)}
		if got := sandboxCommandBase(command); got != "" {
			t.Errorf("sandboxCommandBase(%q) = %q, want untrusted empty base", command[0], got)
		}
	}

	if got := sandboxCommandBase([]string{"/usr/local/bin/aonohako-tla-run"}); got != "aonohako-tla-run" {
		t.Fatalf("trusted runtime base = %q, want aonohako-tla-run", got)
	}
	optWorkspaceRoot := "/opt/aonohako-work/run-1"
	if got := sandboxCommandBase([]string{filepath.Join(optWorkspaceRoot, "box", "dotnet")}, optWorkspaceRoot); got != "" {
		t.Fatalf("workspace runtime under trusted root = %q, want untrusted empty base", got)
	}
}

func TestSandboxCommandBaseRecognizesSystemBEAMRuntime(t *testing.T) {
	command := []string{
		"/usr/bin/env",
		"ERL_AFLAGS=+S 1:1",
		"/usr/lib/erlang/erts-15.2.7/bin/beam.smp",
	}
	if got := sandboxCommandBase(command, "/work/run-1"); got != "beam.smp" {
		t.Fatalf("sandboxCommandBase() = %q, want trusted BEAM runtime", got)
	}
	if got := addressSpaceLimitBytes(sandboxCommandBase(command, "/work/run-1"), 768); got < 8<<30 {
		t.Fatalf("BEAM address-space limit = %d, want at least 8 GiB", got)
	}
}

func TestSandboxCommandBaseRecognizesSystemRRuntime(t *testing.T) {
	command := []string{
		"/usr/bin/env",
		"R_HOME=/usr/lib/R",
		"/usr/lib/R/bin/exec/R",
	}
	commandBase := sandboxCommandBase(command, "/work/run-1")
	if commandBase != "R" {
		t.Fatalf("sandboxCommandBase() = %q, want trusted R runtime", commandBase)
	}
	if got, want := security.OpenFileLimitForCommand(commandBase), security.OpenFileLimitForCommand("R"); got != want {
		t.Fatalf("R open-file limit = %d, want %d", got, want)
	}
}

func TestRunBlocksForkForSubmittedTrustedRuntimeName(t *testing.T) {
	requireSandboxSupport(t)

	binary := buildCTestBinary(t, `
#include <errno.h>
#include <stdio.h>
#include <unistd.h>

int main(void) {
	pid_t child = fork();
	if (child == -1 && (errno == EPERM || errno == EACCES || errno == ENOSYS)) {
		puts("blocked");
		return 0;
	}
	if (child == 0) {
		_exit(0);
	}
	puts("forked");
	return 0;
}
`)
	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "aonohako-tla-run",
			DataB64: binary,
			Mode:    "exec",
		}},
		ExpectedStdout: "blocked\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("submitted trusted-name binary bypassed process policy: %+v", resp)
	}
}

func TestEvaluateRunStatusRejectsTruncatedStdoutPrefix(t *testing.T) {
	req := &model.RunRequest{
		ExpectedStdout: "expected",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 64, OutputBytes: len("expected")},
	}
	res := execResult{Status: "OK", Stdout: []byte("expected"), StdoutTruncated: true}

	status, score, reason, source := evaluateRunStatus(
		context.Background(),
		Workspace{},
		req,
		res,
		res.Stdout,
		"stdout",
		"",
		nil,
		config.DefaultRuntimeTuningConfig(),
		"",
	)
	if status != model.RunStatusWA {
		t.Fatalf("status = %q, want %q", status, model.RunStatusWA)
	}
	if score != nil {
		t.Fatalf("score = %v, want nil", score)
	}
	if reason != "stdout exceeded output limit" {
		t.Fatalf("reason = %q, want stdout limit reason", reason)
	}
	if source != "stdout_limit" {
		t.Fatalf("source = %q, want stdout_limit", source)
	}
}

func TestRunSPJRejectsTruncatedScore(t *testing.T) {
	requireSandboxSupport(t)

	resp := New().Run(context.Background(), &model.RunRequest{
		Lang:     "python",
		Binaries: []model.Binary{{Name: "main.py", DataB64: b64("print('actual')\n")}},
		Limits:   model.Limits{TimeMs: 3000, MemoryMB: 128},
		SPJ: &model.SPJSpec{
			Binary:    &model.Binary{Name: "checker.py", DataB64: b64("#!/usr/bin/env python3\nprint('1garbage')\n")},
			Lang:      "python",
			EmitScore: true,
			Limits:    &model.Limits{TimeMs: 1000, MemoryMB: 128, OutputBytes: 1},
		},
	}, Hooks{})
	if resp.Status != model.RunStatusRE || resp.Reason != "spj failed: score output exceeded output limit" {
		t.Fatalf("truncated SPJ score must fail closed: %+v", resp)
	}
}

func TestInteractiveResponseDoesNotMaskResourceFailures(t *testing.T) {
	exitZero := 0
	req := &model.RunRequest{Limits: model.Limits{TimeMs: 100, MemoryMB: 64}}
	interactorReq := &model.RunRequest{Limits: model.Limits{TimeMs: 100, MemoryMB: 64}}

	tests := []struct {
		name          string
		contestantRes execResult
		interactorRes execResult
		wantStatus    string
		wantSource    string
	}{
		{
			name:          "contestant memory limit",
			contestantRes: execResult{Status: model.RunStatusMLE, VerdictSource: "memory_cgroup"},
			interactorRes: execResult{Status: "OK", ExitCode: &exitZero},
			wantStatus:    model.RunStatusMLE,
			wantSource:    "contestant:memory_cgroup",
		},
		{
			name:          "contestant workspace limit",
			contestantRes: execResult{Status: model.RunStatusWLE, VerdictSource: "workspace_bytes"},
			interactorRes: execResult{Status: "OK", ExitCode: &exitZero},
			wantStatus:    model.RunStatusWLE,
			wantSource:    "contestant:workspace_bytes",
		},
		{
			name:          "interactor final CPU limit",
			contestantRes: execResult{Status: "OK", ExitCode: &exitZero},
			interactorRes: execResult{Status: "OK", ExitCode: &exitZero, CPUTimeMs: 101},
			wantStatus:    model.RunStatusRE,
			wantSource:    "interactor:cpu_time_final",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := interactiveResponse(req, interactorReq, tc.contestantRes, tc.interactorRes, 10, false, false, 1024)
			if resp.Status != tc.wantStatus || resp.VerdictSource != tc.wantSource {
				t.Fatalf("interactiveResponse() = %+v, want status %q source %q", resp, tc.wantStatus, tc.wantSource)
			}
		})
	}
}

func TestRunInteractiveEnforcesInteractorWallLimit(t *testing.T) {
	requireSandboxSupport(t)

	started := time.Now()
	resp := New().Run(context.Background(), &model.RunRequest{
		Lang:     "python",
		Binaries: []model.Binary{{Name: "main.py", DataB64: b64("import time\ntime.sleep(10)\n")}},
		Interactor: &model.InteractorSpec{
			Lang:     "python",
			Binaries: []model.Binary{{Name: "interactor.py", DataB64: b64("import time\ntime.sleep(0.5)\n")}},
			Limits:   &model.Limits{TimeMs: 50, MemoryMB: 128},
		},
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 128},
	}, Hooks{})
	if resp.Status != model.RunStatusRE || resp.VerdictSource != "interactor:wall_time" {
		t.Fatalf("interactor wall limit must fail the run: %+v", resp)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("interactor wall limit took %s, want under 1s", elapsed)
	}
}

func TestStreamImageEventsRetainsEventsBeyondPerReadLimit(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	imgPath := filepath.Join(workDir, "__img__", "images.jsonl")
	if err := os.MkdirAll(filepath.Dir(imgPath), 0o755); err != nil {
		t.Fatalf("mkdir image directory: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	events := make([]string, 0, maxImageEventsPerRead+1)
	done := make(chan struct{})
	go func() {
		streamImageEvents(ctx, ws, "__img__/images.jsonl", func(_ string, b64 string, _ int64) {
			mu.Lock()
			events = append(events, b64)
			mu.Unlock()
		})
		close(done)
	}()

	var payload string
	for i := 0; i < maxImageEventsPerRead+1; i++ {
		payload += fmt.Sprintf("{\"mime\":\"image/png\",\"b64\":\"%d\",\"ts\":%d}\n", i, i+1)
	}
	if err := os.WriteFile(imgPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("write image events: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(events)
		mu.Unlock()
		if count == maxImageEventsPerRead+1 {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("emitted %d image events, want %d: %v", len(events), maxImageEventsPerRead+1, events)
}

func TestStreamImageEventsFlushesFinalUnterminatedEvent(t *testing.T) {
	workDir := t.TempDir()
	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		t.Fatalf("prepareWorkspaceDirs: %v", err)
	}
	imgPath := filepath.Join(workDir, "__img__", "images.jsonl")
	if err := os.MkdirAll(filepath.Dir(imgPath), 0o755); err != nil {
		t.Fatalf("mkdir image directory: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	events := make([]string, 0, 1)
	done := make(chan struct{})
	go func() {
		streamImageEvents(ctx, ws, "__img__/images.jsonl", func(_ string, b64 string, _ int64) {
			mu.Lock()
			events = append(events, b64)
			mu.Unlock()
		})
		close(done)
	}()

	if err := os.WriteFile(imgPath, []byte(`{"mime":"image/png","b64":"final","ts":1}`), 0o644); err != nil {
		t.Fatalf("write final image event: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0] != "final" {
		t.Fatalf("final image events = %v, want [final]", events)
	}
}

func TestSandboxCgroupAccountingResetsTargetCPUAndChecksFinalEvents(t *testing.T) {
	raw, err := os.ReadFile("sandbox_exec.go")
	if err != nil {
		t.Fatalf("read sandbox_exec.go: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "targetStarted = true\n\t\t\t\t\tmaxCPUTimeMs = 0") {
		t.Fatalf("target accounting must discard the sandbox helper CPU maximum")
	}
	if !strings.Contains(body, "cgroupCPUBaselineMicros = stats.CPUUsageMicros\n\t\t\tcgroupLimitBaseline = stats") {
		t.Fatalf("cgroup event baseline must be captured when the helper joins the run group")
	}
	finalStart := strings.Index(body, "result.MemoryKB = maxRSSKB")
	finalEnd := strings.Index(body, "if result.Status == \"OK\" {\n\t\tusage, err := workspacequota.Scan")
	if finalStart < 0 || finalEnd <= finalStart {
		t.Fatalf("could not locate final cgroup accounting block")
	}
	finalAccounting := body[finalStart:finalEnd]
	if strings.Contains(finalAccounting, "targetStarted && result.Status == \"OK\"") {
		t.Fatalf("final cgroup events must be checked even when a fast target exits before polling detects it")
	}
	if !strings.Contains(finalAccounting, "cgroupLimitBreachSince(stats, cgroupLimitBaseline, cgroupLimitBaselineSet)") {
		t.Fatalf("final cgroup accounting must compare events to the captured baseline")
	}
}
