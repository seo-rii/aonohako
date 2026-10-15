//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/api"
	"aonohako/internal/compile"
	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/isolation/cgroup"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/sandbox"
)

func TestMain(m *testing.M) {
	// Execute and compile services launch this binary as their sandbox helper.
	if sandbox.MaybeRunFromEnv() {
		return
	}
	os.Exit(m.Run())
}

// TestSIGTERMServerHelper is invoked only by the integration parent. Its HTTP
// services, signal handler, and shutdown function are real; a short grace keeps
// this process-level cleanup regression practical in the root sandbox CI job.
func TestSIGTERMServerHelper(t *testing.T) {
	controlDir := os.Getenv("AONOHAKO_SIGNAL_TEST_CONTROL")
	if controlDir == "" {
		t.Skip("integration child helper")
	}
	cgroupParent := os.Getenv("AONOHAKO_SIGNAL_TEST_CGROUP")
	target := platform.DeploymentTargetDev
	if cgroupParent != "" {
		target = platform.DeploymentTargetSelfHosted
	} else if os.Getenv("AONOHAKO_SIGNAL_TEST_MODE") == "communication" {
		target = platform.DeploymentTargetCloudRun
	}
	cfg := config.Config{
		MaxActiveRuns: 1, MaxPendingQueue: 1, HeartbeatInterval: 50 * time.Millisecond,
		CommunicationEnabled: true, CommunicationMemoryBudgetMB: 24576,
		CommunicationCPUCount: 8, CommunicationWallBudgetMs: 600000,
		WorkRootMaxBytes: 1 << 30,
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{DeploymentTarget: target, ExecutionTransport: platform.ExecutionTransportEmbedded, SandboxBackend: platform.SandboxBackendHelper},
			Cgroup:   config.CgroupConfig{ParentDir: cgroupParent},
		},
	}
	service := api.NewWithServices(cfg, signalCompileRunner{fixturePath: filepath.Join(controlDir, "pause")}, execute.NewWithConfig(cfg))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: service.Handler()}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	if err := os.WriteFile(filepath.Join(controlDir, "ready"), []byte("http://"+listener.Addr().String()), 0o600); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	stop()
	shutdownErr := shutdownServer(server, service, 50*time.Millisecond)
	if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("HTTP serve failed: %v", err)
	}
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("expected forced shutdown of active sandbox work, got %v", shutdownErr)
	}
	if err := os.WriteFile(filepath.Join(controlDir, "cleanup-complete"), []byte("forced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Production main also exits 1 after the grace deadline and completed cleanup.
	os.Exit(1)
}

type signalCompileRunner struct{ fixturePath string }

func (s signalCompileRunner) Run(ctx context.Context, _ *model.CompileRequest) model.CompileResponse {
	// Exercise the compiler's actual process-tree cancellation and capture path
	// deterministically, without depending on how long a particular language
	// compiler takes. Source preparation/artifact collection are outside this test.
	workDir, err := os.MkdirTemp(os.Getenv("AONOHAKO_WORK_ROOT"), "signal-compile-*")
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	defer os.RemoveAll(workDir)
	fixture, err := os.ReadFile(s.fixturePath)
	if err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	binDir := filepath.Join(workDir, ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	bin := filepath.Join(binDir, "pause")
	if err := os.WriteFile(bin, fixture, 0o755); err != nil {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: err.Error()}
	}
	stdout, stderr, status, reason := compile.RunSandboxedCommand(ctx, workDir, bin, nil, nil)
	return model.CompileResponse{Status: status, Stdout: stdout, Stderr: stderr, Reason: reason}
}

func TestSIGTERMCleansActiveSandboxJobs(t *testing.T) {
	if os.Geteuid() != 0 {
		signalSandboxUnavailable(t, "SIGTERM sandbox integration requires root")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		signalSandboxUnavailable(t, "SIGTERM sandbox integration requires a C compiler")
	}
	controlBase := t.TempDir()
	fixturePath := filepath.Join(controlBase, "pause")
	compiler := exec.Command(cc, "-O2", "-x", "c", "-", "-o", fixturePath)
	compiler.Stdin = strings.NewReader(`
#include <stdio.h>
#include <unistd.h>

int main(void) {
    FILE *marker = fopen("shutdown-pid", "w");
    if (!marker) return 2;
    fprintf(marker, "%d\n", (int)getpid());
    fclose(marker);
    for (;;) pause();
}
`)
	if output, err := compiler.CombinedOutput(); err != nil {
		t.Fatalf("compile pause fixture: %v\n%s", err, output)
	}
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		mode string
		pids int
	}{{"ordinary", 1}, {"interactive", 2}, {"communication", 3}, {"compile_command", 1}} {
		t.Run(scenario.mode, func(t *testing.T) {
			testSIGTERMCleanup(t, scenario.mode, scenario.pids, fixture)
		})
	}
}

func testSIGTERMCleanup(t *testing.T, mode string, wantPIDs int, fixture []byte) {
	t.Helper()
	control := t.TempDir()
	if err := os.WriteFile(filepath.Join(control, "pause"), fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	workRoot, err := os.MkdirTemp(os.Getenv("AONOHAKO_WORK_ROOT"), "aonohako-signal-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(workRoot) })
	if err := os.Chmod(workRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cgroupParent := signalTestCgroup(t, mode)
	childPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(childPath, "-test.run=^TestSIGTERMServerHelper$", "-test.count=1")
	child.Env = append(os.Environ(),
		"AONOHAKO_SIGNAL_TEST_CONTROL="+control, "AONOHAKO_SIGNAL_TEST_MODE="+mode,
		"AONOHAKO_SIGNAL_TEST_CGROUP="+cgroupParent, "AONOHAKO_WORK_ROOT="+workRoot,
		"AONOHAKO_DEPLOYMENT_TARGET=dev", "AONOHAKO_EXECUTION_TRANSPORT=embedded", "AONOHAKO_SANDBOX_BACKEND=helper",
	)
	var childOutput bytes.Buffer
	child.Stdout, child.Stderr = &childOutput, &childOutput
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	exited := false
	var targetPIDs []int
	t.Cleanup(func() {
		if !exited {
			_ = child.Process.Kill()
			<-childDone
		}
		for _, pid := range targetPIDs {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	var address string
	for address == "" && time.Now().Before(deadline) {
		if ready, err := os.ReadFile(filepath.Join(control, "ready")); err == nil {
			address = string(ready)
			break
		}
		select {
		case err := <-childDone:
			exited = true
			t.Fatalf("child exited before listening: %v\n%s", err, childOutput.Bytes())
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if address == "" {
		t.Fatal("child server did not announce its listener")
	}
	path, payload := signalRequest(t, mode, fixture)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("request rejected: status=%d body=%s", response.StatusCode, body)
	}
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		_, _ = io.Copy(io.Discard, response.Body)
	}()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		targetPIDs = signalTargetPIDs(t, workRoot)
		if len(targetPIDs) == wantPIDs {
			break
		}
		select {
		case <-streamDone:
			t.Fatalf("request completed before %d targets started; found %v", wantPIDs, targetPIDs)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(targetPIDs) != wantPIDs {
		t.Fatalf("found sandbox target PIDs %v, want %d", targetPIDs, wantPIDs)
	}
	for _, pid := range targetPIDs {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("sandbox target %d was not active before SIGTERM: %v", pid, err)
		}
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-childDone:
		exited = true
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Fatalf("forced shutdown exit = %v, want exit code 1\n%s", err, childOutput.Bytes())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not finish canceled sandbox cleanup")
	}
	if complete, err := os.ReadFile(filepath.Join(control, "cleanup-complete")); err != nil || string(complete) != "forced\n" {
		t.Fatalf("child did not finish the shutdown lifecycle: %v\n%s", err, childOutput.Bytes())
	}
	for _, pid := range targetPIDs {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("sandbox target PID %d remains after server exit: %v", pid, err)
		}
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil || len(entries) != 0 {
		t.Errorf("sandbox workspaces remain after server exit: entries=%v err=%v", entries, err)
	}
	if cgroupParent != "" {
		entries, err := os.ReadDir(cgroupParent)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				t.Errorf("sandbox cgroup remains after server exit: %s", entry.Name())
			}
		}
	}
	select {
	case <-streamDone:
	case <-time.After(time.Second):
		t.Error("HTTP stream stayed open after child exit")
	}
}

func signalRequest(t *testing.T, mode string, fixture []byte) (string, []byte) {
	t.Helper()
	var request any
	path := "/execute"
	binary := model.Binary{Name: "pause", DataB64: base64.StdEncoding.EncodeToString(fixture), Mode: "exec"}
	limits := model.Limits{TimeMs: 30000, MemoryMB: 64, OutputBytes: 1024, WorkspaceBytes: 8 << 20}
	switch mode {
	case "compile_command":
		path = "/compile"
		request = model.CompileRequest{Lang: "CPP17", Sources: []model.Source{{Name: "main.cpp", DataB64: base64.StdEncoding.EncodeToString([]byte("int main(){}"))}}}
	case "communication":
		request = model.RunRequest{
			Programs:      []model.RunProgram{{ID: "participant", Lang: "binary", Binaries: []model.Binary{binary}}, {ID: "manager", Lang: "binary", Binaries: []model.Binary{binary}}},
			Communication: &model.CommunicationSpec{Version: 1, ParticipantProgramID: "participant", ManagerProgramID: "manager", ParticipantCount: 2, ResultProtocol: "manager-result-v1"},
			Limits:        limits,
		}
	default:
		req := model.RunRequest{Lang: "binary", Binaries: []model.Binary{binary}, Limits: limits}
		if mode == "interactive" {
			req.Interactor = &model.InteractorSpec{Lang: "binary", Binaries: []model.Binary{binary}, Limits: &limits}
		}
		request = req
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return path, payload
}

func signalTargetPIDs(t *testing.T, root string) []int {
	t.Helper()
	var pids []int
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Name() != "shutdown-pid" || !entry.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The file can be observed between fopen and fprintf during startup.
		if len(raw) == 0 {
			return nil
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 1 {
			return fmt.Errorf("invalid fixture PID %q", raw)
		}
		pids = append(pids, pid)
		return nil
	})
	if err != nil {
		t.Fatalf("scan active sandbox PID markers: %v", err)
	}
	return pids
}

func signalTestCgroup(t *testing.T, mode string) string {
	t.Helper()
	parent := strings.TrimSpace(os.Getenv("AONOHAKO_COMMUNICATION_TEST_CGROUP"))
	if parent == "" || mode == "compile_command" {
		t.Log("cgroup cleanup assertions unavailable: no delegated parent or direct compile-command wrapper")
		return ""
	}
	if err := cgroup.ValidateParent(parent, []string{"cpu", "memory", "pids"}); err != nil {
		t.Fatalf("invalid delegated test cgroup: %v", err)
	}
	group, err := cgroup.CreateRunGroup(parent, cgroup.RunName("signal-test"), cgroup.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := group.KillAndRemoveWithRetry(time.Second); err != nil {
			t.Errorf("remove private test cgroup: %v", err)
		}
	})
	if err := cgroup.EnableControllers(group.Path, []string{"cpu", "memory", "pids"}); err != nil {
		t.Fatal(err)
	}
	return group.Path
}

func signalSandboxUnavailable(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("AONOHAKO_ENFORCE_SANDBOX_TESTS") != "" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}
