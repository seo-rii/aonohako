package execute

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	if got := sandboxCommandBase([]string{"/usr/bin/true"}); got != "true" {
		t.Fatalf("trusted runtime base = %q, want true", got)
	}
	optWorkspaceRoot := "/opt/aonohako-work/run-1"
	if got := sandboxCommandBase([]string{filepath.Join(optWorkspaceRoot, "box", "dotnet")}, optWorkspaceRoot); got != "" {
		t.Fatalf("workspace runtime under trusted root = %q, want untrusted empty base", got)
	}
}

func TestSandboxCommandBaseRecognizesSystemBEAMRuntime(t *testing.T) {
	root := t.TempDir()
	trustedRoot := filepath.Join(root, "usr", "lib", "erlang")
	runtimePath := filepath.Join(trustedRoot, "erts-15.2.7", "bin", "beam.smp")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("create BEAM runtime directory: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("create BEAM runtime: %v", err)
	}
	command := []string{
		"/usr/bin/env",
		"ERL_AFLAGS=+S 1:1",
		runtimePath,
	}
	commandBase := sandboxCommandBaseWithTrustedRoots(command, []string{trustedRoot}, filepath.Join(root, "work", "run-1"))
	if commandBase != "beam.smp" {
		t.Fatalf("sandboxCommandBase() = %q, want trusted BEAM runtime", commandBase)
	}
	if got := addressSpaceLimitBytes(commandBase, 768); got < 8<<30 {
		t.Fatalf("BEAM address-space limit = %d, want at least 8 GiB", got)
	}
}

func TestSandboxCommandBaseRecognizesSystemRRuntime(t *testing.T) {
	root := t.TempDir()
	trustedRoot := filepath.Join(root, "usr", "lib", "R")
	runtimePath := filepath.Join(trustedRoot, "bin", "exec", "R")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatalf("create R runtime directory: %v", err)
	}
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("create R runtime: %v", err)
	}
	command := []string{
		"/usr/bin/env",
		"R_HOME=" + trustedRoot,
		runtimePath,
	}
	commandBase := sandboxCommandBaseWithTrustedRoots(command, []string{trustedRoot}, filepath.Join(root, "work", "run-1"))
	if commandBase != "R" {
		t.Fatalf("sandboxCommandBase() = %q, want trusted R runtime", commandBase)
	}
	if got, want := security.OpenFileLimitForCommand(commandBase), security.OpenFileLimitForCommand("R"); got != want {
		t.Fatalf("R open-file limit = %d, want %d", got, want)
	}
}

func TestSandboxCommandBaseRecognizesSystemJavaRuntime(t *testing.T) {
	javaPath, err := exec.LookPath("java")
	if err != nil {
		t.Skip("java is not installed")
	}
	javaPath, err = filepath.Abs(javaPath)
	if err != nil {
		t.Fatalf("resolve java path: %v", err)
	}
	if got := sandboxCommandBase([]string{javaPath}, "/work/run-1"); got != "java" {
		resolvedPath, _ := filepath.EvalSymlinks(javaPath)
		t.Fatalf("sandboxCommandBase(%q -> %q) = %q, want trusted Java runtime", javaPath, resolvedPath, got)
	}
}

func TestSandboxTrustedRootsCoverRuntimeImageSymlinkTargets(t *testing.T) {
	trustedRoots := sandboxTrustedExecutableRoots()
	for name, targetPath := range map[string]string{
		"BEAM":          "/usr/lib/erlang/erts-15.2.7/bin/beam.smp",
		"Elixir":        "/usr/lib/elixir/bin/elixir",
		"Java":          "/usr/lib/jvm/java-21-openjdk-amd64/bin/java",
		"Julia":         "/usr/local/julia/bin/julia",
		"R":             "/usr/lib/R/bin/exec/R",
		"SWI-Prolog":    "/usr/lib/swi-prolog/bin/x86_64-linux/swipl",
		"system binary": "/usr/bin/true",
	} {
		if !isPathWithinTrustedRoots(targetPath, trustedRoots) {
			t.Errorf("%s runtime target %q is outside sandbox trusted roots", name, targetPath)
		}
	}
}

func TestSandboxCommandBasePreservesTrustedLauncherAcrossSystemSymlinks(t *testing.T) {
	root := t.TempDir()
	systemBinRoot := filepath.Join(root, "usr", "bin")
	elixirRoot := filepath.Join(root, "usr", "lib", "elixir")
	javaRoot := filepath.Join(root, "usr", "lib", "jvm")
	prologRoot := filepath.Join(root, "usr", "lib", "swi-prolog")
	localBinRoot := filepath.Join(root, "usr", "local", "bin")
	juliaRoot := filepath.Join(root, "usr", "local", "julia")
	trustedRoots := []string{
		systemBinRoot,
		elixirRoot,
		javaRoot,
		prologRoot,
		localBinRoot,
		juliaRoot,
	}
	tests := []struct {
		name       string
		launcher   string
		targetPath string
	}{
		{
			name:       "elixir",
			launcher:   filepath.Join(systemBinRoot, "elixir"),
			targetPath: filepath.Join(elixirRoot, "bin", "elixir"),
		},
		{
			name:       "java",
			launcher:   filepath.Join(systemBinRoot, "java"),
			targetPath: filepath.Join(javaRoot, "default-java", "bin", "java"),
		},
		{
			name:       "swipl",
			launcher:   filepath.Join(systemBinRoot, "swipl"),
			targetPath: filepath.Join(prologRoot, "bin", "swipl"),
		},
		{
			name:       "julia",
			launcher:   filepath.Join(localBinRoot, "julia"),
			targetPath: filepath.Join(juliaRoot, "bin", "julia"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(tc.launcher), 0o755); err != nil {
				t.Fatalf("create launcher directory: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(tc.targetPath), 0o755); err != nil {
				t.Fatalf("create runtime directory: %v", err)
			}
			if err := os.WriteFile(tc.targetPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatalf("create runtime executable: %v", err)
			}
			if err := os.Symlink(tc.targetPath, tc.launcher); err != nil {
				t.Fatalf("create launcher symlink: %v", err)
			}
			if got := sandboxCommandBaseWithTrustedRoots([]string{tc.launcher}, trustedRoots); got != tc.name {
				t.Fatalf("sandboxCommandBaseWithTrustedRoots(%q) = %q, want %q", tc.launcher, got, tc.name)
			}
		})
	}
}

func TestSandboxCommandBaseRejectsTrustedLauncherIntoWorkspace(t *testing.T) {
	root := t.TempDir()
	trustedRoot := filepath.Join(root, "usr", "bin")
	workspaceRoot := filepath.Join(root, "work", "run-1")
	targetPath := filepath.Join(workspaceRoot, "box", "java")
	launcher := filepath.Join(trustedRoot, "java")
	for _, dir := range []string{trustedRoot, filepath.Dir(targetPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create directory: %v", err)
		}
	}
	if err := os.WriteFile(targetPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("create workspace executable: %v", err)
	}
	if err := os.Symlink(targetPath, launcher); err != nil {
		t.Fatalf("create launcher symlink: %v", err)
	}
	if got := sandboxCommandBaseWithTrustedRoots([]string{launcher}, []string{trustedRoot, workspaceRoot}, workspaceRoot); got != "" {
		t.Fatalf("workspace-targeting launcher base = %q, want untrusted empty base", got)
	}
}

func TestSandboxCommandBaseRejectsUntrustedSymlinkEndpoints(t *testing.T) {
	root := t.TempDir()
	trustedLauncherRoot := filepath.Join(root, "usr", "bin")
	trustedTargetRoot := filepath.Join(root, "usr", "lib", "jvm")
	untrustedRoot := filepath.Join(root, "tmp")
	for _, dir := range []string{trustedLauncherRoot, trustedTargetRoot, untrustedRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create directory: %v", err)
		}
	}
	trustedTarget := filepath.Join(trustedTargetRoot, "java")
	untrustedTarget := filepath.Join(untrustedRoot, "java-target")
	for _, target := range []string{trustedTarget, untrustedTarget} {
		if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("create executable: %v", err)
		}
	}
	trustedRoots := []string{trustedLauncherRoot, trustedTargetRoot}
	tests := []struct {
		name     string
		launcher string
		target   string
	}{
		{name: "untrusted launcher", launcher: filepath.Join(untrustedRoot, "java"), target: trustedTarget},
		{name: "untrusted target", launcher: filepath.Join(trustedLauncherRoot, "java-untrusted-target"), target: untrustedTarget},
		{name: "broken target", launcher: filepath.Join(trustedLauncherRoot, "java-broken"), target: filepath.Join(untrustedRoot, "missing")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Symlink(tc.target, tc.launcher); err != nil {
				t.Fatalf("create symlink: %v", err)
			}
			if got := sandboxCommandBaseWithTrustedRoots([]string{tc.launcher}, trustedRoots); got != "" {
				t.Fatalf("sandboxCommandBaseWithTrustedRoots(%q) = %q, want untrusted empty base", tc.launcher, got)
			}
		})
	}
}

func TestJVMRunLanguagesDoNotUseAddressSpaceProximityMLE(t *testing.T) {
	for _, runLang := range []string{"clojure", "groovy", "java", "kotlin-jvm", "scala"} {
		if !isJVMRunLang(runLang) {
			t.Errorf("isJVMRunLang(%q) = false", runLang)
		}
		if !isTrustedJVMRuntime(runLang, "java") {
			t.Errorf("isTrustedJVMRuntime(%q, \"java\") = false", runLang)
		}
		if isTrustedJVMRuntime(runLang, "") {
			t.Errorf("isTrustedJVMRuntime(%q, \"\") = true", runLang)
		}
		if addressSpaceProximityCanClassifyMLE("", runLang) {
			t.Errorf("%s must not use RLIMIT_AS proximity to classify MLE when command detection is unavailable", runLang)
		}
	}
	if isJVMRunLang("javascript") {
		t.Fatal("JavaScript must not be classified as a JVM run language")
	}
}

func TestFastWorkspaceScriptDoesNotInheritSandboxHelperVMSize(t *testing.T) {
	requireSandboxSupport(t)

	svc := New()
	for attempt := 1; attempt <= 100; attempt++ {
		resp := svc.Run(context.Background(), &model.RunRequest{
			Lang: "binary",
			Binaries: []model.Binary{{
				Name: "fast.sh",
				DataB64: b64(`#!/bin/sh
while IFS= read -r line; do
	printf 'seen:%s\n' "$line"
done
`),
				Mode: "exec",
			}},
			Stdin:          "ok\n",
			ExpectedStdout: "seen:ok\n",
			Limits:         model.Limits{TimeMs: 1000, MemoryMB: 128},
		}, Hooks{})
		if resp.Status != model.RunStatusAccepted {
			t.Fatalf("fast script attempt %d inherited helper accounting: %+v", attempt, resp)
		}
	}
}

func TestSandboxRunDoesNotChargeParentHeapToTargetMemory(t *testing.T) {
	requireSandboxSupport(t)

	// Cloud Run keeps the API server and its request payloads in the parent
	// process. The forked sandbox child briefly inherits those resident pages
	// before it execs the helper and target, but they are not target memory.
	parentHeap := make([]byte, 96<<20)
	for offset := 0; offset < len(parentHeap); offset += os.Getpagesize() {
		parentHeap[offset] = 1
	}

	resp := New().Run(context.Background(), &model.RunRequest{
		Lang: "binary",
		Binaries: []model.Binary{{
			Name:    "run.sh",
			DataB64: b64("#!/bin/sh\nprintf 'ok\\n'\n"),
			Mode:    "exec",
		}},
		EntryPoint:     "run.sh",
		ExpectedStdout: "ok\n",
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	runtime.KeepAlive(parentHeap)

	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("target inherited parent memory accounting: %+v", resp)
	}
	if resp.MemoryKB > 64*1024 {
		t.Fatalf("target memory includes parent heap: %+v", resp)
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

func TestSandboxTargetSynchronizationWaitsForExecTransition(t *testing.T) {
	raw, err := os.ReadFile("sandbox_exec.go")
	if err != nil {
		t.Fatalf("read sandbox_exec.go: %v", err)
	}
	body := string(raw)
	ready := strings.Index(body, "case err := <-readyCh:")
	processBaseline := strings.Index(body, "cpuBaselineNs, _ = timing.ProcessCPUTimeNs")
	release := strings.Index(body, "targetReleaseWrite.Write")
	execTransition := strings.Index(body, "case err := <-targetExecCh:")
	targetAccounting := strings.Index(body, "targetStarted := true")
	if ready < 0 || processBaseline < 0 || release < 0 || execTransition < 0 || targetAccounting < 0 {
		t.Fatalf("sandbox_exec.go is missing target synchronization anchors")
	}
	if !(ready < processBaseline && processBaseline < release && release < execTransition && execTransition < targetAccounting) {
		t.Fatalf("baselines must precede target release and accounting must wait for the exec transition")
	}
	baselineBlock := body[processBaseline:release]
	if !strings.Contains(baselineBlock, "cgroupLimitBaseline = stats") || !strings.Contains(baselineBlock, "cgroupCPUBaselineMicros = stats.CPUUsageMicros") {
		t.Fatalf("cgroup CPU and event baselines must be captured before target release")
	}
	if !strings.Contains(baselineBlock, "targetReadyRead.Read") {
		t.Fatalf("parent must prepare to observe the close-on-exec transition before target release")
	}
	if strings.Contains(body, `os.Readlink(fmt.Sprintf("/proc/%d/exe"`) || strings.Contains(body, "targetStartGraceDeadline") {
		t.Fatalf("target start must not depend on procfs polling or a grace timeout")
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
	if !strings.Contains(finalAccounting, "stats.MemoryPeakBytes") || !strings.Contains(finalAccounting, "result.MemoryKB = peakKB") {
		t.Fatalf("final cgroup accounting must report aggregate memory.peak")
	}
	processAccounting := body[finalEnd:]
	if strings.Contains(processAccounting, "Maxrss") {
		t.Fatalf("no-cgroup accounting must not charge pre-exec parent/helper RSS to the target")
	}
}
